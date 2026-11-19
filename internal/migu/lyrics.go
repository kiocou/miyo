package migu

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"netease-music-mygo/internal/lyrics"
)

const (
	mrcTEAKey   = "karakal@123Qcomyidongtiantianhaoting"
	mrcTEADelta = int64(2654435769)
)

// DecryptMRC decrypts Migu's TEA+UTF-16 encrypted .mrc lyrics hex string,
// matching MrcEnDeUtil.Decrypt from the Android APK. Non-hex or plaintext
// lyrics already containing '[' line tags are returned as-is.
func DecryptMRC(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) < 32 || strings.Contains(s, "\n[") || strings.HasPrefix(s, "[") {
		return raw
	}
	if len(s)%16 != 0 {
		return raw
	}
	v := make([]int64, len(s)/16)
	for i := range v {
		u, err := strconv.ParseUint(s[i*16:(i+1)*16], 16, 64)
		if err != nil {
			return raw
		}
		v[i] = int64(u)
	}
	k := mrcKeyToLongArray(mrcTEAKey)
	teaDecrypt64(v, k)

	out := make([]byte, len(v)*8)
	for i, w := range v {
		binary.LittleEndian.PutUint64(out[i*8:], uint64(w))
	}
	for len(out) > 0 && out[len(out)-1] == '0' {
		out = out[:len(out)-1]
	}
	for len(out) > 1 && out[len(out)-1] == 0 && out[len(out)-2] == 0 {
		out = out[:len(out)-2]
	}
	if len(out)%2 == 1 {
		out = out[:len(out)-1]
	}
	return decodeUTF16(out)
}

func mrcKeyToLongArray(key string) []int64 {
	for len(key) < 32 {
		key += "0"
	}
	kb := []byte(key)
	utf16Bytes := make([]byte, len(kb)*2)
	for i, b := range kb {
		utf16Bytes[i*2] = b
	}
	n := (len(utf16Bytes) + 7) / 8
	out := make([]int64, n)
	for i := 0; i < n-1; i++ {
		out[i] = int64(binary.LittleEndian.Uint64(utf16Bytes[i*8:]))
	}
	var tail [8]byte
	copy(tail[:], utf16Bytes[(n-1)*8:])
	out[n-1] = int64(binary.LittleEndian.Uint64(tail[:]))
	return out
}

func teaDecrypt64(v, k []int64) {
	n := len(v)
	if n < 1 || len(k) == 0 {
		return
	}
	y := v[0]
	sum := int64(52/n+6) * mrcTEADelta
	for sum != 0 {
		e := (sum >> 2) & 3
		for p := n - 1; p > 0; p-- {
			z := v[p-1]
			idx := (int64(p) & 3) ^ e
			y = v[p] - ((((z >> 5) ^ (y << 2)) + ((y >> 3) ^ (z << 4))) ^ ((y ^ sum) + (k[idx%int64(len(k))] ^ z)))
			v[p] = y
		}
		z := v[n-1]
		y = v[0] - ((((z >> 5) ^ (y << 2)) + ((y >> 3) ^ (z << 4))) ^ ((sum ^ y) + (z ^ k[e%int64(len(k))])))
		v[0] = y
		sum -= mrcTEADelta
	}
}

func decodeUTF16(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	be := false
	if b[0] == 0xfe && b[1] == 0xff {
		be = true
		b = b[2:]
	} else if b[0] == 0xff && b[1] == 0xfe {
		b = b[2:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if be {
			u[i] = binary.BigEndian.Uint16(b[i*2:])
		} else {
			u[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
	}
	return string(utf16.Decode(u))
}

var (
	mrcLineRe = regexp.MustCompile(`^\[(\d+),(\d+)\](.*)$`)
	mrcWordRe = regexp.MustCompile(`\(\s*(\d+)\s*,\s*(\d+)\s*\)`)
)

// ParseMRC parses decrypted Migu word-by-word lyrics (`[startMs,durMs]w(s,d)...`)
// into timed lyric lines compatible with internal/lyrics.
func ParseMRC(content string) []lyrics.Line {
	var out []lyrics.Line
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		raw = strings.TrimSpace(raw)
		m := mrcLineRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		startMS, _ := strconv.ParseInt(m[1], 10, 64)
		durMS, _ := strconv.ParseInt(m[2], 10, 64)
		body := m[3]
		if durMS <= 0 {
			durMS = 4000
		}

		matches := mrcWordRe.FindAllStringSubmatchIndex(body, -1)
		if len(matches) == 0 {
			text := strings.TrimSpace(body)
			if text == "" {
				continue
			}
			at := time.Duration(startMS) * time.Millisecond
			end := at + time.Duration(durMS)*time.Millisecond
			out = append(out, lyrics.Line{
				At:    at,
				End:   end,
				Text:  text,
				Words: lyrics.SpreadWords(text, at, end),
			})
			continue
		}

		var words []lyrics.Word
		var full strings.Builder
		prev := 0
		timed := false
		for _, idx := range matches {
			wText := body[prev:idx[0]]
			prev = idx[1]
			ws, _ := strconv.ParseInt(body[idx[2]:idx[3]], 10, 64)
			wd, _ := strconv.ParseInt(body[idx[4]:idx[5]], 10, 64)
			if wText == "" {
				continue
			}
			timed = true
			full.WriteString(wText)
			wStart := time.Duration(ws) * time.Millisecond
			// Some MRC encoders store absolute ms; relative offsets smaller than startMS
			// are shifted onto line start.
			if ws < startMS && ws+durMS <= startMS+durMS && len(words) == 0 && startMS > 1000 {
				wStart = time.Duration(startMS+ws) * time.Millisecond
			}
			wEnd := wStart + time.Duration(wd)*time.Millisecond
			words = append(words, lyrics.Word{Text: wText, Start: wStart, End: wEnd})
		}
		if prev < len(body) {
			tail := body[prev:]
			if tail != "" {
				full.WriteString(tail)
				end := time.Duration(startMS+durMS) * time.Millisecond
				if len(words) > 0 {
					words = append(words, lyrics.Word{Text: tail, Start: words[len(words)-1].End, End: end})
				}
			}
		}

		text := strings.TrimSpace(full.String())
		if text == "" {
			continue
		}
		at := time.Duration(startMS) * time.Millisecond
		end := at + time.Duration(durMS)*time.Millisecond
		if len(words) > 0 && words[len(words)-1].End > end {
			end = words[len(words)-1].End
		}
		if !timed {
			words = lyrics.SpreadWords(text, at, end)
		}
		out = append(out, lyrics.Line{
			At:    at,
			End:   end,
			Text:  text,
			Words: words,
			Timed: timed,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// Lyrics fetches timed word-by-word (MRC) or LRC lyrics and translation for a Migu track,
// returning parsed lines and plain LRC text when available.
func Lyrics(ctx context.Context, contentID, lrcURL, trcURL, mrcURL string) ([]lyrics.Line, string, error) {
	if mrcURL == "" && contentID != "" {
		if l, t, m, err := fetchSongLyricURLs(ctx, contentID); err == nil {
			lrcURL = firstNonEmpty(lrcURL, l)
			trcURL = firstNonEmpty(trcURL, t)
			mrcURL = firstNonEmpty(mrcURL, m)
		}
	}

	var lrcText string
	if lrcURL != "" {
		if b, err := fetchTextURL(ctx, lrcURL); err == nil {
			lrcText = b
		}
	}
	var trcText string
	if trcURL != "" {
		if b, err := fetchTextURL(ctx, trcURL); err == nil {
			trcText = b
		}
	}

	if mrcURL != "" {
		if enc, err := fetchTextURL(ctx, mrcURL); err == nil {
			dec := DecryptMRC(enc)
			if lines := ParseMRC(dec); len(lines) > 0 {
				if trcText != "" {
					tl := lyrics.Build(trcText, "", "")
					for i := range tl {
						tl[i].Translation = tl[i].Text
					}
					lines = lyrics.KeepTranslations(lines, tl)
				}
				return lines, lrcText, nil
			}
		}
	}

	if lrcText != "" {
		return lyrics.Build(lrcText, trcText, ""), lrcText, nil
	}
	return nil, "", errors.New("migu: no lyrics available")
}

func fetchSongLyricURLs(ctx context.Context, contentID string) (lrc, trc, mrc string, err error) {
	var resp struct {
		Code     string `json:"code"`
		Resource []struct {
			LrcURL   string `json:"lrcUrl"`
			LyricURL string `json:"lyricUrl"`
			TrcURL   string `json:"trcUrl"`
			MrcURL   string `json:"mrcUrl"`
		} `json:"resource"`
	}
	u := appUBase + "/MIGUM2.0/v1.0/content/resourceinfo.do?resourceType=2&resourceId=" + url.QueryEscape(contentID)
	if err = getJSON(ctx, u, map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
	}, &resp); err != nil {
		return "", "", "", err
	}
	if len(resp.Resource) == 0 {
		return "", "", "", errors.New("migu: empty resourceinfo")
	}
	r := resp.Resource[0]
	return httpsURL(firstNonEmpty(r.LrcURL, r.LyricURL)), httpsURL(r.TrcURL), httpsURL(r.MrcURL), nil
}

func fetchTextURL(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", httpsURL(rawURL), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", webReferer)
	body, _, _, err := doRequest(ctx, req)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
