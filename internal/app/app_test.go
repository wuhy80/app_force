package app

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"appforce/internal/model"
	"appforce/internal/store"
)

func TestSavedAntigravityRuleAutomaticallyIncludesLanguageServer(t *testing.T) {
	dir := t.TempDir()
	s, config, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a Windows rule saved before helper matching was introduced.
	config.Rules = []model.Rule{{ID: "antigravity", Name: "Antigravity", Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true}}
	if err := s.Save(config); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	rules := a.Snapshot().Rules
	if len(rules) != 1 || rules[0].ID != "antigravity" || !rules[0].Enabled || len(rules[0].ProcessPathRegex) == 0 {
		t.Fatalf("saved rule did not automatically gain helper scope: %+v", rules)
	}
	if !rules[0].MatchesPath(`C:\Apps\Antigravity\resources\app\bin\language_server_windows_x64.exe`) {
		t.Fatal("saved rule does not cover the language server")
	}
	_, saved, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Rules) != 1 || strings.Contains(string(raw), "process_path_regex") {
		t.Fatal("generated helper rules were unnecessarily persisted")
	}
}

func TestRunningConfigurationRollsBackWhenPortIsBusy(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	_, _, err = a.Import(context.Background(), ImportRequest{Content: `socks5://127.0.0.1:19999#node`}, "")
	if err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	settings := a.Snapshot().Settings
	settings.Mode = "mixed"
	settings.TUNStack = "mixed"
	settings.MixedPort = free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	if err := a.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	bad := settings
	bad.MixedPort = occupied.Addr().(*net.TCPAddr).Port
	bad.TUNStack = "system"
	if err := a.SetSettings(bad); err == nil {
		t.Fatal("configuration with busy listener accepted")
	}
	state := a.Snapshot()
	if !state.Engine.Running || state.Settings.MixedPort != settings.MixedPort || state.Settings.TUNStack != settings.TUNStack {
		b, _ := json.Marshal(state)
		t.Fatalf("old engine was not restored: %s", b)
	}
	_, saved, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings.MixedPort != settings.MixedPort || saved.Settings.TUNStack != settings.TUNStack {
		t.Fatal("failed configuration persisted")
	}
}
