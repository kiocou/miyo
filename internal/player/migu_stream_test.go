package player

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

// Stream headers on external tracks are sent to the CDN when fetching audio.
func TestExternalStreamHeadersAndCacheVetting(t *testing.T) {
	type reqHeaders struct {
		referer string
		ua      string
	}
	ch := make(chan reqHeaders, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case ch <- reqHeaders{referer: r.Header.Get("Referer"), ua: r.Header.Get("User-Agent")}:
		default:
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"))
	}))
	defer srv.Close()

	c := NewCache(t.TempDir(), 64<<20)
	dl := c.download(-9, &netease.Stream{
		URL:     srv.URL + "/song.mp3",
		Source:  migu.Source,
		Headers: map[string]string{"Referer": "https://music.migu.cn/", "User-Agent": "migu-agent"},
		Level:   "exhigh",
		Type:    "mp3",
		Size:    10,
	})
	defer dl.Close()

	h := <-ch
	if h.referer != "https://music.migu.cn/" || h.ua != "migu-agent" {
		t.Fatalf("unexpected headers sent to CDN: Referer=%q UA=%q", h.referer, h.ua)
	}
}

func TestLoudKeyExternalTrack(t *testing.T) {
	tr := netease.Track{ID: -101, Source: migu.Source, SourceID: "600902000006889366"}
	if got := loudKey(tr, ""); got != "xmigu:600902000006889366" {
		t.Fatalf("loudKey = %q", got)
	}
}

// Reload keeps the active playback position, preserves playing state, and bypasses cache to apply a new quality.
func TestPlayerReloadKeepsPositionAndBypassesCache(t *testing.T) {
	p := New(func(fn func()) { fn() })
	p.Cue([]netease.Track{{ID: 10, Name: "晴天", Duration: 240_000}})
	p.Hold(42 * time.Second)
	p.playing = true

	lv := "lossless"
	p.Quality = func() string { return lv }
	asked := make(chan string, 2)
	p.ResolveStream = func(_ context.Context, _ netease.Track, level string) (*netease.Stream, error) {
		asked <- level
		return nil, context.Canceled
	}

	if !p.Reload() {
		t.Fatal("expected Reload() to trigger for active streamable track")
	}
	if p.pending != 42*time.Second || !p.playing || p.reloadSkipCache != p.gen {
		t.Fatalf("unexpected reload state: pending=%v playing=%v skipGen=%d gen=%d", p.pending, p.playing, p.reloadSkipCache, p.gen)
	}
	if got := <-asked; got != "lossless" {
		t.Fatalf("expected updated quality level lossless, got %q", got)
	}
}
