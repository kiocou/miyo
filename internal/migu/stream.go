package migu

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"netease-music-mygo/internal/netease"
)

const (
	h5XORKey = "Jk8qzuePiJ1qE3mDYhLQ3T73DtDoAhLP"
	pcHexKey = "ccTWaprX2aWmTIgA"
)

// ErrUnavailable is returned when Migu cannot supply a playable full stream.
var ErrUnavailable = errors.New("migu: song unavailable")

type qualitySpec struct {
	neteaseLevel string
	toneFlag     string
	audioType    string
	resourceType string
}

// qualityLadder lists every supported Migu audio quality from highest to lowest
// along with the NetEase level alias and Migu resourceType parameter observed in APK 8.9.6.
var qualityLadder = []qualitySpec{
	{neteaseLevel: "jymaster", toneFlag: "ZQ32", audioType: "flac", resourceType: "2"},
	{neteaseLevel: "hires", toneFlag: "ZQ24", audioType: "flac", resourceType: "2"},
	{neteaseLevel: "jyeffect", toneFlag: "AV3A", audioType: "mp4", resourceType: "2"},
	{neteaseLevel: "sky", toneFlag: "Z3D", audioType: "flac", resourceType: "2"},
	{neteaseLevel: "lossless", toneFlag: "SQ", audioType: "flac", resourceType: "E"},
	{neteaseLevel: "exhigh", toneFlag: "HQ", audioType: "mp3", resourceType: "2"},
	{neteaseLevel: "standard", toneFlag: "PQ", audioType: "mp3", resourceType: "2"},
	{neteaseLevel: "lq", toneFlag: "LQ", audioType: "mp3", resourceType: "3"},
}

func startQualityIndex(level string) int {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "ZQ32", "JYMASTER":
		return 0
	case "ZQ24", "ZQ", "HIRES":
		return 1
	case "AV3A", "JYEFFECT":
		return 2
	case "Z3D", "SKY":
		return 3
	case "SQ", "LOSSLESS":
		return 4
	case "HQ", "EXHIGH", "HIGHER":
		return 5
	case "LQ":
		return 7
	default:
		// standard / PQ or empty defaults to PQ unless user picks a higher/lower tier.
		return 6
	}
}

// ToneForLevel normalizes a NetEase level or Migu tone into a canonical Migu toneFlag.
func ToneForLevel(level string) string {
	return qualityLadder[startQualityIndex(level)].toneFlag
}

// DecryptH5Payload decodes the obfuscated binary response returned by
// /strategy/listen-url/h5/v2.4 (magic 0xAB 0xCD 0x01 <seed>).
func DecryptH5Payload(raw []byte) ([]byte, error) {
	if len(raw) < 4 {
		return nil, errors.New("migu h5 payload too short")
	}
	if raw[0] != 0xab || raw[1] != 0xcd || raw[2] != 0x01 {
		return nil, errors.New("migu h5 bad magic")
	}
	seed := int(raw[3])
	key := []byte(h5XORKey)
	out := make([]byte, len(raw)-4)
	for i, b := range raw[4:] {
		out[i] = byte((int(b) + seed - int(key[i%len(key)])) & 0xff)
	}
	return out, nil
}

// DecryptPCPayload decodes the hex-encoded encrypted JSON returned by
// /strategy/pc/listen/v2.0 and /strategy/listen-url/v3.x.
func DecryptPCPayload(raw []byte) ([]byte, error) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, "{") {
		return []byte(s), nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("migu pc payload hex decode: %w", err)
	}
	key := []byte(pcHexKey)
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = byte((int(c) - int(key[i%len(key)])) & 0xff)
	}
	return out, nil
}

// streamHeaders returns the HTTP headers expected by freetyst CDN servers.
func streamHeaders() map[string]string {
	return map[string]string{
		"Referer":    webReferer,
		"User-Agent": webUA,
	}
}

// rewriteQualityURL rewrites a Migu CDN URL returned for one tone (usually PQ)
// into candidate URLs for the target quality spec.
func rewriteQualityURL(rawURL string, target qualitySpec) []string {
	if rawURL == "" {
		return nil
	}
	rawURL = httpsURL(rawURL)
	decoded, err := url.PathUnescape(rawURL)
	if err != nil {
		decoded = rawURL
	}

	type repl struct {
		sub  string
		with []string
	}
	switch target.toneFlag {
	case "LQ":
		u := replaceAny(rawURL,
			[]string{"MP3_128_16_Stero", "MP3_320_16_Stero"},
			"MP3_64_16_Stero",
		)
		return dedupeStrings([]string{u})
	case "PQ":
		if strings.Contains(decoded, "MP3_128_16_Stero") {
			return []string{rawURL}
		}
		u := replaceAny(rawURL,
			[]string{"MP3_320_16_Stero", "MP3_64_16_Stero"},
			"MP3_128_16_Stero",
		)
		return dedupeStrings([]string{rawURL, u})
	case "HQ":
		if strings.Contains(decoded, "MP3_320_16_Stero") {
			return []string{rawURL}
		}
		u := replaceAny(rawURL,
			[]string{"MP3_128_16_Stero", "MP3_64_16_Stero"},
			"MP3_320_16_Stero",
		)
		return dedupeStrings([]string{u})
	case "SQ":
		var out []string
		for _, spec := range []struct{ prefix, ext string }{
			{"%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/flac", ".flac"},
			{"%E6%97%A0%E6%8D%9F/flac", ".flac"},
			{"%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/m4a", ".m4a"},
		} {
			if u := replacePathQuality(rawURL, spec.prefix, spec.ext); u != "" {
				out = append(out, u)
			}
		}
		return dedupeStrings(out)
	case "ZQ24", "ZQ":
		var out []string
		for _, prefix := range []string{"%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/flac_24bit", "%E8%87%B3%E8%87%BB%E9%9F%B3%E8%B4%A82.0/flac", "%E8%87%B3%E8%87%BB%E9%9F%B3%E8%B4%A8/flac"} {
			if u := replacePathQuality(rawURL, prefix, ".flac"); u != "" {
				out = append(out, u)
			}
		}
		return dedupeStrings(out)
	case "ZQ32":
		var out []string
		for _, prefix := range []string{"%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/flac_32bit", "%E8%87%B3%E8%87%BB%E6%AF%8D%E5%B8%A6/flac"} {
			if u := replacePathQuality(rawURL, prefix, ".flac"); u != "" {
				out = append(out, u)
			}
		}
		return dedupeStrings(out)
	default:
		_ = repl{}
		return nil
	}
}

func replaceAny(s string, olds []string, newVal string) string {
	for _, old := range olds {
		if strings.Contains(s, old) {
			return strings.ReplaceAll(s, old, newVal)
		}
	}
	return s
}

var pathQualityRe = regexp.MustCompile(`(%E6%A0%87%E6%B8%85%E9%AB%98%E6%B8%85|标清高清|%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD|歌曲下载)/(MP3_128_16_Stero|MP3_320_16_Stero|MP3_64_16_Stero)`)

func replacePathQuality(rawURL, replacement, newExt string) string {
	u := rawURL
	if pathQualityRe.MatchString(u) {
		u = pathQualityRe.ReplaceAllString(u, replacement)
	} else if idx := strings.Index(u, "/MP3_"); idx >= 0 {
		if slash := strings.Index(u[idx+1:], "/"); slash >= 0 {
			u = u[:idx+1] + "flac" + u[idx+1+slash:]
		}
	} else {
		return ""
	}
	if i := strings.Index(u, "?"); i >= 0 {
		base, q := u[:i], u[i:]
		base = swapAudioExt(base, newExt)
		return base + q
	}
	return swapAudioExt(u, newExt)
}

func swapAudioExt(path, ext string) string {
	lower := strings.ToLower(path)
	for _, old := range []string{".mp3", ".flac", ".m4a", ".wav"} {
		if strings.HasSuffix(lower, old) {
			return path[:len(path)-len(old)] + ext
		}
	}
	return path
}

func dedupeStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SongURLContext resolves the highest available stream at or below level for a Migu track.
func SongURLContext(ctx context.Context, tr netease.Track, level string) (*netease.Stream, error) {
	if tr.CopyrightID == "" && tr.SourceID == "" {
		return nil, ErrUnavailable
	}
	startIdx := startQualityIndex(level)
	reqSpec := qualityLadder[startIdx]

	var seedURLs []string
	var directCandidates []struct {
		url  string
		spec qualitySpec
	}

	if HasCookie() {
		tried := map[string]bool{}
		for _, spec := range []qualitySpec{reqSpec, specForTone("SQ", ""), specForTone("HQ", "")} {
			if startQualityIndex(spec.toneFlag) < startIdx || tried[spec.toneFlag] {
				continue
			}
			tried[spec.toneFlag] = true
			if u, gotSpec, ok := fetchListenPC(ctx, tr, spec); ok && u != "" {
				directCandidates = append(directCandidates, struct {
					url  string
					spec qualitySpec
				}{url: u, spec: gotSpec})
				seedURLs = append(seedURLs, u)
				if startQualityIndex(gotSpec.toneFlag) <= startQualityIndex(spec.toneFlag) {
					break
				}
			}
		}
	}
	if u, err := fetchListenH5(ctx, tr, "PQ"); err == nil && u != "" {
		seedURLs = append(seedURLs, u)
	}
	if u, spec, err := fetchListenV21(ctx, tr, reqSpec); err == nil && u != "" {
		directCandidates = append(directCandidates, struct {
			url  string
			spec qualitySpec
		}{url: u, spec: spec})
		seedURLs = append(seedURLs, u)
	}

	// Probe direct higher-quality candidate first if the account/API returned one
	// at or below the requested ceiling and matching its audio quality tier.
	for _, dc := range directCandidates {
		if startQualityIndex(dc.spec.toneFlag) < startIdx {
			continue
		}
		if dc.spec.toneFlag != reqSpec.toneFlag && startQualityIndex(dc.spec.toneFlag) > startIdx {
			// API downgraded a VIP tier to PQ/LQ; still allow CDN rewrite to try higher tiers first.
			continue
		}
		at := streamAudioType(dc.url, dc.spec.audioType)
		if size, ok := probeAudioURL(ctx, dc.url, at, tr.Duration); ok {
			return &netease.Stream{
				URL:     dc.url,
				Source:  Source,
				Headers: streamHeaders(),
				Size:    size,
				Type:    at,
				Level:   dc.spec.neteaseLevel,
			}, nil
		}
	}

	seedURLs = dedupeStrings(seedURLs)
	if len(seedURLs) == 0 {
		return nil, ErrUnavailable
	}

	for i := startIdx; i < len(qualityLadder); i++ {
		spec := qualityLadder[i]
		for _, seed := range seedURLs {
			for _, cand := range rewriteQualityURL(seed, spec) {
				at := streamAudioType(cand, spec.audioType)
				if size, ok := probeAudioURL(ctx, cand, at, tr.Duration); ok {
					return &netease.Stream{
						URL:     cand,
						Source:  Source,
						Headers: streamHeaders(),
						Size:    size,
						Type:    at,
						Level:   spec.neteaseLevel,
					}, nil
				}
			}
		}
	}
	return nil, ErrUnavailable
}

func streamAudioType(rawURL, fallback string) string {
	base := rawURL
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	switch {
	case strings.HasSuffix(strings.ToLower(base), ".flac"):
		return "flac"
	case strings.HasSuffix(strings.ToLower(base), ".m4a"):
		return "m4a"
	case strings.HasSuffix(strings.ToLower(base), ".mp4"):
		return "mp4"
	case strings.HasSuffix(strings.ToLower(base), ".mp3"):
		return "mp3"
	default:
		return fallback
	}
}

func fetchListenH5(ctx context.Context, tr netease.Track, tone string) (string, error) {
	cid := firstNonEmpty(tr.SourceID, tr.SongID)
	crid := firstNonEmpty(tr.CopyrightID, tr.SourceID)
	q := url.Values{
		"contentId":             {cid},
		"copyrightId":           {crid},
		"resourceType":          {"2"},
		"netType":               {"01"},
		"toneFlag":              {tone},
		"scene":                 {""},
		"lowerQualityContentId": {cid},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strategyBase+"/strategy/listen-url/h5/v2.4?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("birth", "h5page")
	req.Header.Set("channel", h5Channel)
	req.Header.Set("Referer", h5Referer)
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("location-data", "30.6698676660,104.1229614820")
	req.Header.Set("location-info", "")

	raw, _, _, err := doRequest(ctx, req)
	if err != nil {
		return "", err
	}
	plain := raw
	if len(raw) >= 3 && raw[0] == 0xab && raw[1] == 0xcd && raw[2] == 0x01 {
		plain, err = DecryptH5Payload(raw)
		if err != nil {
			return "", err
		}
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(plain, &resp); err != nil {
		return "", err
	}
	return httpsURL(resp.Data.URL), nil
}

func fetchListenV21(ctx context.Context, tr netease.Track, reqSpec qualitySpec) (string, qualitySpec, error) {
	cid := firstNonEmpty(tr.SourceID, tr.SongID)
	rtype := firstNonEmpty(reqSpec.resourceType, "E")
	q := url.Values{
		"netType":               {"01"},
		"resourceType":          {rtype},
		"songId":                {firstNonEmpty(tr.SongID, cid)},
		"toneFlag":              {reqSpec.toneFlag},
		"lowerQualityContentId": {cid},
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			URL         string `json:"url"`
			AudioFormat string `json:"audioFormatType"`
		} `json:"data"`
	}
	err := getJSON(ctx, strategyBase+"/MIGUM2.0/v2.1/content/listen-url?"+q.Encode(), map[string]string{
		"channel":    defaultChn,
		"Referer":    webReferer,
		"User-Agent": webUA,
	}, &resp)
	if err != nil || resp.Data.URL == "" {
		return "", qualitySpec{}, ErrUnavailable
	}
	spec := specForTone(resp.Data.AudioFormat, resp.Data.URL)
	return httpsURL(resp.Data.URL), spec, nil
}

func fetchListenPC(ctx context.Context, tr netease.Track, reqSpec qualitySpec) (string, qualitySpec, bool) {
	cid := firstNonEmpty(tr.SourceID, tr.SongID)
	crid := firstNonEmpty(tr.CopyrightID, tr.SourceID)
	rtype := firstNonEmpty(reqSpec.resourceType, "2")
	q := url.Values{
		"copyrightId":  {crid},
		"contentId":    {cid},
		"toneFlag":     {reqSpec.toneFlag},
		"resourceType": {rtype},
		"netType":      {"01"},
	}
	if tr.SongID != "" {
		q.Set("songId", tr.SongID)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strategyBase+"/strategy/pc/listen/v2.0?"+q.Encode(), nil)
	if err != nil {
		return "", qualitySpec{}, false
	}
	req.Header.Set("channel", defaultChn)
	req.Header.Set("Referer", webReferer)
	req.Header.Set("User-Agent", webUA)

	raw, _, _, err := doRequest(ctx, req)
	if err != nil {
		return "", qualitySpec{}, false
	}
	plain, err := DecryptPCPayload(raw)
	if err != nil {
		return "", qualitySpec{}, false
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			URL         string `json:"url"`
			FormatType  string `json:"formatType"`
			AudioFormat string `json:"audioFormatType"`
			ResourceURL string `json:"resourceUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(plain, &resp); err != nil {
		return "", qualitySpec{}, false
	}
	u := firstNonEmpty(resp.Data.URL, resp.Data.ResourceURL)
	if u == "" {
		return "", qualitySpec{}, false
	}
	return httpsURL(u), specForTone(firstNonEmpty(resp.Data.AudioFormat, resp.Data.FormatType), u), true
}

func specForTone(tone, rawURL string) qualitySpec {
	lowerURL := strings.ToLower(rawURL)
	if lowerURL != "" {
		// Trust concrete URL path/extension over ambiguous or empty API tone hints.
		switch {
		case strings.Contains(lowerURL, "flac_32bit"):
			return findSpec("ZQ32")
		case strings.Contains(lowerURL, "flac_24bit"):
			return findSpec("ZQ24")
		case strings.Contains(lowerURL, ".flac") || strings.Contains(lowerURL, ".m4a"):
			if strings.ToUpper(strings.TrimSpace(tone)) == "ZQ24" || strings.ToUpper(strings.TrimSpace(tone)) == "ZQ32" {
				return findSpec(tone)
			}
			return findSpec("SQ")
		case strings.Contains(lowerURL, "mp3_320"):
			return findSpec("HQ")
		case strings.Contains(lowerURL, "mp3_128"):
			return findSpec("PQ")
		case strings.Contains(lowerURL, "mp3_64"):
			return findSpec("LQ")
		}
	}
	if spec, ok := lookupSpec(tone); ok {
		return spec
	}
	return findSpec("PQ")
}

func lookupSpec(tone string) (qualitySpec, bool) {
	upper := strings.ToUpper(strings.TrimSpace(tone))
	if upper == "ZQ" {
		upper = "ZQ24"
	}
	for _, s := range qualityLadder {
		if s.toneFlag == upper {
			return s, true
		}
	}
	return qualitySpec{}, false
}

func findSpec(tone string) qualitySpec {
	if s, ok := lookupSpec(tone); ok {
		return s
	}
	return qualityLadder[len(qualityLadder)-2]
}

// probeAudioURL verifies the target URL responds with real audio bytes (not an HTML/JSON
// error page or truncated 60s preview clip) and returns its total byte length.
func probeAudioURL(ctx context.Context, rawURL, expectedType string, durationMS int64) (int64, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Range", "bytes=0-63")
	for k, v := range streamHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml") {
		return 0, false
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if !validAudioMagic(head, expectedType) {
		return 0, false
	}
	total := parseContentTotal(resp.Header.Get("Content-Range"), resp.ContentLength)
	// Reject genuine 30s-60s audition clips when the real song is significantly longer.
	// Note h5/v2.4 often reports auditionsLength=60 metadata even on full 4MB-31MB files,
	// so byte size against track duration is the reliable check.
	if total > 0 {
		if durationMS > 90_000 && total < durationMS*10 {
			return 0, false
		}
		if expectedType == "flac" && durationMS > 30_000 && total < durationMS*25 {
			return 0, false
		}
	}
	return total, true
}

func validAudioMagic(head []byte, expectedType string) bool {
	if len(head) < 4 {
		return false
	}
	isMP4 := len(head) >= 8 && string(head[4:8]) == "ftyp"
	isMP3 := bytes.HasPrefix(head, []byte("ID3")) || (head[0] == 0xff && (head[1]&0xe0) == 0xe0)
	switch expectedType {
	case "flac":
		return bytes.HasPrefix(head, []byte("fLaC"))
	case "m4a", "mp4":
		return isMP4
	case "mp3":
		return isMP3
	default:
		return bytes.HasPrefix(head, []byte("fLaC")) || isMP3 || isMP4
	}
}

func parseContentTotal(cr string, cl int64) int64 {
	if cr != "" {
		if _, after, ok := strings.Cut(cr, "/"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	if cl > 0 {
		return cl
	}
	return 0
}

// Match searches Migu for an equivalent of a NetEase track (typically unavailable
// or VIP preview) and returns the best full playable stream and matched Migu track.
func Match(ctx context.Context, target netease.Track, level string) (*netease.Stream, *netease.Track, error) {
	query := strings.TrimSpace(target.Name)
	if query == "" {
		return nil, nil, ErrUnavailable
	}
	if artists := target.ArtistNames(); artists != "" {
		query += " " + artists
	}
	candidates, err := SearchContext(ctx, query, 10)
	if err != nil || len(candidates) == 0 {
		candidates, err = SearchContext(ctx, target.Name, 10)
		if err != nil || len(candidates) == 0 {
			return nil, nil, ErrUnavailable
		}
	}

	type scored struct {
		tr    netease.Track
		score int
	}
	var ranked []scored
	for _, c := range candidates {
		if s, ok := scoreCandidate(target, c); ok {
			ranked = append(ranked, scored{tr: c, score: s})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	for i := 0; i < len(ranked) && i < 3; i++ {
		cand := ranked[i].tr
		if st, err := SongURLContext(ctx, cand, level); err == nil && st != nil {
			return st, &cand, nil
		}
	}
	return nil, nil, ErrUnavailable
}

var (
	parensRe = regexp.MustCompile(`[\(（\[【][^\)）\]】]*[\)）\]】]`)
	nonAlnum = regexp.MustCompile(`[\s\p{P}\p{S}]+`)
)

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, " - "); i > 0 {
		s = s[:i]
	}
	s = parensRe.ReplaceAllString(s, "")
	return nonAlnum.ReplaceAllString(s, "")
}

func hasVariantTag(name string, tags ...string) bool {
	lower := strings.ToLower(name)
	for _, t := range tags {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

func scoreCandidate(want, cand netease.Track) (int, bool) {
	wantTitle := normalizeTitle(want.Name)
	candTitle := normalizeTitle(cand.Name)
	if wantTitle == "" || candTitle == "" {
		return 0, false
	}

	// Reject companion/live/dj/remix/cover variants unless the requested song has the same qualifier.
	for _, pair := range [][]string{
		{"伴奏", "instrumental", "off vocal", "纯音乐"},
		{"live", "现场", "concert"},
		{"dj", "remix", "串烧"},
		{"cover", "翻唱"},
	} {
		if !hasVariantTag(want.Name, pair...) && hasVariantTag(cand.Name, pair...) {
			return 0, false
		}
	}

	score := 0
	switch {
	case candTitle == wantTitle:
		score += 100
	case strings.Contains(candTitle, wantTitle) || strings.Contains(wantTitle, candTitle):
		score += 55
	default:
		return 0, false
	}

	if want.Duration > 0 && cand.Duration > 0 {
		diff := want.Duration - cand.Duration
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff > 12_000:
			return 0, false
		case diff <= 3_000:
			score += 35
		case diff <= 7_000:
			score += 15
		}
	}

	if sharesArtist(want.Artists, cand.Artists) {
		score += 60
	} else if len(want.Artists) > 0 && len(cand.Artists) > 0 {
		score -= 40
	}

	if want.Album.Name != "" && cand.Album.Name != "" && normalizeTitle(want.Album.Name) == normalizeTitle(cand.Album.Name) {
		score += 15
	}
	if score < 80 {
		return 0, false
	}
	return score, true
}

func sharesArtist(a, b []netease.Artist) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	norm := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	for _, x := range a {
		nx := norm(x.Name)
		if nx == "" {
			continue
		}
		for _, y := range b {
			ny := norm(y.Name)
			if ny == "" {
				continue
			}
			if nx == ny || strings.Contains(nx, ny) || strings.Contains(ny, nx) {
				return true
			}
		}
	}
	return false
}

// QualityStatus summarizes live availability of one Migu quality tier.
type QualityStatus struct {
	Tone  string
	Level string
	OK    bool
	Size  int64
}

// ProbeQuality checks which Migu quality tiers currently resolve to full audio
// files using live public reference tracks.
func ProbeQuality(ctx context.Context) ([]QualityStatus, error) {
	refs := []netease.Track{
		{
			Name:        "说好不哭",
			Duration:    222000,
			Source:      Source,
			SourceID:    "600919000007816057",
			CopyrightID: "6902739Z004",
			SongID:      "1139607021",
		},
		{
			Name:        "晴天",
			Duration:    270000,
			Source:      Source,
			SourceID:    "600902000006889366",
			CopyrightID: "60054701923",
			SongID:      "3790007",
		},
	}
	var ref netease.Track
	var seed string
	var err error
	for _, cand := range refs {
		seed, err = fetchListenH5(ctx, cand, "PQ")
		if err == nil && seed != "" {
			ref = cand
			break
		}
	}
	if seed == "" {
		return nil, fmt.Errorf("migu probe failed: %w", err)
	}
	out := make([]QualityStatus, 0, len(qualityLadder))
	for _, spec := range qualityLadder {
		st := QualityStatus{Tone: spec.toneFlag, Level: spec.neteaseLevel}
		if HasCookie() {
			if u, gotSpec, ok := fetchListenPC(ctx, ref, spec); ok && gotSpec.toneFlag == spec.toneFlag {
				at := streamAudioType(u, spec.audioType)
				if sz, good := probeAudioURL(ctx, u, at, ref.Duration); good {
					st.OK = true
					st.Size = sz
				}
			}
		}
		if !st.OK {
			for _, u := range rewriteQualityURL(seed, spec) {
				at := streamAudioType(u, spec.audioType)
				if sz, good := probeAudioURL(ctx, u, at, ref.Duration); good {
					st.OK = true
					st.Size = sz
					break
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// ProbeTrackQualitiesContext returns the exact Migu quality toneFlags that
// resolve to full playable streams for tr under the current network/credentials,
// ordered from highest to lowest.
func ProbeTrackQualitiesContext(ctx context.Context, tr netease.Track) ([]string, error) {
	if tr.CopyrightID == "" && tr.SourceID == "" {
		return nil, ErrUnavailable
	}
	fmts := tr.Qualities
	if len(fmts) == 0 {
		fmts, _ = TrackFormatsContext(ctx, tr)
	}
	allowed := map[string]bool{}
	for _, f := range fmts {
		allowed[f] = true
	}
	seed, err := fetchListenH5(ctx, tr, "PQ")
	if err != nil && !HasCookie() {
		return nil, err
	}

	var candidates []qualitySpec
	for _, spec := range qualityLadder {
		if len(allowed) > 0 && !allowed[spec.toneFlag] {
			continue
		}
		if !HasCookie() && (spec.toneFlag == "ZQ32" || spec.toneFlag == "AV3A" || spec.toneFlag == "Z3D") {
			continue
		}
		candidates = append(candidates, spec)
	}

	okFlags := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for idx, spec := range candidates {
		idx, spec := idx, spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			if HasCookie() {
				if u, gotSpec, ok := fetchListenPC(ctx, tr, spec); ok && gotSpec.toneFlag == spec.toneFlag {
					at := streamAudioType(u, spec.audioType)
					if _, good := probeAudioURL(ctx, u, at, tr.Duration); good {
						okFlags[idx] = true
						return
					}
				}
			}
			if seed != "" {
				for _, u := range rewriteQualityURL(seed, spec) {
					at := streamAudioType(u, spec.audioType)
					if _, good := probeAudioURL(ctx, u, at, tr.Duration); good {
						okFlags[idx] = true
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	var out []string
	for idx, ok := range okFlags {
		if ok {
			out = append(out, candidates[idx].toneFlag)
		}
	}
	if len(out) == 0 && len(fmts) > 0 {
		return fmts, nil
	}
	return out, nil
}
