package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"appforce/internal/antigravity"
	"appforce/internal/app"
)

func accountRoutes(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/antigravity/accounts", func(w http.ResponseWriter, r *http.Request) { send(w, a.Accounts.List()) })
	mux.HandleFunc("POST /api/antigravity/accounts", func(w http.ResponseWriter, r *http.Request) {
		var body antigravity.ImportRequest
		if !read(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		account, err := a.Accounts.Import(ctx, body)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, account)
	})
	mux.HandleFunc("DELETE /api/antigravity/accounts/{id}", func(w http.ResponseWriter, r *http.Request) { result(w, a.Accounts.Delete(r.PathValue("id"))) })
	mux.HandleFunc("POST /api/antigravity/accounts/{id}/quota", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		quota, err := a.Accounts.RefreshQuota(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		send(w, quota)
	})
	mux.HandleFunc("POST /api/antigravity/accounts/{id}/switch", func(w http.ResponseWriter, r *http.Request) {
		var body app.AccountTarget
		if !read(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		status, err := a.SwitchAccount(ctx, r.PathValue("id"), body)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, status)
	})
	mux.HandleFunc("POST /api/antigravity/capture", func(w http.ResponseWriter, r *http.Request) {
		var body app.AccountTarget
		if !read(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		account, err := a.CaptureAccount(ctx, body)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, account)
	})
	mux.HandleFunc("GET /api/antigravity/config", func(w http.ResponseWriter, r *http.Request) { send(w, a.Accounts.Config()) })
	mux.HandleFunc("PUT /api/antigravity/config", func(w http.ResponseWriter, r *http.Request) {
		var body antigravity.OAuthConfig
		if read(w, r, &body) {
			result(w, a.Accounts.SetConfig(body))
		}
	})
	mux.HandleFunc("POST /api/antigravity/oauth", func(w http.ResponseWriter, r *http.Request) {
		session, err := a.StartAccountOAuth()
		if err != nil {
			fail(w, err)
			return
		}
		send(w, session)
	})
	mux.HandleFunc("GET /api/antigravity/oauth/{id}", func(w http.ResponseWriter, r *http.Request) {
		session, err := a.Accounts.OAuthStatus(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		send(w, session)
	})
	mux.HandleFunc("POST /api/antigravity/oauth/{id}/open", func(w http.ResponseWriter, r *http.Request) {
		result(w, a.OpenAccountOAuthBrowser(r.PathValue("id")))
	})
	mux.HandleFunc("DELETE /api/antigravity/oauth/{id}", func(w http.ResponseWriter, r *http.Request) {
		session, err := a.Accounts.CancelOAuth(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		send(w, session)
	})
	mux.HandleFunc("GET /api/antigravity/native/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		send(w, a.Accounts.Status(ctx, antigravity.Target{User: r.URL.Query().Get("user"), Client: r.URL.Query().Get("client")}))
	})
	mux.HandleFunc("POST /api/antigravity/native/capture", func(w http.ResponseWriter, r *http.Request) {
		var target antigravity.Target
		if !read(w, r, &target) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		credential, err := a.Accounts.Capture(ctx, target)
		if err != nil {
			fail(w, err)
			return
		}
		account, err := a.Accounts.ImportNativeCredential(ctx, "", credential)
		if err != nil {
			fail(w, err)
			return
		}
		if err = a.Accounts.ObserveCapture(target, account.ID); err != nil {
			fail(w, err)
			return
		}
		send(w, credential) // used only by the authenticated controller over pinned TLS
	})
	mux.HandleFunc("POST /api/antigravity/native/apply", func(w http.ResponseWriter, r *http.Request) {
		var body antigravity.Transfer
		if !read(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		status, err := a.Accounts.Apply(ctx, body)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, status)
	})
	mux.HandleFunc("GET /api/antigravity/usage", func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if raw := r.URL.Query().Get("days"); raw != "" {
			number, err := strconv.Atoi(raw)
			if err != nil || number < 1 || number > 365 {
				fail(w, fmt.Errorf("统计天数应在 1 到 365 之间"))
				return
			}
			days = number
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		report, err := a.Accounts.Usage(ctx, days, r.URL.Query().Get("account"), antigravity.Target{User: r.URL.Query().Get("user"), Client: r.URL.Query().Get("client")})
		if err != nil {
			fail(w, err)
			return
		}
		send(w, report)
	})
	mux.HandleFunc("POST /api/antigravity/usage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content   string `json:"content"`
			AccountID string `json:"account_id"`
			antigravity.Target
		}
		if !read(w, r, &body) {
			return
		}
		count, err := a.Accounts.ImportUsage(body.Content, body.AccountID, body.Target)
		if err != nil {
			fail(w, err)
			return
		}
		send(w, map[string]any{"count": count})
	})
}
