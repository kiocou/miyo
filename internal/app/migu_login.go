package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/migu"
)

const (
	miguModeQR = iota
	miguModeBrowser
	miguModeCookie
)

type miguLoginState struct {
	mode     int
	gen      int
	qrBmp    *ui.Bitmap
	qrStatus migu.QRStatus
	qrCancel context.CancelFunc
	err      string
	busy     bool
	finished bool
	nickname string

	browserURL    string
	browserCancel context.CancelFunc

	cookieText string
	cookieErr  string
}

var (
	miguStartQR       = migu.StartQR
	miguPollQR        = migu.PollQR
	miguExchangeToken = migu.ExchangeToken
	miguUserInfo      = migu.UserInfo
)

// openMiguLogin opens the unified login dialog on the Migu Music tab.
func (a *app) openMiguLogin() {
	a.setAudioSource(migu.Source)
	a.openLoginFor(migu.Source)
	// When clicking "重新登录…" / "切换账号…" from settings while already signed in,
	// go straight to the QR view so switching accounts takes one step.
	if migu.HasCookie() {
		a.startMiguQR()
	}
}

func (a *app) refreshMiguProfile() {
	if !migu.HasCookie() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		acc, err := miguUserInfo(ctx)
		cancel()
		if err != nil || acc == nil {
			return
		}
		a.post(func() {
			if !migu.HasCookie() {
				return
			}
			changed := false
			if acc.Nickname != "" && acc.Nickname != a.settings.MiguUser {
				a.settings.MiguUser = acc.Nickname
				changed = true
			}
			if acc.Avatar != "" && acc.Avatar != a.settings.MiguAvatar {
				a.settings.MiguAvatar = acc.Avatar
				changed = true
			}
			if changed {
				a.settings.save()
				a.redraw()
			}
		})
	}()
}

func (a *app) stopMiguLoginTasks() {
	a.stopMiguQRPoll()
	a.stopMiguBrowserLogin()
	a.migu.login.gen++
}

func (a *app) stopMiguQRPoll() {
	if a.migu.login.qrCancel != nil {
		a.migu.login.qrCancel()
		a.migu.login.qrCancel = nil
	}
}

func (a *app) signOutMigu() {
	a.stopMiguLoginTasks()
	a.settings.MiguCookie = ""
	a.settings.MiguUser = ""
	a.settings.MiguAvatar = ""
	a.migu.cookieDraft = ""
	a.applyMiguSettings()
	a.say("migu", "已退出咪咕账号")
	if a.isMiguSource() {
		a.reloadActiveSource()
	}
	a.changed()
}

func (a *app) startMiguQR() {
	a.stopMiguQRPoll()
	a.stopMiguBrowserLogin()
	l := &a.migu.login
	l.gen++
	gen := l.gen
	ctx, cancel := context.WithCancel(context.Background())
	l.mode = miguModeQR
	l.qrBmp = nil
	l.qrStatus = migu.QRWaiting
	l.qrCancel = cancel
	l.err = ""
	l.busy = false
	l.finished = false
	l.cookieText = a.settings.MiguCookie

	go func() {
		reqCtx, reqCancel := context.WithTimeout(ctx, 12*time.Second)
		sess, err := miguStartQR(reqCtx)
		reqCancel()

		var img image.Image
		if err == nil && sess != nil {
			img, _, err = image.Decode(bytes.NewReader(sess.Image))
		}
		a.post(func() {
			if gen != a.migu.login.gen || !a.login.open {
				return
			}
			if err != nil {
				a.migu.login.err = "二维码获取失败：" + err.Error()
				return
			}
			a.migu.login.qrBmp = ui.NewBitmap(img)
			a.pollMiguQR(ctx, gen, sess.SessionID)
		})
	}()
}

// pollMiguQR polls Migu's QR endpoint until scanned & confirmed, expired, or cancelled.
// Termination is decided directly on this goroutine (never reading unsynchronized state
// mutated asynchronously in a.post), so QRDone always advances into ExchangeToken.
func (a *app) pollMiguQR(ctx context.Context, gen int, sessionID string) {
	go func() {
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				return
			}
			pollCtx, pollCancel := context.WithTimeout(ctx, 8*time.Second)
			st, ticket, redir, msg, err := miguPollQR(pollCtx, sessionID)
			pollCancel()

			if ctx.Err() != nil {
				return
			}
			if err != nil {
				a.post(func() {
					if gen == a.migu.login.gen && a.login.open {
						a.migu.login.busy = false
						a.migu.login.err = "轮询扫码状态失败，请刷新重试"
					}
				})
				return
			}

			switch st {
			case migu.QRWaiting, migu.QRScanned:
				cur := st
				a.post(func() {
					if gen == a.migu.login.gen && a.login.open {
						a.migu.login.qrStatus = cur
					}
				})
				select {
				case <-ctx.Done():
					return
				case <-time.After(1200 * time.Millisecond):
				}
			case migu.QRExpired:
				a.post(func() {
					if gen == a.migu.login.gen && a.login.open {
						a.migu.login.busy = false
						a.migu.login.qrStatus = migu.QRExpired
					}
				})
				return
			case migu.QRDone:
				if strings.TrimSpace(ticket) == "" && strings.TrimSpace(redir) == "" {
					a.post(func() {
						if gen == a.migu.login.gen && a.login.open {
							a.migu.login.busy = false
							a.migu.login.err = "扫码已确认，但未收到登录票据，请刷新重试"
						}
					})
					return
				}
				a.post(func() {
					if gen == a.migu.login.gen && a.login.open {
						a.migu.login.qrStatus = migu.QRDone
						a.migu.login.busy = true
						a.migu.login.err = ""
					}
				})
				exCtx, exCancel := context.WithTimeout(ctx, 15*time.Second)
				acc, exErr := miguExchangeToken(exCtx, ticket, redir)
				exCancel()
				a.post(func() {
					if gen != a.migu.login.gen || !a.login.open {
						return
					}
					a.migu.login.busy = false
					if exErr != nil {
						a.migu.login.err = "换取凭据失败：" + exErr.Error()
						return
					}
					a.finishMiguLogin(acc)
				})
				return
			default:
				m := msg
				a.post(func() {
					if gen == a.migu.login.gen && a.login.open {
						a.migu.login.busy = false
						if m != "" {
							a.migu.login.err = m
						} else {
							a.migu.login.err = "扫码登录失败，请刷新重试"
						}
					}
				})
				return
			}
		}
		a.post(func() {
			if gen == a.migu.login.gen && a.login.open {
				a.migu.login.busy = false
				a.migu.login.qrStatus = migu.QRExpired
			}
		})
	}()
}

func (a *app) finishMiguLogin(acc *migu.Account) {
	if acc == nil || acc.Cookie == "" {
		a.migu.login.busy = false
		a.migu.login.err = "未获取到有效凭据"
		return
	}
	a.stopMiguQRPoll()
	a.stopMiguBrowserLogin()
	a.settings.MiguCookie = acc.Cookie
	a.settings.MiguUser = firstNonEmptyStr(acc.Nickname, acc.UID)
	if acc.Avatar != "" {
		a.settings.MiguAvatar = acc.Avatar
	}
	a.migu.cookieDraft = acc.Cookie
	a.applyMiguSettings()
	a.migu.login.busy = false
	a.migu.login.finished = true
	a.migu.login.nickname = a.settings.MiguUser
	a.migu.login.err = ""
	a.say("migu", "咪咕音乐登录成功")
	if a.isMiguSource() {
		a.reloadActiveSource()
	}
	a.changed()
	gen := a.migu.login.gen
	time.AfterFunc(1200*time.Millisecond, func() {
		a.post(func() {
			if gen == a.migu.login.gen && a.migu.login.finished {
				a.login.open = false
			}
		})
	})
}

func (a *app) stopMiguBrowserLogin() {
	if a.migu.login.browserCancel != nil {
		a.migu.login.browserCancel()
		a.migu.login.browserCancel = nil
	}
}

func (a *app) startMiguBrowserLogin() {
	a.stopMiguQRPoll()
	a.stopMiguBrowserLogin()
	l := &a.migu.login
	l.mode = miguModeBrowser
	l.gen++
	gen := l.gen
	l.err = ""
	l.busy = false
	l.finished = false

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		l.err = "无法启动本地回调服务：" + err.Error()
		return
	}
	addr := ln.Addr().(*net.TCPAddr)
	cbURL := fmt.Sprintf("http://127.0.0.1:%d/cb", addr.Port)
	loginURL := migu.BrowserLoginURL(cbURL)
	l.browserURL = loginURL

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	l.browserCancel = cancel

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ticket := firstNonEmptyStr(q.Get("token"), q.Get("authToken"), q.Get("ticket"), q.Get("SAMLart"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if ticket == "" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><meta charset="utf-8"><body><script>
if (location.hash && location.hash.length > 1) {
  location.replace('/cb?' + location.hash.substring(1).replace(/^#/, ''));
} else {
  document.body.innerHTML = '<div style="font-family:sans-serif;text-align:center;padding:48px"><h3>未检测到登录票据</h3><p>如果官方页面限制了本地回跳，请返回播放器改用「扫码登录」。</p></div>';
}
</script></body></html>`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><meta charset="utf-8"><body style="font-family:sans-serif;text-align:center;padding:48px"><h2>咪咕音乐授权成功</h2><p>可以关闭此浏览器标签页并回到 迷一会儿（Miyo）。</p></body></html>`)

		go func() {
			exCtx, exCancel := context.WithTimeout(context.Background(), 15*time.Second)
			acc, exErr := miguExchangeToken(exCtx, ticket, "")
			exCancel()
			a.post(func() {
				if gen != a.migu.login.gen {
					return
				}
				if exErr != nil {
					a.migu.login.err = "浏览器授权换取失败：" + exErr.Error()
					return
				}
				a.finishMiguLogin(acc)
			})
			_ = srv.Close()
		}()
	})

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		_ = srv.Serve(ln)
	}()

	if openErr := openURL(loginURL); openErr != nil {
		l.err = "无法自动打开浏览器：" + openErr.Error()
	}
}

func (a *app) importMiguCookie() {
	l := &a.migu.login
	raw := strings.TrimSpace(l.cookieText)
	if l.busy || raw == "" {
		return
	}
	l.busy, l.cookieErr = true, ""
	gen := l.gen
	go func() {
		migu.SetCookie(raw)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		acc, err := miguUserInfo(ctx)
		cancel()
		a.post(func() {
			if gen != a.migu.login.gen {
				return
			}
			l.busy = false
			if acc == nil {
				acc = &migu.Account{Cookie: raw}
			} else {
				acc.Cookie = raw
			}
			if err != nil {
				a.finishMiguLogin(acc)
				return
			}
			a.finishMiguLogin(acc)
		})
	}()
}

// miguLoginBody renders the Migu tab inside the unified loginDialog.
func (a *app) miguLoginBody(c *ui.Context, panel ui.Element) {
	p := a.pal
	l := &a.migu.login
	if migu.HasCookie() && !l.finished && l.mode == miguModeQR && l.qrBmp == nil && !l.busy && l.err == "" {
		name := a.settings.MiguUser
		if name == "" {
			name = "咪咕音乐用户"
		}
		ui.Text(c, "已登录咪咕音乐").FontSize(19).FontWeight(700)
		ui.Text(c, name).FontSize(13).TextColor(p.muted).Margin(0, 0, 14, 0)
		ui.Row(c).Gap(10).Children(func() {
			if pillButton(c.Key("migu-use-now"), iconCheck, "使用咪咕音源", p.accent, p.onAccent).Clicked() {
				a.setAudioSource(migu.Source)
				a.login.open = false
			}
			if pillButton(c.Key("migu-relogin-qr"), iconRepeat, "重新扫码", p.hover.Over(p.bg), p.text).Clicked() {
				a.startMiguQR()
			}
		})
		if textLink(c.Key("migu-dlg-logout"), "退出咪咕登录", dangerColor).Margin(14, 0, 0, 0).Clicked() {
			a.signOutMigu()
			a.startMiguQR()
		}
		return
	}
	switch {
	case l.finished:
		ui.Text(c, "登录成功").FontSize(20).FontWeight(700)
		name := l.nickname
		if name == "" {
			name = "咪咕音乐用户"
		}
		ui.Text(c, "欢迎，"+name).FontSize(13).TextColor(p.muted).Margin(0, 0, 14, 0)
		if primaryButton(c.Key("migu-login-done"), "完成", nil, p, false).Clicked() {
			a.login.open = false
		}
	case l.mode == miguModeCookie:
		a.miguCookieLoginView(c, panel)
	case l.mode == miguModeBrowser:
		a.miguBrowserLoginView(c)
	default:
		a.miguQRLoginView(c)
	}
}

func (a *app) miguQRLoginView(c *ui.Context) {
	p := a.pal
	l := &a.migu.login
	title, sub := "扫码登录咪咕音乐", "打开咪咕音乐 App，扫一扫授权登录"
	switch {
	case l.busy:
		title, sub = "正在登录", "正在换取音频会话凭据…"
	case l.err != "":
		sub = l.err
	case l.qrStatus == migu.QRScanned:
		title, sub = "扫描成功", "请在手机上点击确认登录"
	case l.qrStatus == migu.QRExpired:
		sub = "二维码已失效，请刷新"
	}
	ui.Text(c, title).FontSize(19).FontWeight(700)
	ui.Text(c, sub).FontSize(13).TextColor(p.muted).TextAlign(ui.Center).Margin(0, 0, 14, 0)

	ui.Box(c).Size(212, 212).Radius(14).Background(ui.RGB(255, 255, 255)).Border(1, p.hairline).Center().Children(func() {
		if l.qrBmp != nil {
			opacity := float32(1)
			if l.qrStatus == migu.QRScanned || l.qrStatus == migu.QRExpired || l.busy || l.err != "" {
				opacity = 0.14
			}
			ui.Image(c, l.qrBmp).Size(192, 192).Radius(8).Opacity(opacity)
		} else if l.err == "" {
			s := shimmer{base: ui.RGBA(0, 0, 0, 0.08), sheen: ui.RGBA(0, 0, 0, 0.03)}.in(c)
			qrSkeleton(c, s, 14).Absolute().Top(0).Left(0).Right(0).Bottom(0)
		}
		if l.qrStatus == migu.QRExpired || l.err != "" {
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
				if pillButton(c.Key("migu-qr-refresh"), iconRepeat, "刷新二维码", p.accent, p.onAccent).Clicked() {
					a.startMiguQR()
				}
			})
		}
	})

	ui.Row(c).Gap(14).Margin(16, 0, 0, 0).AlignItems(ui.Center).Children(func() {
		if textLink(c.Key("migu-to-browser"), "使用验证码 / 浏览器登录", p.muted).Clicked() {
			a.startMiguBrowserLogin()
		}
		ui.Text(c, "·").FontSize(12).TextColor(p.faint)
		if textLink(c.Key("migu-to-cookie"), "手动 Cookie", p.muted).Clicked() {
			a.stopMiguQRPoll()
			a.stopMiguBrowserLogin()
			l.mode, l.cookieErr = miguModeCookie, ""
		}
	})
}

func (a *app) miguBrowserLoginView(c *ui.Context) {
	p := a.pal
	l := &a.migu.login
	ui.Text(c, "浏览器授权登录").FontSize(19).FontWeight(700)
	msg := "已在默认浏览器打开咪咕通行证页面，请使用短信验证码、密码或扫码完成登录，完成后会自动返回。"
	if l.err != "" {
		msg = l.err
	}
	ui.Text(c, msg).FontSize(12.5).TextColor(p.muted).LineHeight(1.45).TextAlign(ui.Center).Margin(6, 0, 14, 0)
	ui.Row(c).Gap(10).Children(func() {
		if pillButton(c.Key("migu-reopen-browser"), iconExternal, "重新打开浏览器", p.accent, p.onAccent).Clicked() {
			if l.browserURL != "" {
				_ = openURL(l.browserURL)
			} else {
				a.startMiguBrowserLogin()
			}
		}
		if l.browserURL != "" {
			if pillButton(c.Key("migu-copy-url"), iconCopy, "复制链接", p.hover.Over(p.bg), p.text).Clicked() {
				_ = copyText(l.browserURL)
				a.toast = "已复制登录链接"
			}
		}
	})
	ui.Row(c).Gap(14).Margin(16, 0, 0, 0).AlignItems(ui.Center).Children(func() {
		if textLink(c.Key("migu-browser-to-qr"), "返回扫码登录", p.muted).Clicked() {
			a.startMiguQR()
		}
		ui.Text(c, "·").FontSize(12).TextColor(p.faint)
		if textLink(c.Key("migu-browser-to-cookie"), "手动 Cookie", p.muted).Clicked() {
			a.stopMiguBrowserLogin()
			l.mode, l.cookieErr = miguModeCookie, ""
		}
	})
}

func (a *app) miguCookieLoginView(c *ui.Context, panel ui.Element) {
	p := a.pal
	l := &a.migu.login
	panel.AlignItems(ui.Stretch)
	ui.Text(c, "咪咕 Cookie / Token").FontSize(19).FontWeight(700).TextAlign(ui.Center)
	ui.Text(c, "填入在 music.migu.cn 获取到的完整 Cookie 或 pacmtoken").
		FontSize(12.5).TextColor(p.muted).LineHeight(1.45).TextAlign(ui.Center).Margin(0, 0, 12, 0)
	in := ui.TextInput(c.Key("migu-cookie-dlg"), &l.cookieText).Placeholder("pacmtoken=... 或完整 Cookie").Label("咪咕凭据").
		Password().FontSize(13).AutoFocus().Disabled(l.busy)
	if in.Submitted() {
		a.importMiguCookie()
	}
	if l.cookieErr != "" {
		ui.Text(c, l.cookieErr).FontSize(12).TextColor(dangerColor).LineHeight(1.4).Margin(4, 0, 0, 0)
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Margin(16, 0, 0, 0).Children(func() {
		if textLink(c.Key("migu-cookie-to-qr"), "返回扫码登录", p.muted).Clicked() {
			a.startMiguQR()
		}
		ui.Box(c).Grow(1)
		label := "保存"
		if l.busy {
			label = "正在保存…"
		}
		if primaryButton(c.Key("migu-cookie-ok"), label, nil, p, l.busy || strings.TrimSpace(l.cookieText) == "").Clicked() {
			a.importMiguCookie()
		}
	})
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
