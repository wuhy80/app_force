package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os/user"
	"strings"
	"testing"
	"time"

	"appforce/internal/antigravity"
	"appforce/internal/app"
	"appforce/internal/model"
)

func TestAccountRoutesAuthenticationAndSecretRedaction(t *testing.T) {
	a, handler := setup(t)
	for _, path := range []string{"accounts", "config", "usage", "native/status", "oauth/missing"} {
		if response := call(handler, "GET", "/api/antigravity/"+path, "", ""); response.Code != 401 {
			t.Fatal("unauthenticated account route accessible", path)
		}
	}
	if response := call(handler, "DELETE", "/api/antigravity/oauth/missing", "", ""); response.Code != 401 {
		t.Fatal("unauthenticated OAuth cancellation accessible")
	}
	if response := call(handler, "POST", "/api/antigravity/oauth/missing/open", "", ""); response.Code != 401 {
		t.Fatal("unauthenticated authorization browser launch accessible")
	}
	config := `{"client_id":"own.apps.googleusercontent.com","client_secret":"secret-configuration","cloud":"daily"}`
	if response := call(handler, "PUT", "/api/antigravity/config", a.Token, config); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for _, path := range []string{"/api/antigravity/config", "/api/antigravity/accounts", "/api/state"} {
		if response := call(handler, "GET", path, a.Token, ""); response.Code != 200 || strings.Contains(response.Body.String(), "secret-configuration") {
			t.Fatal("public response leaked config", response.Body.String())
		}
	}
	if response := call(handler, "POST", "/api/antigravity/accounts", a.Token, `{"credential":"invalid-json"}`); response.Code != 400 {
		t.Fatal("invalid credential accepted")
	}
	if response := call(handler, "POST", "/api/antigravity/accounts/missing/switch", a.Token, `{"device_id":"","client":"system","user":"","unknown":true}`); response.Code != 400 {
		t.Fatal("unknown account switch field accepted")
	}
	if response := call(handler, "GET", "/api/antigravity/usage?days=366", a.Token, ""); response.Code != 400 {
		t.Fatal("invalid period accepted")
	}
}

func TestOAuthCanStartAndCancelUsingDefaultConfiguration(t *testing.T) {
	a, handler := setup(t)
	response := call(handler, "POST", "/api/antigravity/oauth", a.Token, "")
	if response.Code != 400 || !strings.Contains(response.Body.String(), "代理未启动") {
		t.Fatal("OAuth did not explain that the proxy must be started first")
	}
	if _, _, err := a.Import(context.Background(), app.ImportRequest{Content: "socks5://127.0.0.1:19999#fixture"}, ""); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	settings := a.Snapshot().Settings
	settings.Mode, settings.MixedPort = "mixed", listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := a.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	response = call(handler, "POST", "/api/antigravity/oauth", a.Token, "")
	var session antigravity.OAuthSession
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil || session.Status != "waiting" || session.URL == "" {
		t.Fatal("default OAuth browser login unavailable")
	}
	response = call(handler, "DELETE", "/api/antigravity/oauth/"+session.ID, a.Token, "")
	var cancelled antigravity.OAuthSession
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &cancelled) != nil || cancelled.Status != "cancelled" || cancelled.URL != "" {
		t.Fatal("OAuth cancellation unavailable or exposed authorization URL")
	}
	if response := call(handler, "POST", "/api/antigravity/oauth/"+session.ID+"/open", a.Token, ""); response.Code != 400 {
		t.Fatal("closed authorization could launch a browser")
	}
}

func TestRemoteTokenHistoryAndAccountRouteAllowlist(t *testing.T) {
	remote, remoteHandler := setup(t)
	server := httptest.NewTLSServer(remoteHandler)
	defer server.Close()
	local, handler := setup(t)
	hash := sha256.Sum256(server.Certificate().Raw)
	device := model.Device{Name: "Linux", URL: server.URL, Token: remote.Token, CertificateSHA256: hex.EncodeToString(hash[:])}
	if err := local.SaveDevice(device); err != nil {
		t.Fatal(err)
	}
	id := local.Snapshot().Devices[0].ID
	prefix := "/api/remote/" + id + "/antigravity/"
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	content := `{"id":"one","time":"` + time.Now().Add(-time.Minute).Format(time.RFC3339) + `","model":"gemini","input_tokens":100,"output_tokens":20,"total_tokens":120}`
	body, _ := json.Marshal(map[string]string{"content": content, "account_id": "unassigned", "user": u.Username, "client": "system"})
	response := call(handler, "POST", prefix+"usage", local.Token, string(body))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = call(handler, "GET", prefix+"usage?days=1&user="+u.Username, local.Token, "")
	var report antigravity.UsageReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || report.Summary.Total != 120 || report.Summary.Requests != 1 {
		t.Fatal("remote token history unavailable", response.Body.String())
	}
	for _, path := range []string{"config", "accounts", "oauth", "../config"} {
		if response := call(handler, "GET", prefix+path, local.Token, ""); response.Code == 200 {
			t.Fatal("remote controller-only route accessible", path)
		}
	}
	response = call(handler, "GET", prefix+"usage?days=7&user="+u.Username, local.Token, "")
	if strings.Contains(response.Body.String(), remote.Token) || strings.Contains(response.Body.String(), "refresh_token") {
		t.Fatal("remote usage leaked credentials")
	}
}
