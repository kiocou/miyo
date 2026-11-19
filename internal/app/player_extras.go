package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

// toggleLyricTrans shows or hides the translations of lyrics, and says
// which, as the player bar's button does.
func (a *app) toggleLyricTrans() {
	a.settings.LyricTrans = !a.settings.LyricTrans
	a.changed()
	if a.settings.LyricTrans {
		a.say("lyric-trans", "已显示歌词翻译")
	} else {
		a.say("lyric-trans", "已隐藏歌词翻译")
	}
	a.redraw()
}

// addToPlaylistMenu opens, above the player bar's button b, the playlists
// the song tr can be added to; signed out, it asks the listener to sign in.
func (a *app) addToPlaylistMenu(b ui.Element, tr *netease.Track) {
	if a.profile == nil {
		a.toast = "登录后才能添加到歌单"
		a.openLoginFor("")
		a.redraw()
		return
	}
	song := *tr
	acts := a.playlistActions(&song, 0)
	if len(acts) == 0 || len(acts[0].sub) == 0 {
		return
	}
	// The submenu's groups, as a menu of their own: new, then the lists.
	a.menuBelow(b, false, acts[0].sub, a.songHead(&song))
}

// podcastSpeeds are the speeds episodes play at, their pitch kept.
var podcastSpeeds = []float64{0.75, 1, 1.25, 1.5, 1.75, 2}

// speedLabel says a speed as players do: 1.5×.
func speedLabel(v float64) string {
	if v <= 0 {
		v = 1
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + "×"
}

// setPodcastSpeed has episodes play at v, the one playing too.
func (a *app) setPodcastSpeed(v float64) {
	if v == 1 {
		v = 0
	}
	a.settings.PodcastSpeed = v
	a.pl.SetPodcastSpeed(v)
	a.changed()
	a.redraw()
}

// speedButton is an episode's speed in Now Playing: a click goes to the
// next of podcastSpeeds, after the last back to the first.
func (a *app) speedButton(c *ui.Context) {
	cur := a.settings.PodcastSpeed
	if cur <= 0 {
		cur = 1
	}
	b := ui.ButtonBase(c.Key("np-speed")).Height(18).PaddingX(7).Radius(999).AlignItems(ui.Center).FocusRing(false).Label("播放速度")
	bg := white.Alpha(0.12)
	if b.Hovered() {
		bg = white.Alpha(0.2)
	}
	b.Background(bg).Children(func() {
		ui.Text(c, speedLabel(cur)).FontSize(10.5).FontWeight(600).FontFeatures("tnum").TextColor(white.Alpha(0.85))
	})
	if b.Clicked() {
		next := podcastSpeeds[0]
		for i, v := range podcastSpeeds {
			if v == cur && i+1 < len(podcastSpeeds) {
				next = podcastSpeeds[i+1]
			}
		}
		a.setPodcastSpeed(next)
		a.say("speed", "播放速度 "+speedLabel(next))
	}
}

// podcastSpeedRow chooses the speed of episodes in the settings.
func (a *app) podcastSpeedRow(c *ui.Context, row func(string, string, func())) {
	row("播客播放速度", "加快或放慢节目，声音的音调保持不变；也可以在播放页点速度切换", func() {
		labels := make([]string, len(podcastSpeeds))
		chosen := speedLabel(a.settings.PodcastSpeed)
		for i, v := range podcastSpeeds {
			labels[i] = speedLabel(v)
		}
		if a.paneSelect(c.Key("podcast-speed"), "播客播放速度", &chosen, labels) {
			for _, v := range podcastSpeeds {
				if speedLabel(v) == chosen {
					a.setPodcastSpeed(v)
				}
			}
		}
	})
}

type qualitySwitchReq struct {
	trackID     int64
	miguSrc     bool
	wantLevel   string
	wantLabel   string
	prevNetease string
	prevMigu    string
}

func streamIsMigu(tr *netease.Track, st *netease.Stream) bool {
	return (tr != nil && tr.Source == migu.Source) || (st != nil && st.Source == migu.Source)
}

func qualityLevelLabel(level string, miguSrc bool) (label string, hi bool) {
	switch level {
	case "jymaster", "ZQ32", "zq32":
		return "至臻母带", true
	case "hires", "ZQ24", "zq24", "ZQ", "zq":
		if miguSrc {
			return "至臻24", true
		}
		return "Hi-Res", true
	case "jyeffect", "AV3A", "av3a":
		return "菁彩声", true
	case "sky", "Z3D", "z3d":
		return "臻3D", true
	case "lossless", "SQ", "sq":
		return "无损", true
	case "exhigh", "HQ", "hq":
		return "极高", false
	case "higher":
		return "较高", false
	case "lq", "LQ":
		return "流畅", false
	case "standard", "PQ", "pq":
		return "标准", false
	default:
		return "极高", false
	}
}

func (a *app) activeQualityBadge(tr *netease.Track, st *netease.Stream) (label string, hi bool) {
	isMigu := streamIsMigu(tr, st)
	if st != nil && st.Level != "" {
		return qualityLevelLabel(st.Level, isMigu)
	}
	lv := a.settings.Quality
	if isMigu {
		lv = a.miguLevelFor(lv)
	}
	return qualityLevelLabel(lv, isMigu)
}

func (a *app) ensureTrackQualityMetadata(tr *netease.Track) {
	if tr == nil || tr.LocalPath != "" || tr.ProgramID != 0 {
		return
	}
	targetMigu := *tr
	isMigu := tr.Source == migu.Source
	if !isMigu {
		if m, ok := a.migu.matchedTracks[tr.ID]; ok {
			isMigu = true
			targetMigu = m
		}
	}
	if isMigu {
		if len(a.migu.trackVerified[tr.ID]) > 0 || a.migu.trackProbeAsked[tr.ID] {
			return
		}
		if a.migu.trackProbeAsked == nil {
			a.migu.trackProbeAsked = map[int64]bool{}
		}
		a.migu.trackProbeAsked[tr.ID] = true
		trackID := tr.ID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			fmts, err := miguProbeTrack(ctx, targetMigu)
			if (err != nil || len(fmts) == 0) && len(targetMigu.Qualities) == 0 {
				fmts, err = miguTrackFormats(ctx, targetMigu)
			}
			a.post(func() {
				if err == nil && len(fmts) > 0 {
					if a.migu.trackVerified == nil {
						a.migu.trackVerified = map[int64][]string{}
					}
					a.migu.trackVerified[trackID] = fmts
				}
				if cur := a.pl.Current(); cur != nil && cur.ID == trackID && a.menu.open {
					a.menu.groups = a.qualityMenuGroups(cur, a.pl.Stream())
					a.redraw()
				}
			})
		}()
		return
	}
	if tr.IsNetEase() && tr.Privilege == nil && a.migu.trackPriv[tr.ID] == nil && !a.migu.trackPrivAsked[tr.ID] {
		if a.migu.trackPrivAsked == nil {
			a.migu.trackPrivAsked = map[int64]bool{}
		}
		a.migu.trackPrivAsked[tr.ID] = true
		id := tr.ID
		go func() {
			songs, err := neteaseSongDetails([]int64{id})
			a.post(func() {
				if err == nil && len(songs) > 0 && songs[0].Privilege != nil {
					if a.migu.trackPriv == nil {
						a.migu.trackPriv = map[int64]*netease.Privilege{}
					}
					a.migu.trackPriv[id] = songs[0].Privilege
					if cur := a.pl.Current(); cur != nil && cur.ID == id && a.menu.open {
						a.menu.groups = a.qualityMenuGroups(cur, a.pl.Stream())
						a.redraw()
					}
				}
			})
		}()
	}
}

func (a *app) checkQualitySwitchResult() {
	req := a.migu.qualityReq
	if req == nil || a.pl == nil {
		return
	}
	tr := a.pl.Current()
	if tr == nil || tr.ID != req.trackID {
		a.migu.qualityReq = nil
		return
	}
	if a.pl.Loading() {
		return
	}
	a.migu.qualityReq = nil
	st := a.pl.Stream()
	if st == nil {
		a.settings.Quality = req.prevNetease
		a.settings.MiguQuality = req.prevMigu
		a.changed()
		a.toast = "无法切换到「" + req.wantLabel + "」"
		return
	}
	gotLabel, _ := qualityLevelLabel(st.Level, req.miguSrc)
	if req.miguSrc {
		gotTone := migu.ToneForLevel(st.Level)
		wantTone := migu.ToneForLevel(req.wantLevel)
		if gotTone != wantTone {
			a.settings.MiguQuality = gotTone
			a.changed()
			a.toast = "当前曲目暂不支持「" + req.wantLabel + "」，已播放「" + gotLabel + "」"
			return
		}
	} else if st.Level != "" && st.Level != req.wantLevel {
		a.settings.Quality = st.Level
		a.changed()
		a.toast = "该歌曲当前账号最高支持「" + gotLabel + "」，无法切至「" + req.wantLabel + "」"
		return
	}
	a.toast = "已切换为「" + gotLabel + "」"
}

func (a *app) switchNeteaseQuality(tr *netease.Track, level, label string) {
	var tid int64
	if tr != nil {
		tid = tr.ID
	}
	a.migu.qualityReq = &qualitySwitchReq{
		trackID:     tid,
		miguSrc:     false,
		wantLevel:   level,
		wantLabel:   label,
		prevNetease: a.settings.Quality,
		prevMigu:    a.settings.MiguQuality,
	}
	a.settings.Quality = level
	a.changed()
	reloaded := a.pl != nil && a.pl.Reload()
	if !reloaded {
		a.migu.qualityReq = nil
	}
	msg := "正在切换音质：" + label
	if !reloaded {
		msg = "已切换音质：" + label + "（下一首生效）"
	}
	a.toast = msg
	a.say("quality", msg)
	a.redraw()
}

func (a *app) switchMiguQuality(tr *netease.Track, tone, label string) {
	var tid int64
	if tr != nil {
		tid = tr.ID
	}
	a.migu.qualityReq = &qualitySwitchReq{
		trackID:     tid,
		miguSrc:     true,
		wantLevel:   tone,
		wantLabel:   label,
		prevNetease: a.settings.Quality,
		prevMigu:    a.settings.MiguQuality,
	}
	a.settings.MiguQuality = tone
	a.changed()
	reloaded := a.pl != nil && a.pl.Reload()
	if !reloaded {
		a.migu.qualityReq = nil
	}
	msg := "正在切换咪咕音质：" + label
	if !reloaded {
		msg = "已切换咪咕音质：" + label + "（下一首生效）"
	}
	a.toast = msg
	a.say("quality", msg)
	a.redraw()
}

func (a *app) miguQualityActions(tr *netease.Track, st *netease.Stream) []menuAction {
	var known []string
	if tr != nil {
		if v := a.migu.trackVerified[tr.ID]; len(v) > 0 {
			known = v
		} else if len(tr.Qualities) > 0 {
			known = tr.Qualities
		} else if m, ok := a.migu.matchedTracks[tr.ID]; ok && len(m.Qualities) > 0 {
			known = m.Qualities
		} else if cached := a.migu.trackFormats[tr.ID]; len(cached) > 0 {
			known = cached
		}
	}

	curTone := migu.ToneForLevel(a.miguLevelFor(a.settings.Quality))
	if st != nil && st.Level != "" {
		curTone = migu.ToneForLevel(st.Level)
	}

	allowed := map[string]bool{curTone: true}
	for _, f := range known {
		allowed[f] = true
	}
	// ZQ32/AV3A/Z3D only resolve via signed-in PC API (never CDN rewrite).
	if !migu.HasCookie() {
		for _, vipOnly := range []string{"ZQ32", "AV3A", "Z3D"} {
			if vipOnly != curTone {
				delete(allowed, vipOnly)
			}
		}
	}

	var out []menuAction
	for _, q := range miguQualityLevels {
		if q.tone == "" || !allowed[q.tone] {
			continue
		}
		q := q
		out = append(out, menuAction{
			label:   q.label,
			checked: curTone == q.tone,
			do:      func() { a.switchMiguQuality(tr, q.tone, q.label) },
		})
	}
	return out
}

func (a *app) neteaseQualityActions(tr *netease.Track, st *netease.Stream) []menuAction {
	var priv *netease.Privilege
	if tr != nil {
		priv = tr.Privilege
		if priv == nil {
			priv = a.migu.trackPriv[tr.ID]
		}
	}
	cur := a.settings.Quality
	if st != nil && st.Level != "" {
		cur = st.Level
	}
	avail := (&netease.Track{Fee: func() int {
		if tr != nil {
			return tr.Fee
		}
		return 0
	}(), Privilege: priv}).AvailableLevels()

	allowed := map[string]bool{cur: true}
	for _, lv := range avail {
		allowed[lv] = true
	}
	out := make([]menuAction, 0, len(qualityLevels))
	for _, q := range qualityLevels {
		if !allowed[q.level] {
			continue
		}
		q := q
		out = append(out, menuAction{
			label:   q.label,
			checked: cur == q.level,
			do:      func() { a.switchNeteaseQuality(tr, q.level, q.label) },
		})
	}
	return out
}

// qualityMenuGroups builds only the quality options supported by the active track's
// playing audio source (NetEase OR Migu), never dumping unrelated tiers or both sources.
func (a *app) qualityMenuGroups(tr *netease.Track, st *netease.Stream) [][]menuAction {
	if streamIsMigu(tr, st) {
		return [][]menuAction{a.miguQualityActions(tr, st)}
	}
	return [][]menuAction{a.neteaseQualityActions(tr, st)}
}

func (a *app) trackQualityPending(tr *netease.Track, st *netease.Stream) bool {
	if tr == nil || tr.LocalPath != "" || tr.ProgramID != 0 {
		return false
	}
	if streamIsMigu(tr, st) {
		if len(a.migu.trackVerified[tr.ID]) > 0 || len(tr.Qualities) > 0 || len(a.migu.trackFormats[tr.ID]) > 0 {
			return false
		}
		if m, ok := a.migu.matchedTracks[tr.ID]; ok && len(m.Qualities) > 0 {
			return false
		}
		return true
	}
	return tr.Privilege == nil && a.migu.trackPriv[tr.ID] == nil
}

func (a *app) qualityHead(tr *netease.Track, st *netease.Stream) func(c *ui.Context, ink paneInk) {
	return func(c *ui.Context, ink paneInk) {
		isMigu := streamIsMigu(tr, st)
		title := "网易云播放音质"
		if isMigu {
			title = "咪咕播放音质"
		}
		detail := "仅列出此曲支持的可用音质"
		if a.trackQualityPending(tr, st) {
			detail = "正在检测此曲支持音质…"
		} else if st != nil {
			lb, _ := qualityLevelLabel(st.Level, isMigu)
			typ := strings.ToUpper(st.Type)
			if typ == "" {
				typ = "AUDIO"
			}
			if st.Path != "" {
				detail = "当前为本地已下载文件（" + lb + "）"
			} else if st.Size > 0 {
				detail = fmt.Sprintf("当前流：%s · %s · %.1f MB", lb, typ, float64(st.Size)/(1<<20))
			} else if lb != "" {
				detail = "当前流：" + lb
			}
		}
		ui.Column(c).Padding(10, 12, 8, 12).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(600)
			ui.Text(c, detail).FontSize(11.5).TextColor(ink.muted).SingleLine()
		})
	}
}

// qualityButton renders a clickable quality pill in the player bar and Now Playing
// that opens a popup menu to switch the current source's supported quality on the fly.
func (a *app) qualityButton(c *ui.Context, tr *netease.Track, st *netease.Stream, onDark bool) ui.Element {
	a.ensureTrackQualityMetadata(tr)
	text, hi := a.activeQualityBadge(tr, st)
	b := ui.ButtonBase(c).PaddingX(6).Radius(999).AlignItems(ui.Center).FocusRing(false).
		Label("切换音质").Tooltip("音质：" + text + "（点击切换）")
	if onDark {
		b.Height(18)
		fg := white.Alpha(0.8)
		bg := white.Alpha(0.12)
		if b.Hovered() {
			bg = white.Alpha(0.22)
		}
		if hi {
			fg = ui.Hex("#f5d58a")
			b.Border(1, fg.Alpha(0.55))
		}
		b.Background(bg).Children(func() {
			ui.Text(c, text).FontSize(9.5).FontWeight(700).LetterSpacing(0.3).TextColor(fg).SingleLine()
		})
	} else {
		p := a.pal
		b.Height(22)
		fg := p.text.Alpha(0.75)
		bg := p.hover.Alpha(0.75)
		if b.Hovered() {
			bg = p.hover
		}
		if hi {
			fg = p.accent
			b.Border(1, p.accent.Alpha(0.55))
		}
		b.Background(bg).Children(func() {
			ui.Text(c, text).FontSize(10.5).FontWeight(700).TextColor(fg).SingleLine()
		})
	}
	if b.Clicked() {
		a.ensureTrackQualityMetadata(tr)
		a.menuBelow(b, onDark, a.qualityMenuGroups(tr, st), a.qualityHead(tr, st))
	}
	return b
}
