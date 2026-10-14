//go:build linux || darwin || openharmony

package mobile

import (
	"encoding/json"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSubscription = `{"outbounds":[{"type":"socks","tag":"测试节点","server":"127.0.0.1","server_port":1090,"username":"fixture-user","password":"fixture-secret"}]}`

func invoke(t *testing.T, req Request) Reply {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return Call(data)
}

func importedConfig(t *testing.T) (string, *State) {
	t.Helper()
	dir := t.TempDir()
	reply := invoke(t, Request{Method: "import", Directory: dir, Name: "测试订阅", Content: sampleSubscription, URL: "https://example.com/sub?token=fixture-token"})
	if !reply.OK {
		t.Fatal(reply.Error)
	}
	return dir, reply.State
}

func TestImportIsPrivateAtomicAndStable(t *testing.T) {
	dir, state := importedConfig(t)
	encoded, _ := json.Marshal(state)
	for _, secret := range []string{"fixture-secret", "fixture-user", "fixture-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public state exposed %s", secret)
		}
	}
	path := filepath.Join(dir, "config.json")
	before, _ := os.ReadFile(path)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("configuration permissions: %v %v", info, err)
	}
	bad := invoke(t, Request{Method: "import", Directory: dir, SourceID: state.Sources[0].ID, Content: "not a subscription"})
	if bad.OK {
		t.Fatal("invalid refresh succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed refresh overwrote existing configuration")
	}
	refresh := invoke(t, Request{Method: "import", Directory: dir, SourceID: state.Sources[0].ID, Content: sampleSubscription})
	if !refresh.OK || refresh.State.SelectedNodeID != state.SelectedNodeID || len(refresh.State.Nodes) != 1 {
		t.Fatalf("refresh lost stable selection: %+v", refresh)
	}
	reload := invoke(t, Request{Method: "state", Directory: dir})
	if !reload.OK || reload.State.SelectedNodeID != state.SelectedNodeID {
		t.Fatal("configuration did not survive reopening")
	}
}

func TestApplicationsNeverCreateAnEmptyOrSelfCapturingVPN(t *testing.T) {
	dir, _ := importedConfig(t)
	if reply := invoke(t, Request{Method: "prepareVpn", Directory: dir}); reply.OK {
		t.Fatal("empty allowlist accepted")
	}
	for _, bundle := range []string{DefaultBundle, "/Applications/Example.app", "bad bundle"} {
		if reply := invoke(t, Request{Method: "saveApplication", Directory: dir, Application: Application{BundleName: bundle, Enabled: true}}); reply.OK {
			t.Fatalf("invalid bundle accepted: %q", bundle)
		}
	}
	app := Application{BundleName: "com.example.browser", Name: "浏览器", Enabled: true}
	if reply := invoke(t, Request{Method: "saveApplication", Directory: dir, Application: app}); !reply.OK {
		t.Fatal(reply.Error)
	}
	plan := invoke(t, Request{Method: "prepareVpn", Directory: dir})
	if !plan.OK || len(plan.VPN.Applications) != 1 || plan.VPN.Applications[0] != app.BundleName {
		t.Fatalf("unexpected VPN plan: %+v", plan)
	}
	app.Enabled = false
	invoke(t, Request{Method: "saveApplication", Directory: dir, Application: app})
	if reply := invoke(t, Request{Method: "prepareVpn", Directory: dir}); reply.OK {
		t.Fatal("all-disabled allowlist accepted")
	}
}

func TestRunningProcessOwnsConfigurationUntilStopped(t *testing.T) {
	dir, state := importedConfig(t)
	lock := flock.New(filepath.Join(dir, "vpn.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	change := invoke(t, Request{Method: "selectNode", Directory: dir, NodeID: state.SelectedNodeID})
	if change.OK {
		t.Fatal("configuration changed while another runtime owns it")
	}
	read := invoke(t, Request{Method: "state", Directory: dir})
	if !read.OK || !read.State.Runtime.Active || read.State.Runtime.Phase != "unknown" {
		t.Fatalf("lost active runtime when heartbeat is missing: %+v", read)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	change = invoke(t, Request{Method: "selectNode", Directory: dir, NodeID: state.SelectedNodeID})
	if !change.OK {
		t.Fatal(change.Error)
	}
}

func TestCoreConfigUsesSystemTUNAndSelectedOutbound(t *testing.T) {
	dir, _ := importedConfig(t)
	invoke(t, Request{Method: "saveApplication", Directory: dir, Application: Application{BundleName: "com.example.browser", Enabled: true}})
	s, _ := OpenStore(dir)
	cfg, err := s.locked(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := BuildCoreConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := coreContext(newPlatform(nil, nil))
	opts, err := decodeCore(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Route.Final != "selected" || opts.Route.FindProcess || !opts.Route.AutoDetectInterface {
		t.Fatalf("mobile route policy is invalid: %s", data)
	}
	if strings.Contains(string(data), "process_path") || strings.Contains(string(data), `"auto_route":true`) {
		t.Fatal("desktop routing leaked into phone configuration")
	}
	if reply := invoke(t, Request{Method: "removeSource", Directory: dir, SourceID: cfg.Sources[0].ID}); !reply.OK || reply.State.SelectedNodeID != "" || len(reply.State.Nodes) != 0 {
		t.Fatal("removed source left an active selection")
	}
}

func TestCustomBundleInitializationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	request := Request{Method: "initialize", Directory: dir, SelfBundle: "org.example.appforce"}
	first := invoke(t, request)
	if !first.OK {
		t.Fatal(first.Error)
	}
	lock := flock.New(filepath.Join(dir, "vpn.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	again := invoke(t, request)
	if !again.OK || again.State.Revision != first.State.Revision {
		t.Fatal("reopening changed configuration while VPN is running")
	}
	if reply := invoke(t, Request{Method: "saveApplication", Directory: dir, Application: Application{BundleName: request.SelfBundle, Enabled: true}}); reply.OK {
		t.Fatal("custom app captured itself")
	}
}
