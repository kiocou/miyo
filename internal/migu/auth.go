package migu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

var passportBase = "https://passport.migu.cn"

const passportSourceID = "220029"

// QRStatus represents the live scan state of a Migu Passport QR code.
type QRStatus int

const (
	QRWaiting QRStatus = iota
	QRScanned
	QRExpired
	QRDone
	QRFailed
)

// QRSession holds the generated QR image bytes and passport session ID.
type QRSession struct {
	SessionID string
	Image     []byte
}

// Account summarizes an authenticated Migu session recovered from QR or callback login.
type Account struct {
	Cookie   string
	Token    string
	UID      string
	Nickname string
	Avatar   string
	VIP      bool
}

// StartQR requests a new Migu Passport Web QR login code (sourceid=220029).
func StartQR(ctx context.Context) (*QRSession, error) {
	u := passportBase + "/api/qrcWeb/qrcLogin?sourceid=" + passportSourceID
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", passportBase+"/login?sourceid="+passportSourceID)
	body, _, _, err := doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status  flexInt `json:"status"`
		Message string  `json:"message"`
		Result  struct {
			QrcURL       string `json:"qrcUrl"`
			QrcSessionID string `json:"qrc_sessionid"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("migu qr decode: %w", err)
	}
	if int(resp.Status) != 2000 || resp.Result.QrcSessionID == "" || resp.Result.QrcURL == "" {
		return nil, fmt.Errorf("migu qr failed (%d): %s", int(resp.Status), resp.Message)
	}
	raw := resp.Result.QrcURL
	if i := strings.Index(raw, "base64,"); i >= 0 {
		raw = raw[i+len("base64,"):]
	}
	imgBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("migu qr image decode: %w", err)
	}
	return &QRSession{
		SessionID: resp.Result.QrcSessionID,
		Image:     imgBytes,
	}, nil
}

// PollQR checks the current scan status of a QR session.
func PollQR(ctx context.Context, sessionID string) (status QRStatus, ticket, redirectURL, message string, err error) {
	q := url.Values{
		"sourceid":      {passportSourceID},
		"qrc_sessionid": {sessionID},
		"isAsync":       {"true"},
	}
	u := passportBase + "/api/qrcWeb/qrcquery?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return QRFailed, "", "", "", err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", passportBase+"/login?sourceid="+passportSourceID)
	body, _, _, err := doRequest(ctx, req)
	if err != nil {
		return QRFailed, "", "", "", err
	}
	var resp struct {
		Status  flexInt `json:"status"`
		Message string  `json:"message"`
		Result  struct {
			Token       string `json:"token"`
			UserToken   string `json:"userToken"`
			AuthToken   string `json:"authToken"`
			RedirectURL string `json:"redirectURL"`
			RedirectUrl string `json:"redirectUrl"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return QRFailed, "", "", "", fmt.Errorf("migu qr poll decode: %w", err)
	}
	tk := firstNonEmpty(resp.Result.Token, resp.Result.UserToken, resp.Result.AuthToken)
	redir := firstNonEmpty(resp.Result.RedirectURL, resp.Result.RedirectUrl)
	switch int(resp.Status) {
	case 4074:
		return QRWaiting, "", "", resp.Message, nil
	case 4073:
		return QRScanned, "", "", resp.Message, nil
	case 4072:
		return QRExpired, "", "", resp.Message, nil
	case 2000:
		return QRDone, tk, redir, resp.Message, nil
	default:
		return QRFailed, "", "", resp.Message, nil
	}
}

// BrowserLoginURL builds the official Migu Passport web login page (supporting SMS
// verification code, password, and QR scan) with a redirect target callbackURL.
func BrowserLoginURL(callbackURL string) string {
	q := url.Values{
		"sourceid":        {passportSourceID},
		"apptype":         {"0"},
		"forceAuthn":      {"false"},
		"isPassive":       {"false"},
		"authType":        {"MiguPassport"},
		"passwordControl": {"0"},
		"display":         {"web"},
		"logintype":       {"1"},
		"callbackURL":     {callbackURL},
		"hideqrc":         {"0"},
	}
	return passportBase + "/login?" + q.Encode()
}

// ExchangeToken trades a Passport login ticket for Migu Music pacmtoken and account info.
func ExchangeToken(ctx context.Context, ticket, redirectURL string) (*Account, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" && strings.TrimSpace(redirectURL) == "" {
		return nil, errors.New("migu: empty login ticket")
	}
	jar, _ := cookiejar.New(nil)
	var hopTokens []string
	cli := &http.Client{
		Timeout: 10 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return http.ErrUseLastResponse
			}
			q := req.URL.Query()
			if t := firstNonEmpty(q.Get("token"), q.Get("authToken"), q.Get("ticket")); t != "" {
				hopTokens = append(hopTokens, t)
			}
			return nil
		},
	}

	// Step 1: validate the passport ticket directly first so a single-use ticket
	// is not consumed prematurely by an SSO redirect.
	if ticket != "" {
		for _, sid := range []string{passportSourceID, "220024"} {
			if acc, ok := validateTokenV3(ctx, cli, jar, ticket, sid); ok {
				return enrichAccount(ctx, acc), nil
			}
		}
		if acc, ok := validateTokenSupport(ctx, cli, jar, ticket); ok {
			return enrichAccount(ctx, acc), nil
		}
	}

	// Step 2: follow redirectURL if provided (some passport flows issue a fresh
	// callback token or aversionid cookie on redirect).
	if redirectURL != "" {
		sep := "?"
		if strings.Contains(redirectURL, "?") {
			sep = "&"
		}
		ssoURL := redirectURL + sep + url.Values{
			"callbackURL": {webReferer},
			"token":       {ticket},
			"qrclogin":    {"1"},
			"logintype":   {"QRCSSO"},
		}.Encode()
		if sreq, err := http.NewRequestWithContext(ctx, "GET", ssoURL, nil); err == nil {
			sreq.Header.Set("User-Agent", webUA)
			if sresp, err := cli.Do(sreq); err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(sresp.Body, 256<<10))
				sresp.Body.Close()
			}
		}
		for _, ht := range hopTokens {
			if ht == ticket {
				continue
			}
			for _, sid := range []string{passportSourceID, "220024"} {
				if acc, ok := validateTokenV3(ctx, cli, jar, ht, sid); ok {
					return enrichAccount(ctx, acc), nil
				}
			}
		}
		// Fallback to any cookies set on .migu.cn during SSO redirect.
		if ck, tok, uid := collectJarCookies(jar); tok != "" || strings.Contains(ck, "aversionid=") {
			return enrichAccount(ctx, &Account{Cookie: ck, Token: tok, UID: uid}), nil
		}
	}

	return nil, errors.New("未换取到有效咪咕会话凭据，请重试或改用浏览器验证码登录")
}

func validateTokenV3(ctx context.Context, cli *http.Client, jar http.CookieJar, ticket, sourceID string) (*Account, bool) {
	q := url.Values{
		"token":    {ticket},
		"type":     {"2"},
		"sourceId": {sourceID},
	}
	valURL := strategyBase + "/user/h5/token-validate/v3.0?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", valURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", webReferer)
	req.Header.Set("deviceId", deviceMS)

	resp, err := cli.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var vresp struct {
		Code string `json:"code"`
		Data struct {
			UID       string `json:"uid"`
			UserID    string `json:"userId"`
			NickName  string `json:"nickName"`
			Nickname  string `json:"nickname"`
			Mobile    string `json:"msisdn"`
			SmallIcon string `json:"smallIcon"`
			Avatar    string `json:"avatar"`
			PacmToken string `json:"pacmtoken"`
			Token     string `json:"token"`
			PassID    string `json:"passId"`
			IsVIP     bool   `json:"isVip"`
			Member    string `json:"memberLevel"`
			UserInfo  struct {
				UID       string `json:"userId"`
				NickName  string `json:"nickName"`
				Mobile    string `json:"msisdn"`
				SmallIcon string `json:"smallIcon"`
				Avatar    string `json:"avatar"`
			} `json:"userInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vresp); err != nil || vresp.Code != "000000" {
		return nil, false
	}
	jarCookie, jarTok, jarUID := collectJarCookies(jar)
	token := firstNonEmpty(resp.Header.Get("pacmtoken"), vresp.Data.PacmToken, vresp.Data.Token, jarTok)
	uid := firstNonEmpty(vresp.Data.UID, vresp.Data.UserID, vresp.Data.UserInfo.UID, vresp.Data.PassID, jarUID)
	nick := firstNonEmpty(vresp.Data.NickName, vresp.Data.Nickname, vresp.Data.UserInfo.NickName, vresp.Data.Mobile, vresp.Data.UserInfo.Mobile)
	avatar := httpsURL(firstNonEmpty(vresp.Data.SmallIcon, vresp.Data.Avatar, vresp.Data.UserInfo.SmallIcon, vresp.Data.UserInfo.Avatar))
	if token == "" && !strings.Contains(jarCookie, "aversionid=") {
		return nil, false
	}
	return &Account{
		Cookie:   buildCookieString(token, uid, jarCookie),
		Token:    token,
		UID:      uid,
		Nickname: nick,
		Avatar:   avatar,
		VIP:      vresp.Data.IsVIP || (vresp.Data.Member != "" && vresp.Data.Member != "0"),
	}, true
}

func validateTokenSupport(ctx context.Context, cli *http.Client, jar http.CookieJar, ticket string) (*Account, bool) {
	valURL := strategyBase + "/user/h5/support/token-validate/v1.0?token=" + url.QueryEscape(ticket)
	req, err := http.NewRequestWithContext(ctx, "GET", valURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", webReferer)
	req.Header.Set("deviceId", deviceMS)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var vresp struct {
		Code string `json:"code"`
		Data struct {
			UID       string `json:"uid"`
			UserID    string `json:"userId"`
			NickName  string `json:"nickName"`
			PacmToken string `json:"pacmtoken"`
			Token     string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vresp); err != nil || vresp.Code != "000000" {
		return nil, false
	}
	jarCookie, jarTok, jarUID := collectJarCookies(jar)
	token := firstNonEmpty(resp.Header.Get("pacmtoken"), vresp.Data.PacmToken, vresp.Data.Token, jarTok)
	uid := firstNonEmpty(vresp.Data.UID, vresp.Data.UserID, jarUID)
	if token == "" {
		return nil, false
	}
	return &Account{
		Cookie:   buildCookieString(token, uid, jarCookie),
		Token:    token,
		UID:      uid,
		Nickname: vresp.Data.NickName,
	}, true
}

func collectJarCookies(jar http.CookieJar) (cookieStr, token, uid string) {
	cookies := map[string]string{}
	for _, rawDomain := range []string{strategyBase, webReferer, passportBase} {
		if u, err := url.Parse(rawDomain); err == nil {
			for _, ck := range jar.Cookies(u) {
				if ck.Value != "" {
					cookies[ck.Name] = ck.Value
				}
			}
		}
	}
	token = cookies["pacmtoken"]
	uid = firstNonEmpty(cookies["uid"], cookies["userId"])
	var parts []string
	for _, k := range []string{"pacmtoken", "uid", "aversionid"} {
		if v := cookies[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; "), token, uid
}

func buildCookieString(token, uid, jarCookie string) string {
	var parts []string
	if token != "" {
		parts = append(parts, "pacmtoken="+token)
	}
	if uid != "" {
		parts = append(parts, "uid="+uid)
	}
	for _, part := range strings.Split(jarCookie, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "aversionid") && strings.TrimSpace(v) != "" {
			parts = append(parts, "aversionid="+strings.TrimSpace(v))
			break
		}
	}
	return strings.Join(parts, "; ")
}

func enrichAccount(ctx context.Context, acc *Account) *Account {
	if acc == nil || (acc.Nickname != "" && acc.Avatar != "") {
		return acc
	}
	SetCookie(acc.Cookie)
	infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if info, err := UserInfo(infoCtx); err == nil && info != nil {
		if acc.Nickname == "" {
			acc.Nickname = firstNonEmpty(info.Nickname, acc.UID)
		}
		if acc.Avatar == "" {
			acc.Avatar = info.Avatar
		}
		acc.VIP = acc.VIP || info.VIP
	}
	return acc
}

// UserInfo fetches the signed-in Migu user's profile using currently configured credentials.
func UserInfo(ctx context.Context) (*Account, error) {
	cookie, av, token, uid := authSnapshot()
	if token == "" && cookie == "" {
		return nil, errors.New("migu: not signed in")
	}
	headers := map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
	}
	if token != "" {
		headers["pacmtoken"] = token
	}
	if uid != "" {
		headers["uid"] = uid
	}
	if av != "" {
		headers["aversionid"] = av
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			UID       string `json:"uid"`
			UserID    string `json:"userId"`
			NickName  string `json:"nickName"`
			Nickname  string `json:"nickname"`
			Mobile    string `json:"mobile"`
			Msisdn    string `json:"msisdn"`
			SmallIcon string `json:"smallIcon"`
			Avatar    string `json:"avatar"`
			IconURL   string `json:"iconUrl"`
			IsVIP     bool   `json:"isVip"`
			Member    string `json:"memberLevel"`
			UserInfo  struct {
				UID       string `json:"userId"`
				NickName  string `json:"nickName"`
				Mobile    string `json:"msisdn"`
				SmallIcon string `json:"smallIcon"`
				Avatar    string `json:"avatar"`
			} `json:"userInfo"`
		} `json:"data"`
	}
	if err := getJSON(ctx, strategyBase+"/user/h5/user-info/v1.0", headers, &resp); err == nil && resp.Code == "000000" {
		return &Account{
			Cookie:   cookie,
			Token:    token,
			UID:      firstNonEmpty(resp.Data.UID, resp.Data.UserID, resp.Data.UserInfo.UID, uid),
			Nickname: firstNonEmpty(resp.Data.NickName, resp.Data.Nickname, resp.Data.UserInfo.NickName, resp.Data.Mobile, resp.Data.Msisdn, resp.Data.UserInfo.Mobile),
			Avatar:   httpsURL(firstNonEmpty(resp.Data.SmallIcon, resp.Data.Avatar, resp.Data.IconURL, resp.Data.UserInfo.SmallIcon, resp.Data.UserInfo.Avatar)),
			VIP:      resp.Data.IsVIP || (resp.Data.Member != "" && resp.Data.Member != "0"),
		}, nil
	}
	if uid != "" {
		var hdrResp struct {
			Code string `json:"code"`
			Info string `json:"info"`
			Data struct {
				UserInfoItem struct {
					UserID     string `json:"userId"`
					NickName   string `json:"nickName"`
					SmallIcon  string `json:"smallIcon"`
					MiddleIcon string `json:"middleIcon"`
					BigIcon    string `json:"bigIcon"`
				} `json:"userInfoItem"`
			} `json:"data"`
		}
		if err := getJSON(ctx, appCBase+"/MIGUM3.0/user/api/my-page-header/v2.0?userId="+url.QueryEscape(uid), headers, &hdrResp); err == nil && hdrResp.Code == "000000" {
			u := hdrResp.Data.UserInfoItem
			return &Account{
				Cookie:   cookie,
				Token:    token,
				UID:      firstNonEmpty(u.UserID, uid),
				Nickname: u.NickName,
				Avatar:   httpsURL(firstNonEmpty(u.BigIcon, u.MiddleIcon, u.SmallIcon)),
			}, nil
		}
	}
	if resp.Code != "" && resp.Code != "000000" {
		return nil, checkCode(resp.Code, resp.Info)
	}
	return nil, errors.New("migu user-info: unavailable")
}
