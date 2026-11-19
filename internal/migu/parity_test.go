package migu

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"netease-music-mygo/internal/netease"
)

func TestMiguDiscoveryChartsAndRadioAdapters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/MIGUM3.0/bmw/index-show/recommend-playlist/v3.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"playLists":[{"resId":"111","txt":"流行热单","txt2":"热门精选","img":"//img.migu.cn/1.jpg"}]}}`))
		case "/MIGUM3.0/bmw/recommend-channel/recommend-playlist/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"contents":[{"view":"ZJ-SongList-Scroll","contents":[{"resId":"112","txt":"精选补充","img":"//img.migu.cn/112.jpg"}]}]}}`))
		case "/MIGUM3.0/bmw/rank/rank-index/v2.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"contents":[{"style":"咪咕特色榜","contents":[{"rankId":"83048887","rankName":"全网热歌榜","imageUrl":"https://d.musicapp.migu.cn/r.jpg"}]}]}}`))
		case "/MIGUM3.0/bmw/rank/rank-info/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"title":"全网热歌榜","desc":"榜单简介","updateTime":"2026-11-19","titlePic":"https://d.musicapp.migu.cn/tp.jpg","totalCount":50,"contents":[{"resId":"cid1","copyrightId":"cr1","songId":"s1","txt":"晴天","txt2":"周杰伦","txt3":"叶惠美","img":"https://d.musicapp.migu.cn/s1.jpg","songData":"{\"contentId\":\"cid1\",\"copyrightId\":\"cr1\",\"songId\":\"s1\",\"songName\":\"晴天\",\"singers\":[{\"id\":\"ar1\",\"name\":\"周杰伦\"}],\"album\":\"叶惠美\",\"albumId\":\"al1\",\"duration\":269,\"audioFormats\":[{\"formatType\":\"ZQ24\"}]}"}]}}`))
		case "/MIGUM3.0/v1.0/template/musiclistplaza-taglist":
			_, _ = w.Write([]byte(`{"code":"000000","data":[{"header":{"title":"风格"},"content":[{"texts":["电子","1000001682","%E7%94%B5%E5%AD%90"]}]}]}`))
		case "/MIGUM3.0/v1.0/template/musiclistplaza-listbytag":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"contentItemList":{"itemList":[{"actionUrl":"mgmusic://song-list-info?id=222","title":"电子脉冲","subTitle":"DJ","imageUrl":"https://d.musicapp.migu.cn/2.jpg","playNum":"12.5万"}]}}}`))
		case "/MIGUM3.0/bmw/music-radio/category/list/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"grp":[{"resId":"20439303","txt":"流行","img":"https://d.musicapp.migu.cn/fm.jpg"}]}}`))
		case "/MIGUM3.0/resource/sceneradio/song-list/v2.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":[{"contentId":"cidfm","copyrightId":"crfm","songId":"sfm","songName":"山外云","duration":200,"singerList":[{"id":"arf","name":"歌手F"}],"album":"专辑F","albumId":"alf","audioFormats":[{"formatType":"SQ"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	oldC := appCBase
	appCBase = srv.URL
	defer func() { appCBase = oldC }()

	featured, err := FeaturedPlaylists(10)
	if err != nil || len(featured) != 2 || featured[0].SourceID != "111" || featured[1].SourceID != "112" {
		t.Fatalf("FeaturedPlaylists got %+v err=%v", featured, err)
	}

	charts, err := Charts()
	if err != nil || len(charts) != 1 || charts[0].SourceID != "rank:83048887" || len(charts[0].Preview) != 1 || charts[0].Preview[0].First != "晴天" {
		t.Fatalf("Charts got %+v err=%v", charts, err)
	}

	detail, err := PlaylistDetail("rank:83048887")
	if err != nil || len(detail.Tracks) != 1 || detail.Tracks[0].Source != Source || len(detail.Tracks[0].Qualities) == 0 {
		t.Fatalf("ChartDetail via PlaylistDetail got %+v err=%v", detail, err)
	}

	plaza, err := CategoryPlaylists("电子", 10)
	if err != nil || len(plaza) != 1 || plaza[0].SourceID != "222" || plaza[0].PlayCount != 125000 {
		t.Fatalf("CategoryPlaylists got %+v err=%v", plaza, err)
	}

	SetCookie("")
	fm, err := PersonalFM(netease.FMDefault)
	if err != nil || len(fm) != 1 || fm[0].Name != "山外云" || fm[0].Artists[0].Name != "歌手F" {
		t.Fatalf("PersonalFM sceneradio fallback got %+v err=%v", fm, err)
	}
}

func TestMiguSocialAndLibraryAdapters(t *testing.T) {
	var seenPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/MIGUM3.0/user/comment/stack/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"commentNums":"42","hotComments":[{"commentId":"901","commentInfo":"经典","commentTime":"2026-11-19 10:00:00","haveThumb":"1","user":{"userId":"55","nickName":"乐迷","smallIcon":"https://d.musicapp.migu.cn/u.jpg"},"opNumItem":{"thumbNum":12}}],"comments":[{"commentId":"902","commentInfo":"好听","commentTime":"1795070000000","haveThumb":"0","user":{"userId":"56","nickName":"新友"},"opNumItem":{"thumbNum":1}}]}}`))
		case "/MIGUM2.0/v1.0/content/queryOPNumItemsAction.do":
			_, _ = w.Write([]byte(`{"code":"000000","userOpNums":[{"opNumItem":{"keepNum":9342,"keepNumDesc":"9342"}}]}`))
		case "/MIGUM3.0/user/api/comment/add/v2.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"commentId":"903","commentInfo":"打卡","user":{"userId":"77","nickName":"我"}}}`))
		case "/MIGUM2.0/v1.0/user/delComment.do":
			_, _ = w.Write([]byte(`{"code":"000000"}`))
		case "/MIGUM2.0/v2.0/user/getMyPage.do", "/MIGUM3.0/user/api/my-page-music/v2.0":
			if r.URL.Path == "/MIGUM3.0/user/api/my-page-music/v2.0" {
				http.Error(w, "use legacy fallback", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"code":"000000","data":{"favoriteMusicList":{"id":"fav1","title":"我喜欢的音乐","musicNum":2},"createdMusicLists":[{"id":"pl1","title":"通勤歌单","musicNum":5}],"collectMusicLists":[{"id":"pl2","title":"朋友的歌单","musicNum":10}]}}`))
		case "/MIGUM2.0/v1.0/user/collections.do", "/pc/v1.0/user/collections.do":
			rt := r.URL.Query().Get("resourceType")
			if strings.HasPrefix(rt, "2003") {
				_, _ = w.Write([]byte(`{"code":"000000","collections":[{"albumId":"al9","title":"叶惠美","singer":"周杰伦"}]}`))
				return
			}
			if rt == "2021|2020" {
				_, _ = w.Write([]byte(`{"code":"000000","collections":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"000000","collections":[{"contentId":"cid1","songId":"s1","songName":"晴天"}]}`))
		case "/pc/open/api/music-list/add/v2.0", "/MIGUM2.0/v1.0/user/createMusicList.do":
			_, _ = w.Write([]byte(`{"code":"000000","musicListId":"newpl1"}`))
		case "/pc/user/h5-import-musiclist/v1.0", "/pc/v1.0/user/deleteMusicList.do", "/MIGUM3.0/musiclist/manager":
			_, _ = w.Write([]byte(`{"code":"000000"}`))
		case "/pc/user/api/add-music-list-song/v1.0", "/user/api/add-music-list-song/v1.0":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["id"] != nil && body["id"] != "newpl1" {
				t.Fatalf("expected playlist id newpl1, got %v", body["id"])
			}
			_, _ = w.Write([]byte(`{"code":"000000","data":{"successNum":"1"}}`))
		case "/pc/v1.0/user/add_collection.do", "/pc/v1.0/user/del_collection.do",
			"/MIGUM2.0/v1.0/user/add_collection.do", "/MIGUM2.0/v1.0/user/del_collection.do",
			"/MIGUM2.0/v1.0/user/follow.do", "/MIGUM2.0/v1.0/user/unfollow.do":
			_, _ = w.Write([]byte(`{"code":"000000"}`))
		case "/MIGUM3.0/bmw/singer-index/follow/v1.0":
			http.Error(w, "use legacy", http.StatusNotFound)
		case "/MIGUM2.0/v1.0/user/followingSingers.do":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"list":[{"singerId":"ar1","singerName":"周杰伦"}]}}`))
		case "/MIGUM3.0/user/i/record/query/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"list":[{"contentId":"cid1","songName":"晴天","createTime":"1795070000000"}]}}`))
		case "/MIGUM3.0/user/i/record/report/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000"}`))
		case "/MIGUM3.0/bmw/listen-song/ranking/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"list":[{"playCount":8,"score":100,"song":{"contentId":"cid1","songName":"晴天"}}]}}`))
		case "/MIGUM3.0/user/cloud/music/list/v2.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"list":[{"contentId":"cidc1","songName":"云端曲"}]}}`))
		case "/MIGUM3.0/user/cloud/music/capacity/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"useCapacity":"1048576","totalCapacity":"1073741824"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	oldC, oldU := appCBase, appUBase
	appCBase, appUBase = srv.URL, srv.URL
	defer func() { appCBase, appUBase = oldC, oldU }()

	SetCookie("pacmtoken=tok123; uid=u999")
	defer SetCookie("")

	cmts, err := ThreadComments("cid1", "2", 0, 30)
	if err != nil || cmts.Total != 42 || len(cmts.Hot) != 1 || cmts.Hot[0].ID != 901 || !cmts.Hot[0].Liked || len(cmts.Latest) != 1 {
		t.Fatalf("ThreadComments got %+v err=%v", cmts, err)
	}
	if rc, err := SongRedCount("cid1"); err != nil || rc.Count != 9342 {
		t.Fatalf("SongRedCount got %+v err=%v", rc, err)
	}
	if added, err := AddComment("cid1", "2", "打卡"); err != nil || added.Content != "打卡" {
		t.Fatalf("AddComment got %+v err=%v", added, err)
	}
	if err := DeleteComment("903"); err != nil {
		t.Fatalf("DeleteComment err=%v", err)
	}
	if err := LikeComment("901", true); err != nil {
		t.Fatalf("LikeComment err=%v", err)
	}

	made, saved, err := UserPlaylists()
	if err != nil || len(made) != 2 || made[0].SpecialType != 5 || len(saved) != 1 {
		t.Fatalf("UserPlaylists made=%+v saved=%+v err=%v", made, saved, err)
	}
	liked, err := LikedTracks()
	if err != nil || len(liked) != 1 || liked[0].Name != "晴天" {
		t.Fatalf("LikedTracks got %+v err=%v", liked, err)
	}
	if err := Like(liked[0], true); err != nil {
		t.Fatalf("Like err=%v", err)
	}
	if pl, err := CreatePlaylist("新歌单"); err != nil || pl.SourceID != "newpl1" {
		t.Fatalf("CreatePlaylist got %+v err=%v", pl, err)
	}
	if err := RenamePlaylist("newpl1", "改名"); err != nil {
		t.Fatalf("RenamePlaylist err=%v", err)
	}
	if err := AddToPlaylist("newpl1", "cid1"); err != nil {
		t.Fatalf("AddToPlaylist err=%v", err)
	}
	if err := RemoveFromPlaylist("newpl1", "cid1"); err != nil {
		t.Fatalf("RemoveFromPlaylist err=%v", err)
	}
	if err := DeletePlaylist("newpl1"); err != nil {
		t.Fatalf("DeletePlaylist err=%v", err)
	}
	if err := SubscribePlaylist("pl2", true); err != nil {
		t.Fatalf("SubscribePlaylist err=%v", err)
	}
	if err := FollowArtist("ar1", false); err != nil {
		t.Fatalf("FollowArtist err=%v", err)
	}
	if ars, err := FollowedArtists(); err != nil || len(ars) != 1 || ars[0].SourceID != "ar1" {
		t.Fatalf("FollowedArtists got %+v err=%v", ars, err)
	}
	if als, err := SubscribedAlbums(); err != nil || len(als) != 1 || als[0].SourceID != "al9" {
		t.Fatalf("SubscribedAlbums got %+v err=%v", als, err)
	}
	if err := SubscribeAlbum("al9", true); err != nil {
		t.Fatalf("SubscribeAlbum err=%v", err)
	}
	if plays, err := RecentPlays(10); err != nil || len(plays) != 1 {
		t.Fatalf("RecentPlays got %+v err=%v", plays, err)
	}
	if err := Scrobble(liked[0], 45); err != nil {
		t.Fatalf("Scrobble err=%v", err)
	}
	if wk, err := WeekRanking(); err != nil || len(wk) != 1 || wk[0].Plays != 8 {
		t.Fatalf("WeekRanking got %+v err=%v", wk, err)
	}
	if drv, err := CloudSongs(); err != nil || len(drv.Songs) != 1 || drv.Used != 1048576 {
		t.Fatalf("CloudSongs got %+v err=%v", drv, err)
	}
	if !strings.Contains(strings.Join(seenPaths, "\n"), "POST /pc/user/api/add-music-list-song/v1.0") {
		t.Fatalf("missing expected POST endpoint in %v", seenPaths)
	}
}

func TestMiguV3MyPageSyncAndPCWebEndpoints(t *testing.T) {
	var sawPacmHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("pacmtoken") == "tok999" {
			sawPacmHeader = true
		}
		switch r.URL.Path {
		case "/MIGUM3.0/user/api/my-page-music/v2.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{
				"myLikeMusicInfo":{"totalCount":"2","playListId":"237528586","songItemList":[{"contentId":"c1","songName":"Track1","img1":"/data/oss/c1.jpg"}]},
				"myCreatedMusicLists":{"totalCount":1,"myCreatedMusicLists":[{"musicListId":"c101","title":"自建单","musicNum":"3"}]},
				"myCollectedMusicInfo":{"totalCount":"1","myCollectedMusicLists":[{"musicListId":"s202","title":"收藏单","musicNum":"9"}]},
				"recentPlayInfo":{"totalCount":"1","recentPlayList":[{"contentId":"rec1","songName":"最近听","duration":180}]}
			}}`))
		case "/MIGUM2.0/v1.0/content/resourceinfo.do":
			_, _ = w.Write([]byte(`{"code":"000000","resource":[{"title":"我喜欢的音乐","musicListId":"237528586","musicNum":2}]}`))
		case "/MIGUM2.0/v1.0/user/queryMusicListSongs.do":
			if r.URL.Query().Get("musicListId") != "237528586" {
				t.Fatalf("unexpected musicListId %q", r.URL.Query().Get("musicListId"))
			}
			// Simulate Migu server returning fewer items than requested pageSize while totalCount=2.
			if r.URL.Query().Get("pageNo") == "2" {
				_, _ = w.Write([]byte(`{"code":"000000","totalCount":2,"list":[{"contentId":"c2","songId":"s2","songName":"某人","singer":"汪苏泷","duration":246}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"000000","totalCount":2,"list":[{"contentId":"c1","songId":"s1","songName":"Rather Be","singerId":"101|102","singer":"Clean Bandit|Jess Glynne","duration":275}]}`))
		case "/pc/user/api/add-music-list-song/v1.0":
			_, _ = w.Write([]byte(`{"code":"290001","info":"请先登录"}`))
		case "/MIGUM3.0/bmw/singer-index/follow/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"contents":[{"singerId":"ar88","singerName":"汪苏泷","img":"/ar88.jpg"}]}}`))
		case "/MIGUM3.0/bmw/listen-song/ranking/v1.0":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"songRankInfos":[{"playCount":15,"song":{"contentId":"c2","songName":"某人"}}]}}`))
		case "/MIGUM3.0/user/i/record/query/v1.0":
			_, _ = w.Write([]byte(`{"code":"290001","info":"请先登录"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	oldC, oldU := appCBase, appUBase
	appCBase, appUBase = srv.URL, srv.URL
	defer func() { appCBase, appUBase = oldC, oldU }()

	SetCookie("pacmtoken=tok999; uid=92234441578")
	defer SetCookie("")

	made, saved, err := UserPlaylists()
	if err != nil {
		t.Fatalf("UserPlaylists err=%v", err)
	}
	if !sawPacmHeader {
		t.Fatal("expected pacmtoken header on outgoing requests")
	}
	if len(made) != 2 || made[0].SourceID != "237528586" || made[0].SpecialType != 5 || made[0].TrackCount != 2 || made[1].SourceID != "c101" {
		t.Fatalf("unexpected made playlists: %+v", made)
	}
	if len(saved) != 1 || saved[0].SourceID != "s202" {
		t.Fatalf("unexpected saved playlists: %+v", saved)
	}

	liked, err := LikedTracks()
	if err != nil || len(liked) != 2 {
		t.Fatalf("LikedTracks got %d err=%v", len(liked), err)
	}
	if len(liked[0].Artists) != 2 || liked[0].Artists[0].SourceID != "101" || liked[0].Artists[1].Name != "Jess Glynne" {
		t.Fatalf("expected pipe-separated artists parsed cleanly, got %+v", liked[0].Artists)
	}

	if err := Like(liked[0], true); !errors.Is(err, netease.ErrNotSignedIn) {
		t.Fatalf("expected ErrNotSignedIn on 290001 even with cookie, got %v", err)
	}
	if ars, err := FollowedArtists(); err != nil || len(ars) != 1 || ars[0].SourceID != "ar88" {
		t.Fatalf("FollowedArtists got %+v err=%v", ars, err)
	}
	if wk, err := WeekRanking(); err != nil || len(wk) != 1 || wk[0].Plays != 15 {
		t.Fatalf("WeekRanking got %+v err=%v", wk, err)
	}
	if plays, err := RecentPlays(10); err != nil || len(plays) != 1 || plays[0].Track.Name != "最近听" {
		t.Fatalf("RecentPlays fallback got %+v err=%v", plays, err)
	}
}
