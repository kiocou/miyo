// Package migu implements catalog, streaming, and lyric clients for Migu Music.
package migu

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"netease-music-mygo/internal/netease"
)

// Source identifies tracks, albums, artists, and playlists coming from Migu.
const Source = "migu"

var (
	jadeiteBase  = "https://jadeite.migu.cn"
	appCBase     = "https://app.c.nf.migu.cn"
	appUBase     = "https://app.u.nf.migu.cn"
	strategyBase = "https://c.musicapp.migu.cn"
	httpClient   = &http.Client{Timeout: 12 * time.Second}
)

const (
	apkCertMD5   = "6cdc72a439cef99a3418d2a78aa28c73"
	searchV3Salt = "yyapp2d16148780a1dcc7408e06336b98cfd50"
	defaultChn   = "014000D"
	h5Channel    = "014X031"
	appUA        = "okhttp/3.14.9"
	webUA        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	webReferer   = "https://music.migu.cn/"
	h5Referer    = "https://y.migu.cn/"
)

var (
	authMu     sync.RWMutex
	rawCookie  string
	aversionID string
	pacmToken  string
	miguUID    string
	deviceMS   = randomHex(16)
)

// SetCookie updates the optional Migu session cookie or bare pacmtoken used
// by stream and PC-listen requests. Passing empty clears saved credentials.
func SetCookie(raw string) {
	raw = strings.TrimSpace(raw)
	authMu.Lock()
	defer authMu.Unlock()
	rawCookie = raw
	aversionID = ""
	pacmToken = ""
	miguUID = ""
	if raw == "" {
		return
	}
	if !strings.Contains(raw, "=") {
		pacmToken = raw
		rawCookie = "pacmtoken=" + raw
		return
	}
	for _, part := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "aversionid":
			aversionID = v
		case "pacmtoken":
			pacmToken = v
		case "uid":
			miguUID = v
		}
	}
}

// HasCookie reports whether any Migu cookie or token is configured.
func HasCookie() bool {
	authMu.RLock()
	defer authMu.RUnlock()
	return rawCookie != "" || pacmToken != "" || aversionID != ""
}

func authSnapshot() (cookie, av, token, uid string) {
	authMu.RLock()
	defer authMu.RUnlock()
	return rawCookie, aversionID, pacmToken, uidOrDefault(miguUID)
}

// CurrentUID returns the parsed Migu user ID from the active session cookie.
func CurrentUID() string {
	_, _, _, uid := authSnapshot()
	return uid
}

func uidOrDefault(uid string) string {
	if uid != "" {
		return uid
	}
	return ""
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "28b6d219234848b8845b2a8f1a8e391f"
	}
	return hex.EncodeToString(b)
}

// VirtualID returns a deterministic negative 63-bit ID for Migu resources.
func VirtualID(kind, sourceID string) int64 {
	return virtualID(kind, sourceID)
}

// virtualID produces a deterministic negative 63-bit ID so Migu tracks,
// albums, artists, and playlists fit NetEase structs without colliding with >0 IDs.
func virtualID(kind, sourceID string) int64 {
	if sourceID == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("migu:"))
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(sourceID))
	return -int64((h.Sum64() & 0x3fffffffffffffff) + 1)
}

// signSearchV3 computes the V3 header signature observed in APK 8.9.6:
// md5(query + certMD5 + salt + ms + base64(msisdn) + timestamp).
func signSearchV3(query, ms, msisdnB64, ts string) string {
	sum := md5.Sum([]byte(query + apkCertMD5 + searchV3Salt + ms + msisdnB64 + ts))
	return hex.EncodeToString(sum[:])
}

func base64Plain(s string) string {
	if s == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func doRequest(ctx context.Context, req *http.Request) ([]byte, http.Header, int, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", appUA)
	}
	if cookie, av, token, uid := authSnapshot(); cookie != "" {
		if req.Header.Get("Cookie") == "" {
			req.Header.Set("Cookie", cookie)
		}
		if av != "" && req.Header.Get("aversionid") == "" {
			req.Header.Set("aversionid", av)
		}
		if token != "" && req.Header.Get("pacmtoken") == "" {
			req.Header.Set("pacmtoken", token)
		}
		if token != "" && req.Header.Get("token") == "" {
			req.Header.Set("token", token)
		}
		if uid != "" && req.Header.Get("uid") == "" {
			req.Header.Set("uid", uid)
		}
	}
	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.Header, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.Header, resp.StatusCode, fmt.Errorf("migu HTTP %d", resp.StatusCode)
	}
	return body, resp.Header, resp.StatusCode, nil
}

func getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	return requestJSON(ctx, http.MethodGet, rawURL, headers, nil, out)
}

func postJSON(ctx context.Context, rawURL string, headers map[string]string, payload, out any) error {
	return requestJSON(ctx, http.MethodPost, rawURL, headers, payload, out)
}

func deleteJSON(ctx context.Context, rawURL string, headers map[string]string, payload, out any) error {
	return requestJSON(ctx, http.MethodDelete, rawURL, headers, payload, out)
}

func pcHeaders() map[string]string {
	return map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
		"Origin":     "https://music.migu.cn",
		"channel":    defaultChn,
		"subchannel": defaultChn,
		"platform":   "H5",
		"version":    "6.8.8",
	}
}

func unwrapJSONBody(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed
	}
	if dec, err := DecryptPCPayload(trimmed); err == nil {
		decTrim := bytes.TrimSpace(dec)
		if len(decTrim) > 0 && (decTrim[0] == '{' || decTrim[0] == '[') {
			return decTrim
		}
	}
	return body
}

func requestJSON(ctx context.Context, method, rawURL string, headers map[string]string, payload, out any) error {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	body, _, _, err := doRequest(ctx, req)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(unwrapJSONBody(body), out); err != nil {
		return fmt.Errorf("migu decode: %w", err)
	}
	return nil
}

func checkCode(code, info string) error {
	if code == "" || code == "000000" {
		return nil
	}
	if info == "" {
		info = code
	}
	// Session expiry or missing token errors must always satisfy errors.Is(err, ErrNotSignedIn),
	// even if a stale pacmtoken cookie is still stored locally.
	if code == "290001" || code == "850005" || code == "100001" || code == "100002" ||
		strings.Contains(info, "登录") || strings.Contains(info, "token") ||
		(!HasCookie() && strings.Contains(info, "uid")) {
		return fmt.Errorf("migu (%s): %s: %w", code, info, netease.ErrNotSignedIn)
	}
	return fmt.Errorf("migu (%s): %s", code, info)
}

// imgItem captures Migu's sized image object array ({imgSizeType, img, webpImg}).
type imgItem struct {
	SizeType string `json:"imgSizeType"`
	Img      string `json:"img"`
	Webp     string `json:"webpImg"`
}

func pickImage(items []imgItem, fallback ...string) string {
	best := ""
	bestRank := -1
	for _, it := range items {
		u := strings.TrimSpace(it.Img)
		if u == "" {
			u = strings.TrimSpace(it.Webp)
		}
		if u == "" {
			continue
		}
		rank := 0
		switch it.SizeType {
		case "03":
			rank = 3
		case "02":
			rank = 2
		case "01":
			rank = 1
		}
		if rank > bestRank {
			bestRank = rank
			best = u
		}
	}
	if best == "" {
		for _, f := range fallback {
			if strings.TrimSpace(f) != "" {
				best = strings.TrimSpace(f)
				break
			}
		}
	}
	return httpsURL(best)
}

func httpsURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return "https://d.musicapp.migu.cn" + u
	}
	return strings.Replace(u, "http://", "https://", 1)
}

// flexInt decodes JSON integers whether Migu encodes them as numbers or strings.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		*n = 0
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		*n = flexInt(f)
		return nil
	}
	return nil
}

// songItem covers both jadeite search v3 song objects and resourceinfo/album/playlist song objects.
type songItem struct {
	ID             string        `json:"id"`
	SongID         string        `json:"songId"`
	ContentID      string        `json:"contentId"`
	CopyrightID    string        `json:"copyrightId"`
	Name           string        `json:"name"`
	SongName       string        `json:"songName"`
	DurationMS     flexInt       `json:"duration"`
	LengthStr      string        `json:"length"`
	Singer         string        `json:"singer"`
	SingerID       string        `json:"singerId"`
	Singers        []singer      `json:"singers"`
	SingerList     []singer      `json:"singerList"`
	ArtistsField   []singer      `json:"artists"`
	Albums         []album       `json:"albums"`
	AlbumName      string        `json:"album"`
	AlbumID        string        `json:"albumId"`
	AlbumImgs      []imgItem     `json:"albumImgs"`
	ImgItems       []imgItem     `json:"imgItems"`
	Img1           string        `json:"img1"`
	Img2           string        `json:"img2"`
	Img3           string        `json:"img3"`
	ImgURL         string        `json:"img"`
	CoverURL       string        `json:"cover"`
	LyricURL       string        `json:"lyricUrl"`
	LrcURL         string        `json:"lrcUrl"`
	TrcURL         string        `json:"trcUrl"`
	MrcURL         string        `json:"mrcUrl"`
	AudioFormats   []formatEntry `json:"audioFormats"`
	NewRateFormats []formatEntry `json:"newRateFormats"`
	RateFormats    []formatEntry `json:"rateFormats"`
}

type formatEntry struct {
	FormatType string `json:"formatType"`
}

func collectFormats(groups ...[]formatEntry) []string {
	present := map[string]bool{}
	for _, g := range groups {
		for _, f := range g {
			t := strings.ToUpper(strings.TrimSpace(f.FormatType))
			if t == "ZQ" {
				t = "ZQ24"
			}
			if t != "" {
				present[t] = true
			}
		}
	}
	if len(present) == 0 {
		return nil
	}
	var out []string
	for _, spec := range qualityLadder {
		if present[spec.toneFlag] {
			out = append(out, spec.toneFlag)
		}
	}
	return out
}

// TrackFormatsContext queries resourceinfo.do for the exact quality tiers
// supported by tr, ordered from highest to lowest.
func TrackFormatsContext(ctx context.Context, tr netease.Track) ([]string, error) {
	if len(tr.Qualities) > 0 {
		return tr.Qualities, nil
	}
	param := "resourceId=" + url.QueryEscape(firstNonEmpty(tr.SourceID, tr.SongID))
	if tr.CopyrightID != "" {
		param = "copyrightId=" + url.QueryEscape(tr.CopyrightID)
	}
	var resp struct {
		Code     string     `json:"code"`
		Resource []songItem `json:"resource"`
	}
	u := appUBase + "/MIGUM2.0/v1.0/content/resourceinfo.do?resourceType=2&" + param
	if err := getJSON(ctx, u, map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
	}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Resource) == 0 {
		return nil, errors.New("migu: empty resourceinfo")
	}
	r := resp.Resource[0]
	return collectFormats(r.NewRateFormats, r.AudioFormats, r.RateFormats), nil
}

type singer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type album struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s songItem) toTrack() netease.Track {
	name := firstNonEmpty(s.Name, s.SongName)
	songID := firstNonEmpty(s.SongID, s.ID)
	cid := s.ContentID
	crid := s.CopyrightID
	srcID := firstNonEmpty(cid, crid, songID)

	var artists []netease.Artist
	for _, list := range [][]singer{s.Singers, s.SingerList, s.ArtistsField} {
		if len(list) > 0 {
			for _, a := range list {
				if a.Name == "" {
					continue
				}
				artists = append(artists, netease.Artist{
					ID:       virtualID("artist", firstNonEmpty(a.ID, a.Name)),
					Name:     a.Name,
					Source:   Source,
					SourceID: a.ID,
				})
			}
			break
		}
	}
	if len(artists) == 0 && s.Singer != "" {
		ids := strings.FieldsFunc(s.SingerID, func(r rune) bool {
			return r == ',' || r == '|'
		})
		for i, nm := range splitNames(s.Singer) {
			sid := ""
			if i < len(ids) {
				sid = strings.TrimSpace(ids[i])
			}
			artists = append(artists, netease.Artist{
				ID:       virtualID("artist", firstNonEmpty(sid, nm)),
				Name:     nm,
				Source:   Source,
				SourceID: sid,
			})
		}
	}

	alID := s.AlbumID
	alName := s.AlbumName
	if len(s.Albums) > 0 {
		if alID == "" {
			alID = s.Albums[0].ID
		}
		if alName == "" {
			alName = s.Albums[0].Name
		}
	}
	pic := pickImage(s.AlbumImgs, "")
	if pic == "" {
		pic = pickImage(s.ImgItems, s.Img3, s.Img2, s.Img1, s.CoverURL, s.ImgURL)
	}

	dur := int64(s.DurationMS)
	if dur <= 0 {
		dur = parseDurationStr(s.LengthStr)
	} else if dur > 0 && dur < 1000 {
		// Some legacy endpoints return seconds in Duration; Migu durations under 1000ms are seconds.
		dur *= 1000
	}

	return netease.Track{
		ID:      virtualID("song", srcID),
		Name:    name,
		Artists: artists,
		Album: netease.Album{
			ID:       virtualID("album", firstNonEmpty(alID, alName)),
			Name:     alName,
			PicURL:   pic,
			Source:   Source,
			SourceID: alID,
		},
		Duration:    dur,
		Source:      Source,
		SourceID:    firstNonEmpty(cid, srcID),
		CopyrightID: crid,
		SongID:      songID,
		Qualities:   collectFormats(s.NewRateFormats, s.AudioFormats, s.RateFormats),
		LrcURL:      httpsURL(firstNonEmpty(s.LrcURL, s.LyricURL)),
		TrcURL:      httpsURL(s.TrcURL),
		MrcURL:      httpsURL(s.MrcURL),
	}
}

func splitNames(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '|' || r == '/' || r == '、'
	}) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 && strings.TrimSpace(s) != "" {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func parseDurationStr(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	if len(parts) == 2 {
		m, err1 := strconv.ParseInt(parts[0], 10, 64)
		sec, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil {
			return m*60000 + int64(sec*1000)
		}
	}
	if len(parts) == 3 {
		h, err1 := strconv.ParseInt(parts[0], 10, 64)
		m, err2 := strconv.ParseInt(parts[1], 10, 64)
		sec, err3 := strconv.ParseFloat(parts[2], 64)
		if err1 == nil && err2 == nil && err3 == nil {
			return (h*3600+m*60)*1000 + int64(sec*1000)
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// searchSwitch builds the JSON searchSwitch object for jadeite searchAll.
func searchSwitch(kind netease.SearchType) string {
	switch kind {
	case netease.SearchAlbums:
		return `{"song":0,"album":1,"singer":0,"tagSong":0,"mvSong":0,"bestShow":0,"songlist":0,"lyricSong":0}`
	case netease.SearchArtists:
		return `{"song":0,"album":0,"singer":1,"tagSong":0,"mvSong":0,"bestShow":0,"songlist":0,"lyricSong":0}`
	case netease.SearchPlaylists:
		return `{"song":0,"album":0,"singer":0,"tagSong":0,"mvSong":0,"bestShow":0,"songlist":1,"lyricSong":0}`
	default:
		return `{"song":1,"album":0,"singer":0,"tagSong":1,"mvSong":0,"bestShow":0,"songlist":0,"lyricSong":0}`
	}
}

type jadeiteSearchResp struct {
	Code       string `json:"code"`
	Info       string `json:"info"`
	SongResult struct {
		TotalCount flexInt    `json:"totalCount"`
		Result     []songItem `json:"result"`
	} `json:"songResultData"`
	AlbumResult struct {
		TotalCount flexInt `json:"totalCount"`
		Result     []struct {
			ID          string    `json:"id"`
			Name        string    `json:"name"`
			PublishDate string    `json:"publishDate"`
			Desc        string    `json:"desc"`
			ImgItems    []imgItem `json:"imgItems"`
			Singer      []singer  `json:"singer"`
		} `json:"result"`
	} `json:"albumResultData"`
	SingerResult struct {
		TotalCount flexInt `json:"totalCount"`
		Result     []struct {
			ID         string    `json:"id"`
			Name       string    `json:"name"`
			AlbumCount flexInt   `json:"albumCount"`
			SongCount  flexInt   `json:"songCount"`
			Pics       []imgItem `json:"singerPicUrl"`
		} `json:"result"`
	} `json:"singerResultData"`
	SongListResult struct {
		TotalCount flexInt `json:"totalCount"`
		Result     []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Intro       string  `json:"intro"`
			PicURL      string  `json:"musicListPicUrl"`
			UserID      string  `json:"userId"`
			UserName    string  `json:"userName"`
			MusicNum    flexInt `json:"musicNum"`
			PlayNum     string  `json:"playNum"`
			MusicNumStr string  `json:"musicNumStr"`
		} `json:"result"`
	} `json:"songListResultData"`
}

// SearchPage queries Migu's catalog by type (songs, albums, artists, playlists),
// falling back to bmw/search/song/v1.0 for song searches if jadeite fails.
func SearchPage(query string, kind netease.SearchType, limit, offset int) (netease.SearchResult, error) {
	return SearchPageContext(context.Background(), query, kind, limit, offset)
}

// SearchPageContext queries Migu's catalog with cancellation support.
func SearchPageContext(ctx context.Context, query string, kind netease.SearchType, limit, offset int) (netease.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return netease.SearchResult{}, nil
	}
	if limit <= 0 {
		limit = 30
	}
	pageNo := offset/limit + 1
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sid := "USS" + randomHex(16) + randomHex(16)
	ms := deviceMS

	q := url.Values{
		"pageNo":       {strconv.Itoa(pageNo)},
		"pageSize":     {strconv.Itoa(limit)},
		"text":         {query},
		"sort":         {"1"},
		"isCopyright":  {"1"},
		"isCorrect":    {"1"},
		"sid":          {sid},
		"searchSwitch": {searchSwitch(kind)},
	}
	headers := map[string]string{
		"uiVersion":  "A_music_3.17.0",
		"ua":         "Android_migu",
		"ms":         ms,
		"timestamp":  ts,
		"msisdn":     "",
		"sign":       signSearchV3(query, ms, "", ts),
		"User-Agent": appUA,
	}

	var resp jadeiteSearchResp
	err := getJSON(ctx, jadeiteBase+"/music_search/v3/search/searchAll?"+q.Encode(), headers, &resp)
	if err != nil && (kind == netease.SearchSongs || kind == 0) {
		return searchSongsBMW(ctx, query, limit, pageNo)
	}
	if err != nil {
		return netease.SearchResult{}, err
	}
	if resp.Code != "" && resp.Code != "000000" {
		if kind == netease.SearchSongs || kind == 0 {
			if out, ferr := searchSongsBMW(ctx, query, limit, pageNo); ferr == nil {
				return out, nil
			}
		}
		return netease.SearchResult{}, fmt.Errorf("migu search (%s): %s", resp.Code, resp.Info)
	}

	var out netease.SearchResult
	switch kind {
	case netease.SearchAlbums:
		for _, a := range resp.AlbumResult.Result {
			var artists []netease.Artist
			for _, sg := range a.Singer {
				artists = append(artists, netease.Artist{
					ID:       virtualID("artist", firstNonEmpty(sg.ID, sg.Name)),
					Name:     sg.Name,
					Source:   Source,
					SourceID: sg.ID,
				})
			}
			out.Albums = append(out.Albums, netease.AlbumProfile{
				Album: netease.Album{
					ID:       virtualID("album", a.ID),
					Name:     a.Name,
					PicURL:   pickImage(a.ImgItems),
					Source:   Source,
					SourceID: a.ID,
				},
				Artists:     artists,
				PublishTime: parseDateMS(a.PublishDate),
				Description: a.Desc,
			})
		}
		out.Total = int(resp.AlbumResult.TotalCount)
	case netease.SearchArtists:
		for _, s := range resp.SingerResult.Result {
			pic := pickImage(s.Pics)
			out.Artists = append(out.Artists, netease.ArtistProfile{
				Artist: netease.Artist{
					ID:       virtualID("artist", s.ID),
					Name:     s.Name,
					Source:   Source,
					SourceID: s.ID,
				},
				Avatar:    pic,
				PicURL:    pic,
				MusicSize: int(s.SongCount),
				AlbumSize: int(s.AlbumCount),
			})
		}
		out.Total = int(resp.SingerResult.TotalCount)
	case netease.SearchPlaylists:
		for _, pl := range resp.SongListResult.Result {
			playCount, _ := strconv.ParseFloat(pl.PlayNum, 64)
			p := netease.Playlist{
				ID:          virtualID("playlist", pl.ID),
				Name:        pl.Name,
				CoverURL:    httpsURL(pl.PicURL),
				PicURL:      httpsURL(pl.PicURL),
				Description: pl.Intro,
				PlayCount:   playCount,
				TrackCount:  int(pl.MusicNum),
				Source:      Source,
				SourceID:    pl.ID,
			}
			p.Creator.Nickname = pl.UserName
			out.Playlists = append(out.Playlists, p)
		}
		out.Total = int(resp.SongListResult.TotalCount)
	default:
		for _, s := range resp.SongResult.Result {
			out.Songs = append(out.Songs, s.toTrack())
		}
		out.Total = int(resp.SongResult.TotalCount)
	}
	return out, nil
}

func searchSongsBMW(ctx context.Context, query string, pageSize, pageNo int) (netease.SearchResult, error) {
	q := url.Values{
		"text":     {query},
		"pageNo":   {strconv.Itoa(pageNo)},
		"pageSize": {strconv.Itoa(pageSize)},
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			Total      flexInt    `json:"total"`
			ResultList []songItem `json:"resultList"`
			Songs      []songItem `json:"songs"`
			Items      []struct {
				Song songItem `json:"song"`
			} `json:"items"`
		} `json:"data"`
	}
	err := getJSON(ctx, appCBase+"/bmw/search/song/v1.0?"+q.Encode(), map[string]string{
		"channel": defaultChn,
	}, &resp)
	if err != nil {
		return netease.SearchResult{}, err
	}
	if resp.Code != "" && resp.Code != "000000" {
		return netease.SearchResult{}, fmt.Errorf("migu bmw search (%s): %s", resp.Code, resp.Info)
	}
	items := resp.Data.ResultList
	if len(items) == 0 {
		items = resp.Data.Songs
	}
	out := netease.SearchResult{Total: int(resp.Data.Total)}
	for _, s := range items {
		out.Songs = append(out.Songs, s.toTrack())
	}
	if len(out.Songs) == 0 {
		for _, it := range resp.Data.Items {
			if it.Song.SongName != "" || it.Song.Name != "" {
				out.Songs = append(out.Songs, it.Song.toTrack())
			}
		}
	}
	return out, nil
}

// Search returns up to limit tracks matching query on Migu.
func Search(query string, limit int) ([]netease.Track, error) {
	return SearchContext(context.Background(), query, limit)
}

// SearchContext is Search with cancellation support.
func SearchContext(ctx context.Context, query string, limit int) ([]netease.Track, error) {
	res, err := SearchPageContext(ctx, query, netease.SearchSongs, limit, 0)
	return res.Songs, err
}

// SearchSuggest returns autocomplete text suggestions for a partial query.
func SearchSuggest(query string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	q := url.Values{"text": {query}}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Items []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := getJSON(context.Background(), appCBase+"/bmw/search/suggest/v3.0?"+q.Encode(), map[string]string{
		"channel": defaultChn,
	}, &resp); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, it := range resp.Data.Items {
		s := strings.TrimSpace(it.Text.Text)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// PlaylistDetail fetches a Migu playlist's metadata and tracks.
func PlaylistDetail(sourceID string) (*netease.Playlist, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil, errors.New("migu: empty playlist id")
	}
	if strings.HasPrefix(sourceID, "rank:") {
		return ChartDetail(strings.TrimPrefix(sourceID, "rank:"))
	}
	if sourceID == "migu-liked" {
		if uid := CurrentUID(); uid != "" {
			if page, err := fetchMyPageMusic(context.Background(), uid); err == nil && page.Data.MyLikeMusicInfo.PlayListID != "" {
				sourceID = page.Data.MyLikeMusicInfo.PlayListID
			}
		}
	}
	var metaResp struct {
		Code     string `json:"code"`
		Resource []struct {
			Title       string  `json:"title"`
			MusicListID string  `json:"musicListId"`
			Summary     string  `json:"summary"`
			PlayNum     string  `json:"opNumItem"`
			MusicNum    int     `json:"musicNum"`
			OwnerName   string  `json:"ownerName"`
			ImgItem     imgItem `json:"imgItem"`
		} `json:"resource"`
	}
	infoURL := appCBase + "/MIGUM2.0/v1.0/content/resourceinfo.do?needSimple=00&resourceType=2021&resourceId=" + url.QueryEscape(sourceID)
	_ = getJSON(context.Background(), infoURL, map[string]string{"User-Agent": webUA, "Referer": webReferer}, &metaResp)

	pl := &netease.Playlist{
		ID:       virtualID("playlist", sourceID),
		Source:   Source,
		SourceID: sourceID,
	}
	if len(metaResp.Resource) > 0 {
		m := metaResp.Resource[0]
		pl.Name = m.Title
		pl.Description = m.Summary
		pl.CoverURL = httpsURL(m.ImgItem.Img)
		pl.PicURL = pl.CoverURL
		pl.TrackCount = m.MusicNum
		pl.Creator.Nickname = m.OwnerName
	}

	const pageSize = 50
	for pageNo := 1; pageNo <= 100; pageNo++ {
		var songsResp struct {
			Code       string     `json:"code"`
			Info       string     `json:"info"`
			TotalCount int        `json:"totalCount"`
			List       []songItem `json:"list"`
		}
		q := url.Values{
			"musicListId": {sourceID},
			"pageNo":      {strconv.Itoa(pageNo)},
			"pageSize":    {strconv.Itoa(pageSize)},
		}
		err := getJSON(context.Background(), appCBase+"/MIGUM2.0/v1.0/user/queryMusicListSongs.do?"+q.Encode(), map[string]string{
			"User-Agent": webUA,
			"Referer":    webReferer,
		}, &songsResp)
		if err != nil {
			if len(pl.Tracks) > 0 {
				break
			}
			return nil, err
		}
		if songsResp.Code != "" && songsResp.Code != "000000" {
			if len(pl.Tracks) > 0 {
				break
			}
			return nil, fmt.Errorf("migu playlist (%s): %s", songsResp.Code, songsResp.Info)
		}
		for _, s := range songsResp.List {
			pl.Tracks = append(pl.Tracks, s.toTrack())
		}
		if pl.TrackCount == 0 && songsResp.TotalCount > 0 {
			pl.TrackCount = songsResp.TotalCount
		}
		if len(songsResp.List) == 0 || (songsResp.TotalCount > 0 && len(pl.Tracks) >= songsResp.TotalCount) || (songsResp.TotalCount == 0 && len(songsResp.List) < pageSize) {
			break
		}
	}
	if pl.TrackCount < len(pl.Tracks) {
		pl.TrackCount = len(pl.Tracks)
	}
	if pl.Name == "" {
		pl.Name = "咪咕歌单"
	}
	if pl.CoverURL == "" && len(pl.Tracks) > 0 {
		pl.CoverURL = pl.Tracks[0].Album.PicURL
		pl.PicURL = pl.CoverURL
	}
	return pl, nil
}

// AlbumDetail fetches a Migu album and its track list.
func AlbumDetail(sourceID string) (*netease.AlbumProfile, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil, errors.New("migu: empty album id")
	}
	var infoResp struct {
		Code     string `json:"code"`
		Resource []struct {
			AlbumID     string     `json:"albumId"`
			Title       string     `json:"title"`
			Singer      string     `json:"singer"`
			SingerID    string     `json:"singerId"`
			PublishDate string     `json:"publishDate"`
			Summary     string     `json:"summary"`
			ImgItems    []imgItem  `json:"imgItems"`
			SongItems   []songItem `json:"songItems"`
		} `json:"resource"`
	}
	infoURL := appCBase + "/MIGUM2.0/v1.0/content/resourceinfo.do?needSimple=00&resourceType=2003&resourceId=" + url.QueryEscape(sourceID)
	err := getJSON(context.Background(), infoURL, map[string]string{"User-Agent": webUA, "Referer": webReferer}, &infoResp)

	al := &netease.AlbumProfile{
		Album: netease.Album{
			ID:       virtualID("album", sourceID),
			Source:   Source,
			SourceID: sourceID,
		},
	}
	if err == nil && len(infoResp.Resource) > 0 {
		r := infoResp.Resource[0]
		al.Name = r.Title
		al.PicURL = pickImage(r.ImgItems)
		al.PublishTime = parseDateMS(r.PublishDate)
		al.Description = r.Summary
		ids := strings.Split(r.SingerID, ",")
		for i, nm := range splitNames(r.Singer) {
			sid := ""
			if i < len(ids) {
				sid = strings.TrimSpace(ids[i])
			}
			al.Artists = append(al.Artists, netease.Artist{
				ID:       virtualID("artist", firstNonEmpty(sid, nm)),
				Name:     nm,
				Source:   Source,
				SourceID: sid,
			})
		}
		for _, s := range r.SongItems {
			tr := s.toTrack()
			if tr.Album.PicURL == "" {
				tr.Album.PicURL = al.PicURL
			}
			al.Songs = append(al.Songs, tr)
		}
	}

	var songsResp struct {
		Code string `json:"code"`
		Data struct {
			SongList []songItem `json:"songList"`
		} `json:"data"`
	}
	q := url.Values{"albumId": {sourceID}, "pageNo": {"1"}, "pageSize": {"100"}}
	songsErr := getJSON(context.Background(), appCBase+"/MIGUM2.0/v1.0/content/queryAlbumSong?"+q.Encode(), map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
	}, &songsResp)
	if songsErr == nil && len(songsResp.Data.SongList) > 0 {
		al.Songs = al.Songs[:0]
		for _, s := range songsResp.Data.SongList {
			tr := s.toTrack()
			if tr.Album.PicURL == "" {
				tr.Album.PicURL = al.PicURL
			}
			al.Songs = append(al.Songs, tr)
		}
	}
	if err != nil && songsErr != nil {
		return nil, err
	}
	if al.Name == "" && len(al.Songs) > 0 {
		al.Name = al.Songs[0].Album.Name
	}
	if al.PicURL == "" && len(al.Songs) > 0 {
		al.PicURL = al.Songs[0].Album.PicURL
	}
	al.Size = len(al.Songs)
	return al, nil
}

// ArtistDetail fetches a Migu singer's profile and top songs.
func ArtistDetail(sourceID string) (*netease.ArtistProfile, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil, errors.New("migu: empty artist id")
	}
	var infoResp struct {
		Code     string `json:"code"`
		Resource []struct {
			SingerID string    `json:"singerId"`
			Singer   string    `json:"singer"`
			Summary  string    `json:"summary"`
			Imgs     []imgItem `json:"imgs"`
			ImgItems []imgItem `json:"imgItems"`
		} `json:"resource"`
	}
	infoURL := appCBase + "/MIGUM2.0/v1.0/content/resourceinfo.do?needSimple=00&resourceType=2002&resourceId=" + url.QueryEscape(sourceID)
	err := getJSON(context.Background(), infoURL, map[string]string{"User-Agent": webUA, "Referer": webReferer}, &infoResp)

	ar := &netease.ArtistProfile{
		Artist: netease.Artist{
			ID:       virtualID("artist", sourceID),
			Source:   Source,
			SourceID: sourceID,
		},
	}
	if err == nil && len(infoResp.Resource) > 0 {
		r := infoResp.Resource[0]
		ar.Name = r.Singer
		ar.BriefDesc = r.Summary
		pic := pickImage(r.Imgs)
		if pic == "" {
			pic = pickImage(r.ImgItems)
		}
		ar.Avatar = pic
		ar.PicURL = pic
	}

	var songsResp struct {
		Code string `json:"code"`
		Data struct {
			Total     int        `json:"total"`
			SongItems []songItem `json:"songItems"`
		} `json:"data"`
	}
	q := url.Values{"singerId": {sourceID}, "pageNo": {"1"}, "pageSize": {"50"}}
	songsErr := getJSON(context.Background(), appCBase+"/MIGUM3.0/resource/singer/song/v1.0?"+q.Encode(), map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
		"channel":    defaultChn,
	}, &songsResp)
	if songsErr == nil {
		ar.MusicSize = songsResp.Data.Total
		for _, s := range songsResp.Data.SongItems {
			ar.Songs = append(ar.Songs, s.toTrack())
		}
	}
	if err != nil && songsErr != nil {
		return nil, err
	}
	if ar.Name == "" && len(ar.Songs) > 0 && len(ar.Songs[0].Artists) > 0 {
		ar.Name = ar.Songs[0].Artists[0].Name
	}
	return ar, nil
}

// ArtistAlbums returns albums for a Migu artist by searching the album catalog
// and filtering matching singer IDs (since v2.0 singer/album is deprecated).
func ArtistAlbums(sourceID, artistName string, limit, offset int) ([]netease.AlbumProfile, bool, error) {
	if strings.TrimSpace(artistName) == "" {
		if ar, err := ArtistDetail(sourceID); err == nil {
			artistName = ar.Name
		}
	}
	if strings.TrimSpace(artistName) == "" {
		return nil, false, nil
	}
	res, err := SearchPage(artistName, netease.SearchAlbums, limit, offset)
	if err != nil {
		return nil, false, err
	}
	var out []netease.AlbumProfile
	for _, al := range res.Albums {
		if sourceID == "" {
			out = append(out, al)
			continue
		}
		matched := false
		for _, ar := range al.Artists {
			if ar.SourceID == sourceID || strings.EqualFold(ar.Name, artistName) {
				matched = true
				break
			}
		}
		if matched || len(al.Artists) == 0 {
			out = append(out, al)
		}
	}
	more := offset+len(res.Albums) < res.Total && len(res.Albums) > 0
	return out, more, nil
}

var dateCleanRe = regexp.MustCompile(`\D+`)

func parseDateMS(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", "20060102", "20060102150405"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	digits := dateCleanRe.ReplaceAllString(s, "")
	if len(digits) >= 8 {
		if t, err := time.ParseInLocation("20060102", digits[:8], time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
