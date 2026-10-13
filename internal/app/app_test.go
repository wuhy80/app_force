package app

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"appforce/internal/store"
)

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
	if err := a.SetSettings(bad); err == nil {
		t.Fatal("configuration with busy listener accepted")
	}
	state := a.Snapshot()
	if !state.Engine.Running || state.Settings.MixedPort != settings.MixedPort {
		b, _ := json.Marshal(state)
		t.Fatalf("old engine was not restored: %s", b)
	}
	_, saved, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings.MixedPort != settings.MixedPort {
		t.Fatal("failed configuration persisted")
	}
}
