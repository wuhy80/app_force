package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"appforce/internal/app"
	"appforce/internal/model"
	"appforce/internal/process"
	"appforce/internal/program"
	"appforce/internal/subscription"
	"appforce/internal/web"
)

func Handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { send(w, a.Snapshot()) })
	mux.HandleFunc("GET /api/programs", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		listing, err := program.Browse(ctx, r.URL.Query().Get("directory"))
		if err != nil {
			fail(w, err)
			return
		}
		send(w, listing)
	})
	mux.HandleFunc("GET /api/processes", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		items, err := process.List(ctx, r.URL.Query().Get("search"))
		if err != nil {
			fail(w, err)
			return
		}
		send(w, items)
	})
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) { send(w, a.Engine.Logs.Entries()) })
	mux.HandleFunc("POST /api/engine/start", func(w http.ResponseWriter, r *http.Request) { result(w, a.Start()) })
	mux.HandleFunc("POST /api/engine/stop", func(w http.ResponseWriter, r *http.Request) { result(w, a.Stop()) })
	mux.HandleFunc("PUT /api/nodes/selected", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if !read(w, r, &body) {
			return
		}
		result(w, a.SelectNode(body.ID))
	})
	mux.HandleFunc("POST /api/nodes/{id}/probe", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ms, err := a.Probe(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, fmt.Errorf("节点延迟测试失败: %w", err))
			return
		}
		send(w, map[string]any{"latency_ms": ms})
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings model.Settings
		if !read(w, r, &settings) {
			return
		}
		result(w, a.SetSettings(settings))
	})
	mux.HandleFunc("POST /api/rules", func(w http.ResponseWriter, r *http.Request) {
		var rule model.Rule
		if !read(w, r, &rule) {
			return
		}
		result(w, a.SaveRule(rule))
	})
	mux.HandleFunc("DELETE /api/rules/{id}", func(w http.ResponseWriter, r *http.Request) { result(w, a.DeleteRule(r.PathValue("id"))) })
	mux.HandleFunc("POST /api/sources", func(w http.ResponseWriter, r *http.Request) {
		var req app.ImportRequest
		if !read(w, r, &req) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		count, warnings, err := a.Import(ctx, req, "")
		if err != nil {
			sendError(w, http.StatusBadRequest, err.Error(), warnings)
			return
		}
		send(w, map[string]any{"count": count, "warnings": warnings})
	})
	mux.HandleFunc("POST /api/sources/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		count, warnings, err := a.Refresh(ctx, r.PathValue("id"))
		if err != nil {
			sendError(w, http.StatusBadRequest, err.Error(), warnings)
			return
		}
		send(w, map[string]any{"count": count, "warnings": warnings})
	})
	mux.HandleFunc("DELETE /api/sources/{id}", func(w http.ResponseWriter, r *http.Request) { result(w, a.DeleteSource(r.PathValue("id"))) })
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		var device model.Device
		if !read(w, r, &device) {
			return
		}
		result(w, a.SaveDevice(device))
	})
	mux.HandleFunc("DELETE /api/devices/{id}", func(w http.ResponseWriter, r *http.Request) { result(w, a.DeleteDevice(r.PathValue("id"))) })
	mux.HandleFunc("/api/remote/{id}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := "/api/" + r.PathValue("path")
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		status, data, err := a.Remote(r.Context(), r.PathValue("id"), r.Method, path, r.Body)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		sendError(w, http.StatusNotFound, "接口不存在", nil)
	})
	mux.Handle("/", http.FileServer(http.FS(web.Assets())))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(a.Token)) != 1 {
				sendError(w, http.StatusUnauthorized, "请输入有效的管理密钥", nil)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					sendError(w, http.StatusForbidden, "请求来源不允许", nil)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, subscription.MaxSize+65536)
		}
		mux.ServeHTTP(w, r)
	})
}

func read(w http.ResponseWriter, r *http.Request, target any) bool {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		fail(w, fmt.Errorf("请求 JSON 无效: %w", err))
		return false
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		fail(w, fmt.Errorf("请求只能包含一个 JSON 对象"))
		return false
	}
	return true
}
func send(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func sendError(w http.ResponseWriter, status int, message string, warnings []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "warnings": warnings})
}
func fail(w http.ResponseWriter, err error) { sendError(w, http.StatusBadRequest, err.Error(), nil) }
func result(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	send(w, map[string]bool{"ok": true})
}
