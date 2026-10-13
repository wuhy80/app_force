package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"appforce/internal/app"
	"appforce/internal/model"
)

func setup(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a, err := app.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, Handler(a)
}
func call(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestAuthenticationOriginAndSecretRedaction(t *testing.T) {
	a, handler := setup(t)
	if call(handler, "GET", "/api/state", "", "").Code != 401 {
		t.Fatal("unauthenticated API accessible")
	}
	req := httptest.NewRequest("POST", "/api/engine/start", nil)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	body := `{"name":"secret subscription","content":"trojan://not-for-state@example.org:443#test"}`
	w = call(handler, "POST", "/api/sources", a.Token, body)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(handler, "GET", "/api/state", a.Token, "")
	if strings.Contains(w.Body.String(), "not-for-state") || strings.Contains(w.Body.String(), a.Token) || strings.Contains(w.Body.String(), "outbound") {
		t.Fatal("state exposed credentials")
	}
	if call(handler, "POST", "/api/rules", a.Token, `{"path":"relative.exe","enabled":true}`).Code != 400 {
		t.Fatal("relative executable path accepted")
	}
	if call(handler, "PUT", "/api/settings", a.Token, `{"mode":"tun","unknown":true}`).Code != 400 {
		t.Fatal("unknown fields accepted")
	}
	if call(handler, "PUT", "/api/nodes/selected", a.Token, `{"id":"missing"} {}`).Code != 400 {
		t.Fatal("multiple JSON objects accepted")
	}
}

func TestPinnedRemoteDeviceManagement(t *testing.T) {
	remote, remoteHandler := setup(t)
	server := httptest.NewTLSServer(remoteHandler)
	defer server.Close()
	local, localHandler := setup(t)
	certHash := sha256.Sum256(server.Certificate().Raw)
	d := model.Device{Name: "test device", URL: server.URL, Token: remote.Token, CertificateSHA256: hex.EncodeToString(certHash[:])}
	body, _ := json.Marshal(d)
	w := call(localHandler, "POST", "/api/devices", local.Token, string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	devices := local.Snapshot().Devices
	if len(devices) != 1 || devices[0].Token != "" {
		t.Fatal("device credentials exposed")
	}
	prefix := "/api/remote/" + devices[0].ID + "/"
	w = call(localHandler, "GET", prefix+"state", local.Token, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), remote.Token) {
		t.Fatal("remote token exposed")
	}
	if call(localHandler, "GET", prefix+"devices", local.Token, "").Code != 400 {
		t.Fatal("nested pairing route accessible")
	}
	if call(localHandler, "GET", prefix+"remote/anything/state", local.Token, "").Code != 400 {
		t.Fatal("recursive remote route accessible")
	}
	// A known device with a different fingerprint must fail before any authenticated HTTP request.
	d.ID = devices[0].ID
	d.CertificateSHA256 = strings.Repeat("0", 64)
	body, _ = json.Marshal(d)
	w = call(localHandler, "POST", "/api/devices", local.Token, string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if call(localHandler, "GET", prefix+"state", local.Token, "").Code != 400 {
		t.Fatal("wrong certificate pin accepted")
	}
	d.URL = "http://192.0.2.10:17891"
	body, _ = json.Marshal(d)
	if call(localHandler, "POST", "/api/devices", local.Token, string(body)).Code != 400 {
		t.Fatal("plaintext remote management accepted")
	}
}

func TestSubscriptionRefreshAndSavedRules(t *testing.T) {
	content := "trojan://secret@example.org:443#original"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, content) }))
	defer source.Close()
	dir := t.TempDir()
	a, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(a)
	body, _ := json.Marshal(app.ImportRequest{Name: "subscription", URL: source.URL + "?token=private"})
	w := call(handler, "POST", "/api/sources", a.Token, string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := a.Snapshot()
	if strings.Contains(call(handler, "GET", "/api/state", a.Token, "").Body.String(), "private") {
		t.Fatal("subscription URL token leaked")
	}
	content = "trojan://secret@example.org:443#renamed"
	w = call(handler, "POST", "/api/sources/"+before.Sources[0].ID+"/refresh", a.Token, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after := a.Snapshot()
	if before.SelectedNodeID != after.SelectedNodeID || after.Nodes[0].Name != "renamed" {
		t.Fatal("refresh lost node selection")
	}
	content = "invalid subscription"
	w = call(handler, "POST", "/api/sources/"+before.Sources[0].ID+"/refresh", a.Token, "")
	if w.Code != 400 {
		t.Fatal("invalid refresh accepted")
	}
	if len(a.Snapshot().Nodes) != 1 {
		t.Fatal("failed refresh discarded nodes")
	}
	rule := model.Rule{Name: "program", Path: filepath.Join(dir, "program"), Enabled: true}
	body, _ = json.Marshal(rule)
	w = call(handler, "POST", "/api/rules", a.Token, string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reopened, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Token != a.Token || len(reopened.Snapshot().Rules) != 1 {
		t.Fatal("persistent config or token lost")
	}
}
