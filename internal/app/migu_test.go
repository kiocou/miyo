package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

// NetEase and Migu sources stay independent by default; enabling MiguFallback explicitly rescues unplayable NetEase tracks.
func TestMiguFallbackResolution(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	if a.settings.MiguFallback {
		t.Fatal("MiguFallback should default to false so NetEase and Migu stay independent")
	}

	oldNetease := neteaseStream
	oldMatch := miguMatch
	oldDirect := miguSongURL
	defer func() {
		neteaseStream, miguMatch, miguSongURL = oldNetease, oldMatch, oldDirect
	}()

	neteaseStream = func(context.Context, int64, string) (*netease.Stream, error) {
		return nil, netease.ErrNoCopyright
	}
	miguMatch = func(context.Context, netease.Track, string) (*netease.Stream, *netease.Track, error) {
		return &netease.Stream{URL: "https://freetyst.nf.migu.cn/demo.mp3", Source: migu.Source, Type: "mp3", Level: "exhigh"}, nil, nil
	}
	miguSongURL = func(_ context.Context, tr netease.Track, _ string) (*netease.Stream, error) {
		return &netease.Stream{URL: "https://freetyst.nf.migu.cn/" + tr.SourceID + ".flac", Source: migu.Source, Type: "flac", Level: "lossless"}, nil
	}

	neTrack := netease.Track{ID: 1001, Name: "晴天"}
	if _, err := a.resolveStream(context.Background(), neTrack, "exhigh"); !errors.Is(err, netease.ErrUnavailable) {
		t.Fatalf("expected NetEase error with independent default sources, got %v", err)
	}

	mTrack := netease.Track{ID: -55, Name: "晴天", Source: migu.Source, SourceID: "cid55"}
	if st, err := a.resolveStream(context.Background(), mTrack, "lossless"); err != nil || st == nil || st.Type != "flac" {
		t.Fatalf("expected direct Migu stream, got st=%+v err=%v", st, err)
	}

	a.settings.MiguFallback = true
	st, err := a.resolveStream(context.Background(), neTrack, "exhigh")
	if err != nil || st == nil || st.Source != migu.Source {
		t.Fatalf("expected Migu fallback stream when explicitly enabled, got st=%+v err=%v", st, err)
	}
	if !a.allowCachedStream(neTrack, st) {
		t.Fatal("cached Migu fallback stream should be allowed while MiguFallback is on")
	}

	a.settings.MiguFallback = false
	if a.allowCachedStream(neTrack, st) {
		t.Fatal("cached Migu fallback stream should be rejected after turning MiguFallback off")
	}
}

// Regression: pollMiguQR must immediately exchange the ticket on QRDone instead of waiting on an unsynchronized bool set inside async a.post.
func TestMiguQRPollAdvancesToTokenExchange(t *testing.T) {
	oldPoll, oldEx := miguPollQR, miguExchangeToken
	defer func() { miguPollQR, miguExchangeToken = oldPoll, oldEx }()

	exchanged := make(chan string, 1)
	miguPollQR = func(context.Context, string) (migu.QRStatus, string, string, string, error) {
		return migu.QRDone, "qr_ticket_77", "https://passport.migu.cn/sso", "", nil
	}
	miguExchangeToken = func(_ context.Context, ticket, _ string) (*migu.Account, error) {
		exchanged <- ticket
		return &migu.Account{Cookie: "pacmtoken=ok", Token: "ok", Nickname: "测试用户"}, nil
	}

	a := newTestApp()
	a.login.open = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.migu.login.gen = 1
	a.pollMiguQR(ctx, 1, "sid_77")

	select {
	case got := <-exchanged:
		if got != "qr_ticket_77" {
			t.Fatalf("unexpected exchanged ticket %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pollMiguQR did not call miguExchangeToken after QRDone")
	}
}

// Clicking the bottom-left login box opens the unified dialog where the listener can switch between NetEase and Migu.
func TestBottomLeftLoginSwitcherUI(t *testing.T) {
	defer migu.SetCookie("")
	oldStartQR := miguStartQR
	defer func() { miguStartQR = oldStartQR }()
	miguStartQR = func(context.Context) (*migu.QRSession, error) {
		return nil, errors.New("offline")
	}

	a := newTestApp()
	a.settings = defaultSettings()
	tt := ui.NewTester(a.view, 1000, 720)
	tt.Frame()

	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click bottom-left account box: %v", err)
	}
	tt.Frame()
	if !a.login.open || a.login.source != "" {
		t.Fatalf("expected unified login dialog open on NetEase, got open=%v src=%q", a.login.open, a.login.source)
	}

	if err := tt.Click("咪咕音乐"); err != nil {
		t.Fatalf("switch login dialog to 咪咕音乐: %v", err)
	}
	tt.Frame()
	if a.login.source != migu.Source || a.settings.AudioSource != migu.Source {
		t.Fatalf("expected Migu source selected, login.source=%q AudioSource=%q", a.login.source, a.settings.AudioSource)
	}

	if err := tt.Click("网易云音乐"); err != nil {
		t.Fatalf("switch login dialog back to 网易云音乐: %v", err)
	}
	tt.Frame()
	if a.login.source != "" || a.settings.AudioSource != "" {
		t.Fatalf("expected NetEase source restored, login.source=%q AudioSource=%q", a.login.source, a.settings.AudioSource)
	}
}

// Switching between NetEase and Migu in Search routes the query to Migu and renders Migu tracks.
func TestSearchPageSourceSwitchUI(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.searched = route{page: pageSearch, query: "晴天", kind: netease.SearchSongs}

	oldNE, oldMG := searchResults, miguSearchResults
	defer func() { searchResults, miguSearchResults = oldNE, oldMG }()

	neDone := make(chan struct{}, 2)
	mgDone := make(chan struct{}, 2)
	searchResults = func(query string, kind netease.SearchType, limit, offset int) (netease.SearchResult, error) {
		defer func() { neDone <- struct{}{} }()
		return netease.SearchResult{
			Total: 1,
			Songs: []netease.Track{{ID: 11, Name: "晴天（网易）"}},
		}, nil
	}
	miguSearchResults = func(query string, kind netease.SearchType, limit, offset int) (netease.SearchResult, error) {
		defer func() { mgDone <- struct{}{} }()
		return netease.SearchResult{
			Total: 1,
			Songs: []netease.Track{{ID: -11, Name: "晴天（咪咕）", Source: migu.Source, SourceID: "cid11", CopyrightID: "cr11"}},
		}, nil
	}

	r := route{page: pageSearch, query: "晴天", kind: netease.SearchSongs}
	a.show(r)
	wait(t, neDone, "initial netease search")
	a.searches[r].res.Songs = []netease.Track{{ID: 11, Name: "晴天（网易）"}}
	a.searches[r].page.loaded = true

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.motion = motionOff
		a.searchPage(c)
	}, 960, 720)
	tt.Frame()

	if err := tt.Click("咪咕"); err != nil {
		t.Fatalf("click 咪咕 source tab: %v", err)
	}
	wait(t, mgDone, "migu search")
	if a.route.source != migu.Source {
		t.Fatalf("expected route source=%q, got %q", migu.Source, a.route.source)
	}
	d := a.searches[a.route]
	d.res = netease.SearchResult{
		Total: 1,
		Songs: []netease.Track{{ID: -11, Name: "晴天（咪咕）", Source: migu.Source, SourceID: "cid11", CopyrightID: "cr11"}},
	}
	d.page.loading, d.page.loaded = false, true
	tt.Frame()

	if _, ok := tt.Find("晴天（咪咕）"); !ok {
		t.Fatal("expected Migu search result row to render after switching source")
	}
}

// Playback settings expose Migu login dialog, browser callback exchange, and sign-out.
func TestPlaybackSettingsMiguUI(t *testing.T) {
	defer migu.SetCookie("")

	oldStartQR, oldExchange, oldOpen := miguStartQR, miguExchangeToken, openURL
	defer func() {
		miguStartQR, miguExchangeToken, openURL = oldStartQR, oldExchange, oldOpen
	}()

	miguStartQR = func(context.Context) (*migu.QRSession, error) {
		return nil, errors.New("offline")
	}
	exCalled := make(chan string, 1)
	miguExchangeToken = func(_ context.Context, ticket, _ string) (*migu.Account, error) {
		exCalled <- ticket
		return &migu.Account{Cookie: "pacmtoken=tok_cb; uid=u9", Token: "tok_cb", UID: "u9", Nickname: "小咪"}, nil
	}
	openedURL := ""
	openURL = func(u string) error {
		openedURL = u
		return nil
	}

	a := newTestApp()
	a.settings = defaultSettings()
	a.settingsView = settingsView{open: true, tab: settingsPlayback}

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.motion = motionOff
		a.settingsPanel(c)
		a.loginDialog(c)
	}, 960, 1250)

	tt.Frame()
	title, ok1 := tt.Find("网易云无版权时用咪咕换源")
	pane, ok2 := tt.Find("播放设置项")
	if !ok1 || !ok2 {
		t.Fatalf("missing MiguFallback row or pane: ok1=%v ok2=%v", ok1, ok2)
	}
	tt.ClickAt(pane.X+pane.W-30-14-22, title.Y+title.H/2)
	tt.Frame()
	if !a.settings.MiguFallback {
		t.Fatal("expected MiguFallback toggle to flip true")
	}

	if err := tt.Click("扫码 / 验证码登录…"); err != nil {
		t.Fatalf("click login button: %v", err)
	}
	tt.Frame()
	if !a.login.open || a.login.source != migu.Source {
		t.Fatalf("expected unified login dialog open on Migu, got open=%v src=%q", a.login.open, a.login.source)
	}

	if err := tt.Click("使用验证码 / 浏览器登录"); err != nil {
		t.Fatalf("switch to browser login: %v", err)
	}
	tt.Frame()
	if !strings.Contains(openedURL, "passport.migu.cn/login") || a.migu.login.browserURL == "" {
		t.Fatalf("expected passport login URL opened, got %q", openedURL)
	}

	idx := strings.Index(openedURL, "callbackURL=")
	if idx < 0 {
		t.Fatalf("missing callbackURL in %q", openedURL)
	}
	rawCB := strings.Split(openedURL[idx+len("callbackURL="):], "&")[0]
	cbURL := strings.ReplaceAll(strings.ReplaceAll(rawCB, "%3A", ":"), "%2F", "/") + "?token=ticket_ok"
	resp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("GET callback URL %q: %v", cbURL, err)
	}
	resp.Body.Close()
	if got := wait(t, exCalled, "browser callback token exchange"); got != "ticket_ok" {
		t.Fatalf("unexpected exchanged ticket %q", got)
	}
	a.login.open = false
	a.finishMiguLogin(&migu.Account{Cookie: "pacmtoken=tok_cb; uid=u9", Token: "tok_cb", UID: "u9", Nickname: "小咪"})
	tt.Frame()
	if !migu.HasCookie() || a.settings.MiguUser != "小咪" {
		t.Fatalf("expected signed-in Migu user, HasCookie=%v user=%q", migu.HasCookie(), a.settings.MiguUser)
	}

	if err := tt.Click("退出登录"); err != nil {
		t.Fatalf("click sign out: %v", err)
	}
	if a.settings.MiguCookie != "" || migu.HasCookie() {
		t.Fatalf("expected cleared Migu cookie after logout")
	}
}

// Migu track context menus keep local/playback/navigation actions and omit NetEase playlist/like actions.
func TestMiguSongMenuOmitsNetEaseOnlyActions(t *testing.T) {
	a := newTestApp()
	a.profile = &netease.Profile{UserID: 1, Nickname: "Tester"}
	a.mine = []netease.Playlist{{ID: 99, Name: "我的歌单"}}

	tr := &netease.Track{
		ID:          -77,
		Name:        "夜曲",
		Source:      migu.Source,
		SourceID:    "cid77",
		CopyrightID: "cr77",
		Artists:     []netease.Artist{{ID: -2, Name: "周杰伦", Source: migu.Source, SourceID: "112"}},
		Album:       netease.Album{ID: -3, Name: "十一月的萧邦", Source: migu.Source, SourceID: "8593"},
	}

	groups := a.songActions(tr, 0)
	seen := map[string]bool{}
	for _, g := range groups {
		for _, act := range g {
			seen[act.label] = true
		}
	}
	for _, want := range []string{"下一首播放", "下载到本机", "下载歌词", "复制链接", "查看歌手：周杰伦", "查看专辑：十一月的萧邦"} {
		if !seen[want] {
			t.Errorf("missing expected Migu song action %q in %v", want, seen)
		}
	}
	if seen["添加到歌单"] {
		t.Error("did not expect NetEase playlist action on Migu catalog track")
	}
}

// Settings sidebar account card reflects Migu login when AudioSource is migu, while NetEase buttons keep opening NetEase login.
func TestSettingsSidebarSyncsMiguAccountAndNetEaseLogin(t *testing.T) {
	defer migu.SetCookie("")
	migu.SetCookie("pacmtoken=tok_sidebar; uid=u88")

	a := newTestApp()
	defer func() { a.login.gen++ }()
	a.settings = defaultSettings()
	a.settings.AudioSource = migu.Source
	a.settings.MiguCookie = "pacmtoken=tok_sidebar; uid=u88"
	a.settings.MiguUser = "小咪测试"
	a.settingsView = settingsView{open: true, tab: settingsAccount}

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.motion = motionOff
		a.settingsPanel(c)
		a.loginDialog(c)
	}, 960, 720)
	tt.Frame()

	name, sub, _, miguIcon := a.settingsAccountSummary()
	if !miguIcon || name != "小咪测试" || sub != "咪咕音乐账号" {
		t.Fatalf("unexpected settings sidebar summary: name=%q sub=%q miguIcon=%v", name, sub, miguIcon)
	}
	if _, ok := tt.Find("咪咕音乐账号"); !ok {
		t.Fatal("expected settings sidebar account card to display 咪咕音乐账号")
	}

	if err := tt.Click("扫码登录"); err != nil {
		t.Fatalf("click NetEase 扫码登录 button in settings: %v", err)
	}
	tt.Frame()
	if !a.login.open || a.login.source != "" {
		t.Fatalf("expected NetEase login button to open NetEase tab, got open=%v src=%q", a.login.open, a.login.source)
	}
}

// Search autocomplete uses miguSearchSuggest when the active audio source is Migu.
func TestSearchSuggestRoutesToMiguSource(t *testing.T) {
	oldMiguSuggest, oldNeteaseSuggest := miguSearchSuggest, searchSuggest
	defer func() {
		miguSearchSuggest, searchSuggest = oldMiguSuggest, oldNeteaseSuggest
	}()

	mgDone := make(chan string, 1)
	miguSearchSuggest = func(q string) ([]string, error) {
		mgDone <- q
		return []string{"稻香", "夜曲"}, nil
	}
	searchSuggest = func(string) (netease.Suggestions, error) {
		t.Fatal("did not expect NetEase suggest when AudioSource is migu")
		return netease.Suggestions{}, nil
	}

	a := newTestApp()
	a.settings = defaultSettings()
	a.settings.AudioSource = migu.Source

	a.suggest("稻香")
	if got := wait(t, mgDone, "migu suggest"); got != "稻香" {
		t.Fatalf("expected migu suggest query %q, got %q", "稻香", got)
	}
	gotItems := miguSuggestionList([]string{"稻香", "夜曲"})
	if len(gotItems) != 2 || gotItems[0].route.source != migu.Source || gotItems[0].name != "稻香" {
		t.Fatalf("unexpected Migu suggestions: %+v", gotItems)
	}
}

// MiguQuality setting overrides Quality when resolving Migu tracks while keeping NetEase playback independent.
func TestMiguQualitySettingOverride(t *testing.T) {
	oldDirect, oldNetease := miguSongURL, neteaseStream
	defer func() { miguSongURL, neteaseStream = oldDirect, oldNetease }()

	var gotMiguLevel, gotNeteaseLevel string
	miguSongURL = func(_ context.Context, _ netease.Track, level string) (*netease.Stream, error) {
		gotMiguLevel = level
		return &netease.Stream{URL: "https://freetyst.nf.migu.cn/demo.flac", Source: migu.Source, Type: "flac", Level: level}, nil
	}
	neteaseStream = func(_ context.Context, _ int64, level string) (*netease.Stream, error) {
		gotNeteaseLevel = level
		return &netease.Stream{URL: "https://music.163.com/demo.mp3", Type: "mp3", Level: level}, nil
	}

	a := newTestApp()
	a.settings = defaultSettings()
	a.settings.Quality = "exhigh"
	a.settings.MiguQuality = "ZQ32"

	if _, err := a.resolveStream(context.Background(), netease.Track{ID: -10, Name: "稻香", Source: migu.Source, SourceID: "600902000006889010"}, a.settings.Quality); err != nil {
		t.Fatalf("resolve Migu stream: %v", err)
	}
	if _, err := a.resolveStream(context.Background(), netease.Track{ID: 100, Name: "晴天"}, a.settings.Quality); err != nil {
		t.Fatalf("resolve NetEase stream: %v", err)
	}
	if gotMiguLevel != "ZQ32" || gotNeteaseLevel != "exhigh" {
		t.Fatalf("unexpected quality routing: migu=%q netease=%q", gotMiguLevel, gotNeteaseLevel)
	}

	a.settingsView = settingsView{open: true, tab: settingsPlayback}
	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.motion = motionOff
		a.settingsPanel(c)
	}, 960, 1250)
	tt.Frame()
	if _, ok := tt.Find("咪咕音质"); !ok {
		t.Fatal("expected 咪咕音质 selector row in playback settings")
	}
}

// Quality menus in player bar and Now Playing list only the active source's supported tiers for the track, and restore settings if the server caps the switch.
func TestPlayerBarAndNowPlayingQualitySwitchUI(t *testing.T) {
	defer migu.SetCookie("")
	migu.SetCookie("")

	oldProbeTrack, oldDetails := miguProbeTrack, neteaseSongDetails
	defer func() {
		miguProbeTrack, neteaseSongDetails = oldProbeTrack, oldDetails
	}()
	miguProbeTrack = func(_ context.Context, tr netease.Track) ([]string, error) {
		return tr.Qualities, nil
	}
	neteaseSongDetails = func([]int64) ([]netease.Track, error) {
		return nil, errors.New("offline")
	}

	// 0. When Privilege is missing, NetEase menu does not expose unverified lossless/Hi-Res tiers.
	unknownApp := newTestApp()
	unknownApp.settings = defaultSettings()
	unknownApp.settings.Quality = "exhigh"
	unknownTr := netease.Track{ID: 99, Name: "未获取权限歌曲"}
	acts := unknownApp.qualityMenuGroups(&unknownTr, nil)[0]
	if len(acts) != 1 || acts[0].label != "极高（320k）" {
		t.Fatalf("expected only active quality while Privilege is unknown, got %+v", acts)
	}

	// 1. Wide player bar with a NetEase track capped at exhigh (320k): omits lossless/Hi-Res and all Migu options.
	a := newTestApp()
	a.settings = defaultSettings()
	a.settings.Quality = "exhigh"
	a.pl.ResolveStream = func(_ context.Context, _ netease.Track, _ string) (*netease.Stream, error) {
		return nil, context.Canceled
	}
	a.pl.Cue([]netease.Track{{
		ID:        101,
		Name:      "让思绪都随风吧",
		Artists:   []netease.Artist{{Name: "供销社乐队"}},
		Duration:  221000,
		Privilege: &netease.Privilege{ID: 101, Pl: 320000, PlLevel: "exhigh"},
	}})

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.motion = motionOff
		a.playerBar(c, 1280)
		a.menuOverlay(c)
	}, 1280, 720)
	tt.Frame()

	if err := tt.Click("切换音质"); err != nil {
		t.Fatalf("click bottom player bar quality pill: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("无损（FLAC）"); ok {
		t.Fatal("did not expect 无损（FLAC） in NetEase menu when PlLevel is exhigh")
	}
	if _, ok := tt.Find("SQ"); ok {
		t.Fatal("did not expect Migu options on a NetEase track")
	}
	if err := tt.Click("标准（128k）"); err != nil {
		t.Fatalf("choose standard from bottom player bar menu: %v", err)
	}
	if a.settings.Quality != "standard" {
		t.Fatalf("expected Quality updated to standard, got %q", a.settings.Quality)
	}

	// Verify rollback if the server caps a requested upgrade back down.
	cApp := newTestApp()
	cApp.settings = defaultSettings()
	cApp.settings.Quality = "lossless"
	cApp.pl.Cue([]netease.Track{{
		ID:        101,
		Name:      "让思绪都随风吧",
		Artists:   []netease.Artist{{Name: "供销社乐队"}},
		Duration:  221000,
		Privilege: &netease.Privilege{ID: 101, Pl: 320000, PlLevel: "exhigh"},
	}})
	cApp.migu.qualityReq = &qualitySwitchReq{
		trackID:     101,
		miguSrc:     false,
		wantLevel:   "lossless",
		wantLabel:   "无损（FLAC）",
		prevNetease: "exhigh",
	}
	cApp.pl.SetStream(&netease.Stream{Level: "exhigh", Type: "mp3"})
	cApp.checkQualitySwitchResult()
	if cApp.settings.Quality != "exhigh" || !strings.Contains(cApp.toast, "最高支持") {
		t.Fatalf("expected quality synced back to exhigh with ceiling toast, quality=%q toast=%q", cApp.settings.Quality, cApp.toast)
	}

	// Verify 歌曲详情 menu also exposes "切换音质".
	if err := tt.Click("歌曲详情"); err != nil {
		t.Fatalf("click 歌曲详情: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("切换音质"); !ok {
		t.Fatal("expected 切换音质 item in 歌曲详情 menu")
	}

	// 2. Now Playing with a Migu track that only carries ZQ24/SQ/HQ/PQ: omits ZQ32/AV3A/Z3D and all NetEase options.
	b := newTestApp()
	b.settings = defaultSettings()
	b.settings.MiguQuality = "SQ"
	b.nowPlaying = true
	b.pl.ResolveStream = func(_ context.Context, _ netease.Track, _ string) (*netease.Stream, error) {
		return nil, context.Canceled
	}
	b.pl.Cue([]netease.Track{{
		ID:        -202,
		Name:      "说好不哭",
		Source:    migu.Source,
		SourceID:  "600919000007816057",
		Qualities: []string{"ZQ24", "SQ", "HQ", "PQ"},
		Artists:   []netease.Artist{{Name: "周杰伦"}},
		Duration:  222000,
	}})

	tt2 := ui.NewTester(func(c *ui.Context) {
		b.pal = paletteFor(c.Theme())
		b.motion = motionOff
		b.nowPlayingView(c)
		b.menuOverlay(c)
	}, 1100, 760)
	tt2.Frame()

	if err := tt2.Click("切换音质"); err != nil {
		t.Fatalf("click Now Playing quality pill: %v", err)
	}
	tt2.Frame()
	if _, ok := tt2.Find("至臻母带（ZQ32）"); ok {
		t.Fatal("did not expect ZQ32 when track formats only include ZQ24/SQ/HQ/PQ")
	}
	if _, ok := tt2.Find("较高（192k）"); ok {
		t.Fatal("did not expect NetEase options in Migu quality menu")
	}
	if err := tt2.Click("至臻 24bit（ZQ24）"); err != nil {
		t.Fatalf("choose ZQ24 from Now Playing quality menu: %v", err)
	}
	if b.settings.MiguQuality != "ZQ24" {
		t.Fatalf("expected MiguQuality updated to ZQ24, got %q", b.settings.MiguQuality)
	}
}

