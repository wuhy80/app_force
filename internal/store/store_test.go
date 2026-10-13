package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
