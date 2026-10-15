package antigravity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

type OAuthSession struct {
	ID        string    `json:"id"`
	URL       string    `json:"url,omitempty"`
	Status    string    `json:"status"`
	Email     string    `json:"email,omitempty"`
	Error     string    `json:"error,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type oauthPending struct {
	view    OAuthSession
	server  *http.Server
	timer   *time.Timer
	claimed bool
}

func randomURLValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *Manager) StartOAuth() (OAuthSession, error) {
	m.mu.Lock()
	config := m.state.OAuth
	m.mu.Unlock()
	if config.ClientID == "" {
		return OAuthSession{}, fmt.Errorf("请先配置自己的 Google OAuth 桌面客户端；也可直接导入已登录的 Antigravity 账号")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return OAuthSession{}, fmt.Errorf("无法启动本机 OAuth 回调服务")
	}
	id, err := randomURLValue()
	if err != nil {
		listener.Close()
		return OAuthSession{}, err
	}
	state, err := randomURLValue()
	if err != nil {
		listener.Close()
		return OAuthSession{}, err
	}
	verifier, err := randomURLValue()
	if err != nil {
		listener.Close()
		return OAuthSession{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	redirect := "http://" + listener.Addr().String() + "/callback"
	values := url.Values{"client_id": {config.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "access_type": {"offline"}, "prompt": {"consent select_account"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "scope": {"openid https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs https://www.googleapis.com/auth/aicode"}}
	view := OAuthSession{ID: id, URL: "https://accounts.google.com/o/oauth2/v2/auth?" + values.Encode(), Status: "waiting", ExpiresAt: time.Now().Add(10 * time.Minute)}
	pending := &oauthPending{view: view}
	m.oauthMu.Lock()
	// Only one active browser login. Abandoned callbacks are closed immediately.
	for _, old := range m.oauthSessions {
		if old.view.Status == "waiting" {
			old.view.Status = "cancelled"
			old.timer.Stop()
			_ = old.server.Close()
		}
	}
	if len(m.oauthSessions) > 20 {
		for key, old := range m.oauthSessions {
			if old.view.Status != "waiting" {
				delete(m.oauthSessions, key)
			}
		}
	}
	m.oauthSessions[id] = pending
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Method != "GET" || r.URL.Path != "/callback" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "OAuth 回调无效", http.StatusBadRequest)
			return
		}
		m.oauthMu.Lock()
		if pending.claimed || pending.view.Status != "waiting" {
			m.oauthMu.Unlock()
			http.Error(w, "登录已结束，请返回 AppForce", http.StatusGone)
			return
		}
		pending.claimed = true
		m.oauthMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		code := r.URL.Query().Get("code")
		var loginErr error
		var account AccountView
		if code == "" || len(code) > 8192 {
			loginErr = fmt.Errorf("Google 登录未完成或已被取消")
		} else {
			form := url.Values{"grant_type": {"authorization_code"}, "client_id": {config.ClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
			if config.ClientSecret != "" {
				form.Set("client_secret", config.ClientSecret)
			}
			token, e := exchange(ctx, m.client(), form)
			loginErr = e
			if e == nil {
				account, loginErr = m.importCredential(ctx, "", Credential{Token: token, AuthMethod: "consumer"}, &config)
			}
		}
		m.oauthMu.Lock()
		pending.timer.Stop()
		if loginErr != nil {
			pending.view.Status = "failed"
			pending.view.Error = loginErr.Error()
		} else {
			pending.view.Status = "succeeded"
			pending.view.Email = account.Email
		}
		m.oauthMu.Unlock()
		if loginErr != nil {
			_, _ = fmt.Fprint(w, "登录失败，请返回 AppForce 查看原因。")
		} else {
			_, _ = fmt.Fprint(w, "账号已保存，请返回 AppForce。可以关闭此窗口。")
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = pending.server.Shutdown(ctx)
		}()
	})
	pending.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	pending.timer = time.AfterFunc(10*time.Minute, func() {
		m.oauthMu.Lock()
		if pending.view.Status == "waiting" {
			pending.view.Status = "expired"
		}
		m.oauthMu.Unlock()
		_ = pending.server.Close()
	})
	m.oauthMu.Unlock()
	go func() { _ = pending.server.Serve(listener) }()
	return view, nil
}

func (m *Manager) OAuthStatus(id string) (OAuthSession, error) {
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	if pending := m.oauthSessions[id]; pending != nil {
		view := pending.view
		view.URL = ""
		return view, nil
	}
	return OAuthSession{}, fmt.Errorf("登录会话不存在或 AppForce 已重启")
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() { m.collectorCancel(); <-m.collectorDone })
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	for _, session := range m.oauthSessions {
		session.timer.Stop()
		_ = session.server.Close()
	}
}
