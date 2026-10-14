package program

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBrowseProgramsWithoutStartingThem(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name string
		mode os.FileMode
	}{{"editor.exe", 0755}, {"notes.txt", 0644}} {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte("fixture"), file.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "Browser.app"), 0755); err != nil {
		t.Fatal(err)
	}
	listing, err := Browse(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Directory != dir || listing.Parent != filepath.Dir(dir) {
		t.Fatalf("incorrect navigation: %+v", listing)
	}
	entries := make(map[string]Entry)
	for _, entry := range listing.Entries {
		entries[entry.Name] = entry
	}
	if len(entries) != 3 || entries["editor.exe"].Directory || !entries["nested"].Directory {
		t.Fatalf("executables and folders were not discovered: %+v", entries)
	}
	if _, ok := entries["notes.txt"]; ok {
		t.Fatal("non-executable document was offered as a program")
	}
	if entries["Browser.app"].Directory != (runtime.GOOS != "darwin") {
		t.Fatal("macOS application bundle was not selectable")
	}
	// Browsing must not change or execute the selected file.
	data, err := os.ReadFile(filepath.Join(dir, "editor.exe"))
	if err != nil || string(data) != "fixture" {
		t.Fatal("browsing altered the executable")
	}
}

func TestBrowseRejectsInvalidDirectoryAndCancellation(t *testing.T) {
	for _, directory := range []string{"relative", "/tmp/invalid\npath", filepath.Join(t.TempDir(), "missing")} {
		if _, err := Browse(context.Background(), directory); err == nil {
			t.Fatalf("invalid directory accepted: %q", directory)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Browse(ctx, t.TempDir()); err != context.Canceled {
		t.Fatalf("cancellation was ignored: %v", err)
	}
}
