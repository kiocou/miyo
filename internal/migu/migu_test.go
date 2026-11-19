package migu

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"netease-music-mygo/internal/netease"
)

func encryptMRCTestHelper(plain string) string {
	for len(plain) < 32 {
		plain += "0"
	}
	u16 := utf16.Encode([]rune(plain))
	b := make([]byte, len(u16)*2)
	for i, w := range u16 {
		binary.LittleEndian.PutUint16(b[i*2:], w)
	}
	n := (len(b) + 7) / 8
	v := make([]int64, n)
	for i := 0; i < n-1; i++ {
		v[i] = int64(binary.LittleEndian.Uint64(b[i*8:]))
	}
	var tail [8]byte
	copy(tail[:], b[(n-1)*8:])
	v[n-1] = int64(binary.LittleEndian.Uint64(tail[:]))

	k := mrcKeyToLongArray(mrcTEAKey)
	z := v[n-1]
	rounds := int64(52/n + 6)
	var sum int64
	for rounds > 0 {
		rounds--
		sum += mrcTEADelta
		e := (sum >> 2) & 3
		for p := 0; p < n-1; p++ {
			y := v[p+1]
			idx := (int64(p) & 3) ^ e
			z = v[p] + ((((z >> 5) ^ (y << 2)) + ((y >> 3) ^ (z << 4))) ^ ((sum ^ y) + (z ^ k[idx%int64(len(k))])))
			v[p] = z
		}
		y := v[0]
		idx := (int64(n-1) & 3) ^ e
		z = v[n-1] + ((((z >> 5) ^ (y << 2)) + ((y >> 3) ^ (z << 4))) ^ ((y ^ sum) + (z ^ k[idx%int64(len(k))])))
		v[n-1] = z
	}
	var sb strings.Builder
	for _, w := range v {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(w))
		sb.WriteString(hex.EncodeToString(buf[:]))
	}
	return sb.String()
}

func TestSignSearchV3AndSetCookie(t *testing.T) {
	got := signSearchV3("晴天", "ms123", "", "1700000000000")
	if len(got) != 32 {
		t.Fatalf("unexpected md5 hex len: %q", got)
	}

	defer SetCookie("")
	SetCookie("baretoken123")
	if !HasCookie() {
		t.Fatal("expected HasCookie after bare token")
	}
	c, _, tok, _ := authSnapshot()
	if c != "pacmtoken=baretoken123" || tok != "baretoken123" {
		t.Fatalf("unexpected bare cookie snapshot: cookie=%q tok=%q", c, tok)
	}

	SetCookie("aversionid=av1; pacmtoken=pt2; uid=u3")
	c, av, tok, uid := authSnapshot()
	if av != "av1" || tok != "pt2" || uid != "u3" || c == "" {
		t.Fatalf("unexpected parsed snapshot: cookie=%q av=%q tok=%q uid=%q", c, av, tok, uid)
	}
}

func TestDecryptH5Payload(t *testing.T) {
	plain := []byte(`{"code":"000000","data":{"url":"https://freetyst.nf.migu.cn/test.mp3"}}`)
	seed := byte(0x4a)
	key := []byte(h5XORKey)
	raw := make([]byte, 4+len(plain))
	raw[0], raw[1], raw[2], raw[3] = 0xab, 0xcd, 0x01, seed
	for i, b := range plain {
		raw[4+i] = byte((int(b) - int(seed) + int(key[i%len(key)])) & 0xff)
	}
	got, err := DecryptH5Payload(raw)
	if err != nil {
		t.Fatalf("DecryptH5Payload error: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

func TestDecryptAndParseMRC(t *testing.T) {
	sample := "[ti:晴天]\n[1000,2500]故(1000,500)事(1500,600)里(2100,1400)"
	enc := encryptMRCTestHelper(sample)
	dec := DecryptMRC(enc)
	if !strings.Contains(dec, "[1000,2500]") {
		t.Fatalf("DecryptMRC failed, got %q", dec)
	}
	lines := ParseMRC(dec)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d (%+v)", len(lines), lines)
	}
	if !lines[0].Timed || lines[0].Text != "故事里" || len(lines[0].Words) != 3 {
		t.Fatalf("unexpected parsed line: %+v", lines[0])
	}
	if lines[0].At != time.Second || lines[0].Words[1].Start != 1500*time.Millisecond {
		t.Fatalf("unexpected word timings: %+v", lines[0].Words)
	}
}

func TestRewriteQualityURLAndTrialGuard(t *testing.T) {
	pq := "https://freetyst.nf.migu.cn/public/product9th/product45/2022/05/%E6%A0%87%E6%B8%85%E9%AB%98%E6%B8%85/MP3_128_16_Stero/demo.mp3?msisdn=1"
	hq := rewriteQualityURL(pq, specForTone("HQ", ""))
	if len(hq) == 0 || !strings.Contains(hq[0], "MP3_320_16_Stero") {
		t.Fatalf("unexpected HQ rewrite: %v", hq)
	}
	lq := rewriteQualityURL(pq, specForTone("LQ", ""))
	if len(lq) == 0 || !strings.Contains(lq[0], "MP3_64_16_Stero") {
		t.Fatalf("unexpected LQ rewrite: %v", lq)
	}
	sq := rewriteQualityURL(pq, specForTone("SQ", ""))
	if len(sq) == 0 || !strings.HasSuffix(strings.Split(sq[0], "?")[0], ".flac") {
		t.Fatalf("unexpected SQ rewrite: %v", sq)
	}
	zq24 := rewriteQualityURL(pq, specForTone("ZQ24", ""))
	if len(zq24) == 0 || !strings.Contains(zq24[0], "flac_24bit") {
		t.Fatalf("unexpected ZQ24 rewrite: %v", zq24)
	}
	zq32 := rewriteQualityURL(pq, specForTone("ZQ32", ""))
	if len(zq32) == 0 || !strings.Contains(zq32[0], "flac_32bit") {
		t.Fatalf("unexpected ZQ32 rewrite: %v", zq32)
	}
	legacyPQ := "https://freetyst.nf.migu.cn/public/product07/2018/%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/MP3_128_16_Stero/demo.mp3?k=1"
	if got := rewriteQualityURL(legacyPQ, specForTone("SQ", "")); len(got) == 0 || !strings.Contains(got[0], "%E6%AD%8C%E6%9B%B2%E4%B8%8B%E8%BD%BD/flac/demo.flac") {
		t.Fatalf("unexpected legacy SQ rewrite: %v", got)
	}

	// Regression: DecryptPCPayload decodes strategy/pc/listen/v2.0 hex ciphertext encrypted with ccTWaprX2aWmTIgA.
	plainPC := []byte(`{"code":"000000","info":"操作成功","data":{"url":"https://freetyst.nf.migu.cn/demo.flac","formatType":"SQ"}}`)
	encBytes := make([]byte, len(plainPC))
	for i, b := range plainPC {
		encBytes[i] = byte((int(b) + int(pcHexKey[i%len(pcHexKey)])) & 0xff)
	}
	decPC, err := DecryptPCPayload([]byte(strings.ToUpper(hex.EncodeToString(encBytes))))
	if err != nil || string(decPC) != string(plainPC) {
		t.Fatalf("DecryptPCPayload got=%q err=%v", decPC, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/trial.mp3":
			w.Header().Set("Content-Range", "bytes 0-63/960000")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(append([]byte("ID3\x03\x00"), make([]byte, 59)...))
		case "/full.flac":
			w.Header().Set("Content-Range", "bytes 0-63/28000000")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(append([]byte("fLaC\x00\x00"), make([]byte, 58)...))
		case "/full.m4a":
			w.Header().Set("Content-Range", "bytes 0-63/28000000")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(append([]byte("\x00\x00\x00\x20ftypM4A "), make([]byte, 52)...))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if _, ok := probeAudioURL(context.Background(), srv.URL+"/trial.mp3", "mp3", 270_000); ok {
		t.Fatal("expected 960KB clip for 270s song to be rejected as trial")
	}
	sz, ok := probeAudioURL(context.Background(), srv.URL+"/full.flac", "flac", 270_000)
	if !ok || sz != 28_000_000 {
		t.Fatalf("expected valid full FLAC probe, got ok=%v size=%d", ok, sz)
	}
	if _, ok := probeAudioURL(context.Background(), srv.URL+"/full.m4a", "m4a", 270_000); !ok {
		t.Fatal("expected valid full M4A probe")
	}
}

func TestScoreCandidateFiltering(t *testing.T) {
	want := netease.Track{
		Name:     "晴天",
		Artists:  []netease.Artist{{Name: "周杰伦"}},
		Duration: 269000,
	}
	exact := netease.Track{
		Name:     "晴天",
		Artists:  []netease.Artist{{Name: "周杰伦"}},
		Duration: 269500,
	}
	live := netease.Track{
		Name:     "晴天 (Live)",
		Artists:  []netease.Artist{{Name: "周杰伦"}},
		Duration: 270000,
	}
	wrongDur := netease.Track{
		Name:     "晴天",
		Artists:  []netease.Artist{{Name: "周杰伦"}},
		Duration: 60000,
	}
	if s, ok := scoreCandidate(want, exact); !ok || s < 150 {
		t.Fatalf("expected strong match, got ok=%v score=%d", ok, s)
	}
	if _, ok := scoreCandidate(want, live); ok {
		t.Fatal("live version should not match studio track")
	}
	if _, ok := scoreCandidate(want, wrongDur); ok {
		t.Fatal("60s clip duration should fail tolerance check")
	}
}

func TestMockCatalogSearchAndDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/music_search/v3/search/searchAll"):
			_, _ = w.Write([]byte(`{"code":"000000","songResultData":{"totalCount":1,"result":[{"contentId":"cid1","copyrightId":"cr1","songId":"s1","name":"晴天","duration":269000,"singers":[{"id":"112","name":"周杰伦"}],"albums":[{"id":"8592","name":"叶惠美"}],"imgItems":[{"imgSizeType":"03","img":"http://d.musicapp.migu.cn/cover.jpg"}]}]}}`))
		case strings.HasPrefix(r.URL.Path, "/bmw/search/suggest/v3.0"):
			_, _ = w.Write([]byte(`{"code":"000000","data":{"items":[{"text":{"text":"晴天"}},{"text":{"text":"晴天 周杰伦"}}]}}`))
		case strings.HasPrefix(r.URL.Path, "/MIGUM2.0/v1.0/content/resourceinfo.do"):
			_, _ = w.Write([]byte(`{"code":"000000","resource":[{"title":"精选歌单","musicListId":"p1","summary":"desc","musicNum":1,"ownerName":"migu"}]}`))
		case strings.HasPrefix(r.URL.Path, "/MIGUM2.0/v1.0/user/queryMusicListSongs.do"):
			_, _ = w.Write([]byte(`{"code":"000000","totalCount":1,"list":[{"contentId":"cid1","copyrightId":"cr1","songName":"晴天","singer":"周杰伦","singerId":"112","album":"叶惠美","albumId":"8592","length":"04:29"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldJ, oldC, oldU := jadeiteBase, appCBase, appUBase
	jadeiteBase, appCBase, appUBase = srv.URL, srv.URL, srv.URL
	defer func() { jadeiteBase, appCBase, appUBase = oldJ, oldC, oldU }()

	res, err := SearchPage("晴天", netease.SearchSongs, 10, 0)
	if err != nil || len(res.Songs) != 1 || res.Songs[0].Source != Source || res.Songs[0].ID >= 0 {
		t.Fatalf("unexpected search result: err=%v songs=%+v", err, res.Songs)
	}
	sug, err := SearchSuggest("晴")
	if err != nil || len(sug) != 2 {
		t.Fatalf("unexpected suggest: err=%v sug=%v", err, sug)
	}
	pl, err := PlaylistDetail("p1")
	if err != nil || pl.Name != "精选歌单" || len(pl.Tracks) != 1 || pl.Tracks[0].Duration != 269000 {
		t.Fatalf("unexpected playlist: err=%v pl=%+v", err, pl)
	}
}

func TestMiguLive(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("set LIVE=1 to test live Migu endpoints")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := SearchPageContext(ctx, "周杰伦 晴天", netease.SearchSongs, 5, 0)
	if err != nil || len(res.Songs) == 0 {
		t.Fatalf("live SearchPage failed: err=%v len=%d", err, len(res.Songs))
	}
	st, err := SongURLContext(ctx, res.Songs[0], "lossless")
	if err != nil || st == nil || st.URL == "" {
		t.Fatalf("live SongURL failed: err=%v st=%+v", err, st)
	}
	lines, _, err := Lyrics(ctx, res.Songs[0].SourceID, res.Songs[0].LrcURL, res.Songs[0].TrcURL, res.Songs[0].MrcURL)
	if err != nil || len(lines) == 0 {
		t.Fatalf("live Lyrics failed: err=%v lines=%d", err, len(lines))
	}
	stats, err := ProbeQuality(ctx)
	if err != nil {
		t.Fatalf("live ProbeQuality failed: %v", err)
	}
	t.Logf("live OK: stream=%s (%s, %d bytes), lyrics=%d lines (timed=%v), probe=%+v", st.Level, st.Type, st.Size, len(lines), lines[0].Timed, stats)
}

func TestQRAuthAndTokenExchange(t *testing.T) {
	defer SetCookie("")
	imgPayload := base64.StdEncoding.EncodeToString([]byte("FAKEJPEG"))
	pollStep := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/qrcWeb/qrcLogin"):
			_, _ = w.Write([]byte(`{"status":2000,"result":{"qrc_sessionid":"sid99","qrcUrl":"data:image/jpeg;base64,` + imgPayload + `"}}`))
		case strings.HasPrefix(r.URL.Path, "/api/qrcWeb/qrcquery"):
			pollStep++
			switch pollStep {
			case 1:
				_, _ = w.Write([]byte(`{"status":4074,"message":"未扫码","result":{}}`))
			case 2:
				_, _ = w.Write([]byte(`{"status":4073,"message":"已扫码","result":{}}`))
			default:
				_, _ = w.Write([]byte(`{"status":2000,"message":"成功","result":{"token":"ticket123","redirectURL":""}}`))
			}
		case strings.HasPrefix(r.URL.Path, "/user/h5/token-validate/v3.0"):
			w.Header().Set("pacmtoken", "pacm_abc")
			_, _ = w.Write([]byte(`{"code":"000000","data":{"uid":"u77","nickName":"小咪"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldPass, oldStrat := passportBase, strategyBase
	passportBase, strategyBase = srv.URL, srv.URL
	defer func() { passportBase, strategyBase = oldPass, oldStrat }()

	qrSess, err := StartQR(context.Background())
	if err != nil || qrSess.SessionID != "sid99" || string(qrSess.Image) != "FAKEJPEG" {
		t.Fatalf("unexpected StartQR: err=%v sess=%+v", err, qrSess)
	}

	st1, _, _, _, err := PollQR(context.Background(), qrSess.SessionID)
	if err != nil || st1 != QRWaiting {
		t.Fatalf("expected QRWaiting, got st=%v err=%v", st1, err)
	}
	st2, _, _, _, err := PollQR(context.Background(), qrSess.SessionID)
	if err != nil || st2 != QRScanned {
		t.Fatalf("expected QRScanned, got st=%v err=%v", st2, err)
	}
	st3, ticket, _, _, err := PollQR(context.Background(), qrSess.SessionID)
	if err != nil || st3 != QRDone || ticket != "ticket123" {
		t.Fatalf("expected QRDone+ticket, got st=%v ticket=%q err=%v", st3, ticket, err)
	}

	acc, err := ExchangeToken(context.Background(), ticket, "")
	if err != nil || acc.Token != "pacm_abc" || acc.Nickname != "小咪" || !strings.Contains(acc.Cookie, "pacmtoken=pacm_abc") {
		t.Fatalf("unexpected ExchangeToken result: err=%v acc=%+v", err, acc)
	}
}
