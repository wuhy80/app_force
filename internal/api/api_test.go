package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"appforce/internal/app"
	"appforce/internal/model"
	"appforce/internal/program"
)

func setup(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a, err := app.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	t.Cleanup(a.Accounts.Close)
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

func TestTUNStackSettingsSaveAndLegacyRequests(t *testing.T) {
	a, handler := setup(t)
	initial := a.Snapshot().Settings
	if initial.TUNStack != "gvisor" {
		t.Fatalf("new settings do not default to gVisor: %+v", initial)
	}
	for _, choice := range []string{"system", "mixed", "gvisor", ""} {
		settings := initial
		settings.TUNStack = choice
		body, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if choice == "" {
			// Older clients send settings without the new field.
			var legacy map[string]any
			if err := json.Unmarshal(body, &legacy); err != nil {
				t.Fatal(err)
			}
			delete(legacy, "tun_stack")
			body, err = json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			settings.TUNStack = "gvisor"
		}
		w := call(handler, "PUT", "/api/settings", a.Token, string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("could not save stack %q: %s", choice, w.Body.String())
		}
		w = call(handler, "GET", "/api/state", a.Token, "")
		var state app.Snapshot
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || state.Settings != settings {
			t.Fatalf("settings response did not retain stack %q: %+v", choice, state.Settings)
		}
	}
	before := a.Snapshot().Settings
	invalid := before
	invalid.TUNStack = "unsupported"
	body, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	w := call(handler, "PUT", "/api/settings", a.Token, string(body))
	if w.Code != http.StatusBadRequest || a.Snapshot().Settings != before {
		t.Fatalf("invalid stack was accepted or changed saved settings: %s", w.Body.String())
	}
}

func TestAuthenticationOriginAndSecretRedaction(t *testing.T) {
	a, handler := setup(t)
	if call(handler, "GET", "/api/state", "", "").Code != 401 {
		t.Fatal("unauthenticated API accessible")
	}
	if call(handler, "GET", "/api/programs", "", "").Code != 401 {
		t.Fatal("unauthenticated program browsing accessible")
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
	w = call(localHandler, "GET", prefix+"traffic?days=30&include_direct=true", local.Token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"days":30`) || !strings.Contains(w.Body.String(), `"include_direct":true`) {
		t.Fatalf("remote traffic unavailable: %s", w.Body.String())
	}
	programDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(programDir, "remote-program.exe"), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	w = call(localHandler, "GET", prefix+"programs?directory="+url.QueryEscape(programDir), local.Token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "remote-program.exe") {
		t.Fatalf("remote program browsing failed: %s", w.Body.String())
	}
	if call(localHandler, "GET", prefix+"devices", local.Token, "").Code != 400 {
		t.Fatal("nested pairing route accessible")
	}
	if call(localHandler, "GET", prefix+"deployment/platforms", local.Token, "").Code != 400 {
		t.Fatal("remote deployment endpoint accessible")
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

func TestBrowseAndSaveProgramThatIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	programDir := t.TempDir()
	programPath := filepath.Join(programDir, "not-running.exe")
	if err := os.WriteFile(programPath, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	handler := Handler(a)
	w := call(handler, "GET", "/api/programs?directory="+url.QueryEscape(programDir), a.Token, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var listing program.Listing
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Path != programPath {
		t.Fatalf("stopped program was not selectable: %+v", listing)
	}
	body, _ := json.Marshal(model.Rule{Path: listing.Entries[0].Path, Enabled: true})
	w = call(handler, "POST", "/api/rules", a.Token, string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reopened, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Stop() })
	rules := reopened.Snapshot().Rules
	resolved, err := filepath.EvalSymlinks(programPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Path != resolved || !rules[0].Enabled {
		t.Fatalf("program rule did not survive restart: %+v", rules)
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
