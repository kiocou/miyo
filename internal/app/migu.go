package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

// miguState holds transient Migu UI/routing state on the main thread.
type miguState struct {
	cookieDraft     string
	probing         bool
	probeNote       string
	playlistSource  map[int64]string
	tracksByID      map[int64]netease.Track
	artistSource    map[int64]string
	albumSource     map[int64]string
	matchedTracks   map[int64]netease.Track
	trackFormats    map[int64][]string
	trackFmtAsked   map[int64]bool
	trackVerified   map[int64][]string
	trackProbeAsked map[int64]bool
	trackPriv       map[int64]*netease.Privilege
	trackPrivAsked  map[int64]bool
	qualityReq      *qualitySwitchReq
	login           miguLoginState
}

// Package hooks so unit tests can replace network stream resolution cleanly.
var (
	miguSongURL        = migu.SongURLContext
	miguMatch          = migu.Match
	miguProbe          = migu.ProbeQuality
	miguProbeTrack     = migu.ProbeTrackQualitiesContext
	miguLyrics         = migu.Lyrics
	miguTrackFormats   = migu.TrackFormatsContext
	neteaseStream      = netease.SongURLContext
	neteaseSongDetails = netease.SongDetails

	miguFeaturedPlaylists    = migu.FeaturedPlaylists
	miguRecommendedPlaylists = migu.RecommendedPlaylists
	miguCharts               = migu.Charts
	miguCategoryPlaylists    = migu.CategoryPlaylists
	miguHighQualityPlaylists = migu.HighQualityPlaylists
	miguDailySongs           = migu.DailySongs
	miguDailyDislike         = migu.DailyDislike
	miguStyleCategories      = migu.StyleCategories
	miguDailyStyleSongs      = migu.DailyStyleSongs
	miguPersonalFM           = migu.PersonalFM
	miguFMTrash              = migu.FMTrash
	miguUserPlaylists        = migu.UserPlaylists
	miguLikedTracks          = migu.LikedTracks
	miguLike                 = migu.Like
	miguSubscribePlaylist    = migu.SubscribePlaylist
	miguSubscribeAlbum       = migu.SubscribeAlbum
	miguSubscribedAlbums     = migu.SubscribedAlbums
	miguFollowArtist         = migu.FollowArtist
	miguFollowedArtists      = migu.FollowedArtists
	miguCreatePlaylist       = migu.CreatePlaylist
	miguRenamePlaylist       = migu.RenamePlaylist
	miguDeletePlaylist       = migu.DeletePlaylist
	miguAddToPlaylist        = migu.AddToPlaylist
	miguRemoveFromPlaylist   = migu.RemoveFromPlaylist
	miguRecentPlays          = migu.RecentPlays
	miguScrobble             = migu.Scrobble
	miguWeekRanking          = migu.WeekRanking
	miguCloudSongs           = migu.CloudSongs
	miguThreadComments       = migu.ThreadComments
	miguLikeComment          = migu.LikeComment
	miguAddComment           = migu.AddComment
	miguDeleteComment        = migu.DeleteComment
	miguSongRedCount         = migu.SongRedCount
	miguSongWikiOf           = migu.SongWikiOf
	miguArtistIntro          = migu.ArtistIntro
)

func (a *app) applyMiguSettings() {
	migu.SetCookie(a.settings.MiguCookie)
}

func (a *app) activeSource() string {
	if a.settings.AudioSource == migu.Source {
		return migu.Source
	}
	return ""
}

func (a *app) isMiguSource() bool {
	return a.activeSource() == migu.Source
}

func miguOpFailMessage(err error, fallback string) string {
	if err != nil && errors.Is(err, netease.ErrNotSignedIn) && strings.Contains(err.Error(), "migu") {
		return "咪咕登录凭据已过期，请到设置重新登录"
	}
	return fallback
}

const miguUserVirtualID int64 = -9990001

func (a *app) sourceSignedIn() bool {
	if a.isMiguSource() {
		return migu.HasCookie()
	}
	return a.profile != nil
}

func (a *app) signedInForTrack(tr *netease.Track) bool {
	if tr != nil && tr.Source == migu.Source {
		return migu.HasCookie()
	}
	if tr != nil {
		return a.profile != nil
	}
	return a.sourceSignedIn()
}

func (a *app) activeUserID() int64 {
	if a.isMiguSource() {
		if migu.HasCookie() {
			return miguUserVirtualID
		}
		return 0
	}
	if a.profile != nil {
		return a.profile.UserID
	}
	return 0
}

func (a *app) setAudioSource(src string) {
	if src != migu.Source {
		src = ""
	}
	if a.activeSource() == src {
		return
	}
	a.settings.AudioSource = src
	a.searched.source = src
	if a.route.page == pageSearch {
		r := a.route
		r.source = src
		if src == migu.Source && (r.kind == netease.SearchUsers || r.kind == netease.SearchPodcasts) {
			r.kind = netease.SearchSongs
		}
		a.show(r)
	}
	if src == migu.Source {
		a.say("source", "已切换当前音源：咪咕音乐")
	} else {
		a.say("source", "已切换当前音源：网易云音乐")
	}
	a.reloadActiveSource()
	a.changed()
}

func (a *app) reloadActiveSource() {
	a.accountGen++
	a.mine, a.saved, a.recPlaylists, a.liked = nil, nil, nil, nil
	a.featured, a.charts, a.daily = nil, nil, nil
	a.homeAt = time.Time{}
	a.myAlbums, a.myAlbumsErr, a.myAlbumsBusy = nil, "", false
	a.cloud = cloudState{}
	a.week = weekState{}
	a.style = styleState{}
	a.radar = radarState{open: a.radar.open}
	a.librarySync = librarySync{}
	a.filteredPlaylists = [len(playlistFilters)]*playlistBrowseData{}
	a.loadHome()
	if a.sourceSignedIn() {
		a.refreshLibrary(time.Now())
		a.loadMyAlbums()
	}
	a.load(a.route)
}

func (a *app) rememberMiguPlaylist(pl netease.Playlist) {
	if pl.Source != migu.Source || pl.SourceID == "" || pl.ID == 0 {
		return
	}
	if a.migu.playlistSource == nil {
		a.migu.playlistSource = map[int64]string{}
	}
	a.migu.playlistSource[pl.ID] = pl.SourceID
	if len(pl.Tracks) > 0 {
		a.rememberMiguTracks(pl.Tracks)
	}
}

func (a *app) rememberMiguPlaylists(pls []netease.Playlist) {
	for _, pl := range pls {
		a.rememberMiguPlaylist(pl)
	}
}

func (a *app) rememberMiguAlbum(al netease.Album) {
	if al.Source != migu.Source || al.SourceID == "" || al.ID == 0 {
		return
	}
	if a.migu.albumSource == nil {
		a.migu.albumSource = map[int64]string{}
	}
	a.migu.albumSource[al.ID] = al.SourceID
}

func (a *app) rememberMiguArtist(ar netease.Artist) {
	if ar.Source != migu.Source || ar.SourceID == "" || ar.ID == 0 {
		return
	}
	if a.migu.artistSource == nil {
		a.migu.artistSource = map[int64]string{}
	}
	a.migu.artistSource[ar.ID] = ar.SourceID
}

func (a *app) rememberMiguTrack(tr netease.Track) {
	if tr.Source != migu.Source || tr.ID == 0 {
		return
	}
	if a.migu.tracksByID == nil {
		a.migu.tracksByID = map[int64]netease.Track{}
	}
	a.migu.tracksByID[tr.ID] = tr
	a.rememberMiguAlbum(tr.Album)
	for _, ar := range tr.Artists {
		a.rememberMiguArtist(ar)
	}
}

func (a *app) rememberMiguTracks(tracks []netease.Track) {
	for _, tr := range tracks {
		a.rememberMiguTrack(tr)
	}
}

func (a *app) miguTrackByID(id int64) (netease.Track, bool) {
	if id >= 0 {
		return netease.Track{}, false
	}
	if cur := a.pl.Current(); cur != nil && cur.ID == id && cur.Source == migu.Source {
		return *cur, true
	}
	for _, t := range a.pl.Queue() {
		if t.ID == id && t.Source == migu.Source {
			return t, true
		}
	}
	if tr, ok := a.migu.tracksByID[id]; ok {
		return tr, true
	}
	return netease.Track{}, false
}

// canPlayTrack reports whether tr can play either directly or via Migu fallback.
func (a *app) canPlayTrack(tr *netease.Track) bool {
	if tr == nil {
		return false
	}
	if tr.Playable() {
		return true
	}
	return a.isFallbackTrack(tr)
}

// isFallbackTrack reports whether tr is an unplayable/VIP NetEase track that
// Migu fallback is enabled to try unblocking.
func (a *app) isFallbackTrack(tr *netease.Track) bool {
	return tr != nil && a.settings.MiguFallback && tr.LocalPath == "" && tr.ProgramID == 0 && tr.IsNetEase()
}

// allowCachedStream prevents serving an old cached Migu fallback stream when the
// listener has disabled Migu fallback in settings.
func (a *app) allowCachedStream(tr netease.Track, st *netease.Stream) bool {
	if st == nil {
		return false
	}
	if tr.IsNetEase() && st.Source == migu.Source && !a.settings.MiguFallback {
		return false
	}
	return true
}

func (a *app) miguLevelFor(level string) string {
	if a.settings.MiguQuality != "" {
		return a.settings.MiguQuality
	}
	return level
}

// resolveStream resolves a playable audio stream for tr at level, routing direct
// Migu tracks to Migu and optionally falling back to Migu when NetEase refuses
// or only serves a preview.
func (a *app) resolveStream(ctx context.Context, tr netease.Track, level string) (*netease.Stream, error) {
	if tr.Source == migu.Source {
		return miguSongURL(ctx, tr, a.miguLevelFor(level))
	}
	st, err := neteaseStream(ctx, tr.ID, level)
	if err == nil {
		return st, nil
	}
	if a.settings.MiguFallback && tr.ProgramID == 0 && tr.LocalPath == "" && errors.Is(err, netease.ErrUnavailable) {
		if mst, matched, merr := miguMatch(ctx, tr, a.miguLevelFor(level)); merr == nil && mst != nil {
			if matched != nil {
				m := *matched
				a.post(func() {
					if a.migu.matchedTracks == nil {
						a.migu.matchedTracks = map[int64]netease.Track{}
					}
					a.migu.matchedTracks[tr.ID] = m
					if cur := a.pl.Current(); cur != nil && cur.ID == tr.ID {
						a.ensureTrackQualityMetadata(cur)
					}
				})
			}
			return mst, nil
		}
	}
	return nil, err
}

func (a *app) trackBadge(c *ui.Context, tr *netease.Track, p palette) {
	switch {
	case tr.Source == migu.Source:
		ui.Text(c, "咪咕").FontSize(9).FontWeight(800).TextColor(p.accent).
			Padding(0, 4).Radius(3).Border(1, p.accent.Alpha(0.5))
	case !tr.Playable() && a.isFallbackTrack(tr):
		ui.Text(c, "换源").FontSize(9).FontWeight(800).TextColor(p.accent).
			Padding(0, 4).Radius(3).Border(1, p.accent.Alpha(0.55))
	case !tr.Playable():
		ui.Text(c, "VIP").FontSize(9).FontWeight(800).TextColor(p.accent).
			Padding(0, 4).Radius(3).Border(1, p.accent.Alpha(0.6))
	}
}

func streamSourceBadge(c *ui.Context, tr *netease.Track, st *netease.Stream) {
	if st == nil || st.Source != migu.Source {
		return
	}
	label := "咪咕"
	if tr != nil && tr.IsNetEase() {
		label = "咪咕换源"
	}
	ui.Text(c, label).FontSize(9.5).FontWeight(700).TextColor(white.Alpha(0.82)).
		Padding(1, 6).Radius(4).Background(white.Alpha(0.14))
}

var miguQualityLevels = []struct{ tone, label string }{
	{"", "跟随播放音质"},
	{"ZQ32", "至臻母带（ZQ32）"},
	{"ZQ24", "至臻 24bit（ZQ24）"},
	{"AV3A", "菁彩声（AV3A）"},
	{"Z3D", "臻 3D（Z3D）"},
	{"SQ", "无损（SQ FLAC）"},
	{"HQ", "极高（HQ 320k）"},
	{"PQ", "标准（PQ 128k）"},
	{"LQ", "流畅（LQ 64k）"},
}

func (a *app) probeMiguQuality() {
	if a.migu.probing {
		return
	}
	a.migu.probing = true
	a.migu.probeNote = "正在检测咪咕可用音质…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		stats, err := miguProbe(ctx)
		a.post(func() {
			a.migu.probing = false
			if err != nil {
				a.migu.probeNote = "检测失败：" + err.Error()
				return
			}
			var okTiers []string
			for _, st := range stats {
				if st.OK {
					label := st.Tone
					switch st.Tone {
					case "ZQ32":
						label = "ZQ32母带"
					case "ZQ24":
						label = "24bit至臻"
					case "AV3A":
						label = "AV3A菁彩声"
					case "Z3D":
						label = "臻3D"
					case "SQ":
						label = "SQ无损"
					case "HQ":
						label = "HQ 320k"
					case "PQ":
						label = "PQ 128k"
					case "LQ":
						label = "LQ 64k"
					}
					if st.Size > 0 {
						label += fmt.Sprintf("(%.1fMB)", float64(st.Size)/(1<<20))
					}
					okTiers = append(okTiers, label)
				}
			}
			if len(okTiers) == 0 {
				a.migu.probeNote = "未检测到可用的完整音频流"
			} else {
				a.migu.probeNote = "可用：" + strings.Join(okTiers, " · ")
			}
		})
	}()
}

func (a *app) miguSettingsGroup(k *settingsKit) {
	s := &a.settings
	k.group("音源与换源", func() {
		k.row("当前音源", "网易云与咪咕音源独立运行，互不干扰；也可在左下角登录框直接切换", func() {
			chosen := "网易云音乐"
			if a.isMiguSource() {
				chosen = "咪咕音乐"
			}
			k.choose("audio-source", "当前音源", chosen, []string{"网易云音乐", "咪咕音乐"}, func(i int) {
				if i == 1 {
					a.setAudioSource(migu.Source)
				} else {
					a.setAudioSource("")
				}
			})
		})
		k.rowOf(settingRow{label: "咪咕音质", desc: "支持至臻母带 ZQ32、24bit 至臻 ZQ24、AV3A、Z3D、无损 SQ、HQ 320k、PQ 128k 与 LQ 64k",
			tip: "曲目本身或当前账号无对应高档位时，会自动按顺序向下回退到最高可用完整音质"}, func() {
			labels := make([]string, len(miguQualityLevels))
			chosen := miguQualityLevels[0].label
			for i, q := range miguQualityLevels {
				labels[i] = q.label
				if q.tone == s.MiguQuality {
					chosen = q.label
				}
			}
			k.choose("migu-quality", "咪咕音质", chosen, labels, func(i int) {
				s.MiguQuality = miguQualityLevels[i].tone
				a.changed()
			})
		})
		k.row("搜索页显示音源切换", "在搜索页顶部快捷切换网易云与咪咕曲库", func() {
			k.toggle("migu-search", "搜索页显示音源切换", &s.MiguSearch, false, a.changed)
		})
		k.row("网易云无版权时用咪咕换源", "默认关闭，保持网易云与咪咕音源完全独立；开启后仅在网易云无法播放时尝试用咪咕匹配", func() {
			k.toggle("migu-fallback", "网易云无版权时用咪咕换源", &s.MiguFallback, false, a.changed)
		})
		accLabel, accDesc := "未登录咪咕账号", "可选：扫码或浏览器验证码登录后，可尝试解锁咪咕会员更高音质"
		if migu.HasCookie() {
			accLabel = "已登录咪咕账号"
			if s.MiguUser != "" {
				accLabel += " · " + s.MiguUser
			}
			accDesc = "当前咪咕播放与下载将携带你的咪咕会话凭据"
		}
		k.stateRow(accLabel, accDesc, func() {
			btnText := "扫码 / 验证码登录…"
			if migu.HasCookie() {
				btnText = "重新登录…"
			}
			if k.button("migu-login-open", btnText, nil, false, false).Clicked() {
				a.openMiguLogin()
			}
			if migu.HasCookie() {
				if k.button("migu-logout", "退出登录", nil, false, true).Clicked() {
					a.signOutMigu()
				}
			}
		})
		probeDesc := a.migu.probeNote
		if probeDesc == "" {
			probeDesc = "测试当前网络与凭据下咪咕 LQ / PQ / HQ / SQ / ZQ24 / ZQ32 / Z3D / AV3A 可用档位"
		}
		k.stateRow("咪咕音质连通性", probeDesc, func() {
			label := "检测可用音质"
			if a.migu.probing {
				label = "检测中…"
			}
			if k.button("migu-probe", label, nil, false, a.migu.probing).Clicked() {
				a.probeMiguQuality()
			}
		})
	})
}
