package netease

import (
	"slices"
	"testing"
)

func TestPlayableFollowsPrivilege(t *testing.T) {
	tracks := []Track{{ID: 1, Fee: 1}, {ID: 2, Fee: 8}, {ID: 3, Fee: 8}, {ID: 4, Fee: 1}}
	if tracks[0].Playable() || !tracks[1].Playable() {
		t.Fatal("without privileges, VIP songs do not play and free ones do")
	}
	withPrivileges(tracks, []Privilege{
		{ID: 1, Pl: 320000},           // VIP song, the listener subscribes
		{ID: 2, Pl: 0},                // nothing to play
		{ID: 3, St: -200, Pl: 128000}, // taken down
	})
	want := []bool{true, false, false, false}
	for i, tr := range tracks {
		if tr.Playable() != want[i] {
			t.Errorf("track %d: playable %v", tr.ID, tr.Playable())
		}
	}
}

func TestExternalTrackAndSized(t *testing.T) {
	migu := Track{
		ID:          -42,
		Name:        "晴天",
		Source:      "migu",
		SourceID:    "60054701923",
		CopyrightID: "60054701923",
	}
	if migu.IsNetEase() {
		t.Fatal("migu track should not report IsNetEase")
	}
	if !migu.Playable() {
		t.Fatal("migu track should default to Playable")
	}
	if got, want := migu.URL(), "https://music.migu.cn/v3/music/song/60054701923"; got != want {
		t.Fatalf("migu URL = %q, want %q", got, want)
	}
	if got := migu.Thread(); got != "" {
		t.Fatalf("migu Thread = %q, want empty", got)
	}
	if got := Sized("http://d.musicapp.migu.cn/prod/cover.jpg", 80); got != "https://d.musicapp.migu.cn/prod/cover.jpg" {
		t.Fatalf("Sized external = %q", got)
	}
	if got := Sized("http://p1.music.126.net/a.jpg", 80); got != "https://p1.music.126.net/a.jpg?param=80y80" {
		t.Fatalf("Sized netease = %q", got)
	}
}

func TestTrackAvailableLevels(t *testing.T) {
	if got := (&Track{ID: 1}).AvailableLevels(); got != nil {
		t.Fatalf("expected nil levels before Privilege loads, got %v", got)
	}
	guestExhigh := &Track{ID: 2, Privilege: &Privilege{Pl: 320000, PlLevel: "exhigh", PlayMaxBrLevel: "hires"}}
	if got := guestExhigh.AvailableLevels(); !slices.Equal(got, []string{"standard", "higher", "exhigh"}) {
		t.Fatalf("guest exhigh track levels = %v", got)
	}
	vipTrackCapped := &Track{ID: 3, Privilege: &Privilege{Pl: 1999000, PlLevel: "hires", PlayMaxBrLevel: "lossless"}}
	if got := vipTrackCapped.AvailableLevels(); !slices.Equal(got, []string{"standard", "higher", "exhigh", "lossless"}) {
		t.Fatalf("VIP on lossless-max track levels = %v", got)
	}
}
