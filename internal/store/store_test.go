package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTUNStackLoadsLegacyConfigurationAndPersistsSelection(t *testing.T) {
	for _, emptyField := range []bool{false, true} {
		dir := t.TempDir()
		s, _, token, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]any{
			"mode": "tun", "mixed_port": 18001,
			"dns_mode": "proxy", "dns_address": "8.8.8.8",
			"strict_route": true, "auto_redirect": false,
		}
		if emptyField {
			settings["tun_stack"] = ""
		}
		legacy, err := json.Marshal(map[string]any{"version": 1, "selected_node_id": "keep-selection", "settings": settings})
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "config.json")
		if err := os.WriteFile(file, legacy, 0600); err != nil {
			t.Fatal(err)
		}
		_, config, loadedToken, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if config.Settings.TUNStack != "gvisor" || config.Settings.MixedPort != 18001 || config.Settings.DNSMode != "proxy" || !config.Settings.StrictRoute || config.Settings.AutoRedirect || config.SelectedNodeID != "keep-selection" || loadedToken != token {
			t.Fatalf("legacy configuration was not preserved while defaulting the stack: %+v", config)
		}
		unchanged, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(unchanged, legacy) {
			t.Fatal("loading a legacy configuration rewrote the file")
		}
		for _, choice := range []string{"system", "mixed", "gvisor"} {
			config.Settings.TUNStack = choice
			if err := s.Save(config); err != nil {
				t.Fatal(err)
			}
			_, loaded, _, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Settings != config.Settings || loaded.SelectedNodeID != config.SelectedNodeID {
				t.Fatalf("stack selection did not survive reopening configuration: %+v", loaded)
			}
		}
	}
}

func TestAtomicPersistenceAndCorruptConfigNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	s, c, token, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.SelectedNodeID = "selection"
	if err = s.Save(c); err != nil {
		t.Fatal(err)
	}
	_, loaded, loadedToken, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedNodeID != "selection" || loadedToken != token {
		t.Fatal("persisted data mismatch")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"token", "config.json"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s permissions: %v", name, info.Mode())
			}
		}
	}
	file := filepath.Join(dir, "config.json")
	bad := []byte("{broken")
	if err := os.WriteFile(file, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(dir); err == nil {
		t.Fatal("corrupt config silently reset")
	}
	unchanged, _ := os.ReadFile(file)
	if string(unchanged) != string(bad) {
		t.Fatal("corrupt config overwritten")
	}
}
