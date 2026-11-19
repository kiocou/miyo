package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

// Switching AudioSource to Migu routes home recommendations, charts, category filters, comments, and red counts to Migu.
func TestMiguSourceHomeDiscoveryAndComments(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	migu.SetCookie("pacmtoken=tok; uid=101")
	defer migu.SetCookie("")

	oldFeat, oldCharts, oldDaily, oldRec := miguFeaturedPlaylists, miguCharts, miguDailySongs, miguRecommendedPlaylists
	oldCat, oldCmts, oldAddCmt, oldLikeCmt := miguCategoryPlaylists, miguThreadComments, miguAddComment, miguLikeComment
	defer func() {
		miguFeaturedPlaylists, miguCharts, miguDailySongs, miguRecommendedPlaylists = oldFeat, oldCharts, oldDaily, oldRec
		miguCategoryPlaylists, miguThreadComments, miguAddComment, miguLikeComment = oldCat, oldCmts, oldAddCmt, oldLikeCmt
	}()

	homeDone := make(chan struct{}, 1)
	catCh := make(chan string, 2)
	cmtLoadCh := make(chan string, 2)
	cmtSendCh := make(chan string, 2)
	cmtLikeCh := make(chan string, 2)

	miguFeaturedPlaylists = func(int) ([]netease.Playlist, error) {
		return []netease.Playlist{{ID: migu.VirtualID("playlist", "p1"), Name: "咪咕首页推荐", Source: migu.Source, SourceID: "p1"}}, nil
	}
	miguCharts = func() ([]netease.Playlist, error) {
		return []netease.Playlist{{ID: migu.VirtualID("playlist", "rank:1"), Name: "全网热歌榜", Source: migu.Source, SourceID: "rank:1"}}, nil
	}
	miguDailySongs = func() ([]netease.Track, error) {
		return []netease.Track{{ID: migu.VirtualID("song", "cid1"), Name: "咪咕日推", Source: migu.Source, SourceID: "cid1"}}, nil
	}
	miguRecommendedPlaylists = func() ([]netease.Playlist, error) {
		homeDone <- struct{}{}
		return nil, nil
	}
	miguCategoryPlaylists = func(cat string, _ int) ([]netease.Playlist, error) {
		catCh <- cat
		return []netease.Playlist{{ID: migu.VirtualID("playlist", "p2"), Name: "电子分类歌单", Source: migu.Source, SourceID: "p2"}}, nil
	}
	miguThreadComments = func(id, rtype string, _, _ int) (*netease.Comments, error) {
		cmtLoadCh <- id + ":" + rtype
		return &netease.Comments{Total: 5, Hot: []netease.Comment{{ID: 88, Content: "咪咕热评"}}}, nil
	}
	miguAddComment = func(id, _, content string) (*netease.Comment, error) {
		cmtSendCh <- id + ":" + content
		return &netease.Comment{ID: 89, Content: content}, nil
	}
	miguLikeComment = func(id string, like bool) error {
		cmtLikeCh <- id
		return nil
	}

	a.settings.AudioSource = migu.Source
	a.loadHome()
	select {
	case <-homeDone:
	case <-time.After(time.Second):
		t.Fatal("loadHome did not invoke Migu recommendation hooks")
	}

	// Category filter UI click dispatches to miguCategoryPlaylists.
	a.featured = []netease.Playlist{{ID: migu.VirtualID("playlist", "p1"), Name: "咪咕首页推荐", Source: migu.Source, SourceID: "p1"}}
	a.charts = []netease.Playlist{{ID: migu.VirtualID("playlist", "rank:1"), Name: "全网热歌榜", Source: migu.Source, SourceID: "rank:1"}}
	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.recommendedPlaylists(c)
	}, 1180, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if err := tt.Click("歌单分类：电子"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-catCh:
		if got != "电子" {
			t.Fatalf("expected Migu category 电子, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Migu category filter did not invoke miguCategoryPlaylists")
	}

	// Comments & composer & like comment route to Migu APIs.
	tr := &netease.Track{ID: migu.VirtualID("song", "cid1"), Name: "咪咕日推", Source: migu.Source, SourceID: "cid1"}
	if !commentable(tr) || !wikiShown(tr) {
		t.Fatal("expected Migu track to be commentable and wikiShown")
	}
	cs := a.commentsFor(tr)
	if cs.thread != "migu:cid1" {
		t.Fatalf("expected migu:cid1 thread, got %q", cs.thread)
	}
	select {
	case got := <-cmtLoadCh:
		if got != "cid1:2" {
			t.Fatalf("expected cid1:2 comment load, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("commentsFor did not invoke miguThreadComments")
	}

	a.more.draft = commentDraft{track: tr.ID, text: "真不错"}
	a.sendComment(tr)
	select {
	case got := <-cmtSendCh:
		if got != "cid1:真不错" {
			t.Fatalf("expected cid1:真不错 sent, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sendComment did not invoke miguAddComment")
	}

	cs.hot = []netease.Comment{{ID: 88, Content: "咪咕热评"}}
	a.toggleCommentLike(88)
	select {
	case got := <-cmtLikeCh:
		if got != "88" {
			t.Fatalf("expected comment id 88 liked, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("toggleCommentLike did not invoke miguLikeComment")
	}
}

// Signed-in Migu users can like songs, subscribe albums/playlists, follow artists, create playlists, and run Migu FM.
func TestMiguMutationsAndFM(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.settings.AudioSource = migu.Source
	migu.SetCookie("pacmtoken=tok; uid=101")
	defer migu.SetCookie("")

	oldLike, oldSubPl, oldSubAl, oldFollow := miguLike, miguSubscribePlaylist, miguSubscribeAlbum, miguFollowArtist
	oldCreate, oldFM, oldTrash := miguCreatePlaylist, miguPersonalFM, miguFMTrash
	defer func() {
		miguLike, miguSubscribePlaylist, miguSubscribeAlbum, miguFollowArtist = oldLike, oldSubPl, oldSubAl, oldFollow
		miguCreatePlaylist, miguPersonalFM, miguFMTrash = oldCreate, oldFM, oldTrash
	}()

	likeCh := make(chan string, 1)
	subPlCh := make(chan string, 1)
	subAlCh := make(chan string, 1)
	followCh := make(chan string, 1)
	createCh := make(chan string, 1)
	fmCh := make(chan string, 1)
	trashCh := make(chan string, 1)

	miguLike = func(tr netease.Track, like bool) error {
		likeCh <- tr.SourceID
		return nil
	}
	miguSubscribePlaylist = func(sid string, sub bool) error {
		subPlCh <- sid
		return nil
	}
	miguSubscribeAlbum = func(sid string, sub bool) error {
		subAlCh <- sid
		return nil
	}
	miguFollowArtist = func(sid string, follow bool) error {
		followCh <- sid
		return nil
	}
	miguCreatePlaylist = func(name string) (*netease.Playlist, error) {
		createCh <- name
		return &netease.Playlist{ID: migu.VirtualID("playlist", "new1"), Name: name, Source: migu.Source, SourceID: "new1"}, nil
	}
	miguPersonalFM = func(mode string) ([]netease.Track, error) {
		fmCh <- mode
		return []netease.Track{{ID: migu.VirtualID("song", "fmsong1"), Name: "FM之声", Source: migu.Source, SourceID: "fmsong1", Duration: 180000}}, nil
	}
	miguFMTrash = func(tr netease.Track) error {
		trashCh <- tr.SourceID
		return nil
	}

	tr := &netease.Track{ID: migu.VirtualID("song", "cid9"), Name: "九月", Source: migu.Source, SourceID: "cid9"}
	a.toggleLike(tr)
	flushTestLike(a, tr.ID)
	select {
	case got := <-likeCh:
		if got != "cid9" {
			t.Fatalf("expected Migu like cid9, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("toggleLike did not invoke miguLike")
	}

	pl := netease.Playlist{ID: migu.VirtualID("playlist", "pl9"), Name: "收藏单", Source: migu.Source, SourceID: "pl9"}
	if !a.savable(&pl) {
		t.Fatal("expected Migu playlist to be savable")
	}
	a.toggleSaved(pl)
	select {
	case got := <-subPlCh:
		if got != "pl9" {
			t.Fatalf("expected Migu playlist pl9 saved, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("toggleSaved did not invoke miguSubscribePlaylist")
	}

	al := netease.AlbumProfile{Album: netease.Album{ID: migu.VirtualID("album", "al9"), Name: "专辑9", Source: migu.Source, SourceID: "al9"}}
	a.toggleAlbum(al)
	select {
	case got := <-subAlCh:
		if got != "al9" {
			t.Fatalf("expected Migu album al9 saved, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("toggleAlbum did not invoke miguSubscribeAlbum")
	}

	arID := migu.VirtualID("artist", "ar9")
	d := a.artist(arID)
	d.source, d.sourceID = migu.Source, "ar9"
	d.profile = &netease.ArtistProfile{Artist: netease.Artist{ID: arID, Name: "歌手9", Source: migu.Source, SourceID: "ar9"}}
	a.toggleFollow(arID)
	select {
	case got := <-followCh:
		if got != "ar9" {
			t.Fatalf("expected Migu artist ar9 followed, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("toggleFollow did not invoke miguFollowArtist")
	}

	a.naming = nameDialog{open: true, name: "新建咪咕单"}
	a.submitName()
	select {
	case got := <-createCh:
		if got != "新建咪咕单" {
			t.Fatalf("expected Migu playlist created, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("submitName did not invoke miguCreatePlaylist")
	}

	a.playFM()
	select {
	case <-fmCh:
	case <-time.After(time.Second):
		t.Fatal("playFM did not invoke miguPersonalFM")
	}

	a.pl.PlayQueue([]netease.Track{{ID: migu.VirtualID("song", "fmsong1"), Name: "FM之声", Source: migu.Source, SourceID: "fmsong1"}}, 0)
	a.trashFM()
	select {
	case got := <-trashCh:
		if got != "fmsong1" {
			t.Fatalf("expected fmsong1 trashed, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("trashFM did not invoke miguFMTrash")
	}

	// Regression: Migu like failure with ErrNotSignedIn returns an expired-session toast instead of generic failure.
	a.likeEdits = nil
	a.toggleLike(tr)
	a.likeAnswered(a.accountGen, tr.ID, a.likeEdits[tr.ID].desired, miguExpiredErrForTest())
	if !strings.Contains(a.toast, "重新登录") {
		t.Fatalf("expected expired Migu session toast, got %q", a.toast)
	}

	// Regression: libraryAnswered enriches Migu special playlist with cover/count and tracks cache.
	favID := migu.VirtualID("playlist", "237528586")
	a.libraryAnswered(a.accountGen, a.likeRevision, libraryReply{
		likedTracks: []netease.Track{{
			ID:       tr.ID,
			Name:     "某人",
			Source:   migu.Source,
			SourceID: tr.SourceID,
			Album:    netease.Album{PicURL: "https://d.musicapp.migu.cn/cov.jpg"},
		}},
		liked: []int64{tr.ID},
		made: []netease.Playlist{{
			ID:          favID,
			Name:        "我喜欢的音乐",
			SpecialType: 5,
			Source:      migu.Source,
			SourceID:    "237528586",
		}},
	})
	if len(a.mine) == 0 || a.mine[0].TrackCount != 1 || a.mine[0].CoverURL == "" {
		t.Fatalf("expected Migu liked playlist enriched with count & cover, got %+v", a.mine)
	}
	if pl := a.playlists[favID]; pl == nil || len(pl.Tracks) != 1 {
		t.Fatalf("expected Migu liked playlist tracks cached, got %+v", pl)
	}
}

func miguExpiredErrForTest() error {
	return fmt.Errorf("migu (290001): 请先登录: %w", netease.ErrNotSignedIn)
}
