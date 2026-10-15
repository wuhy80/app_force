package antigravity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLegacySQLiteCredentialUpdatePreservesUnrelatedState(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".config")
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support")
	}
	if runtime.GOOS == "windows" {
		base = home
		t.Setenv("APPDATA", home)
	}
	dir := filepath.Join(base, "Antigravity", "User", "globalStorage")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY,value TEXT)"); err != nil {
		t.Fatal(err)
	}
	extra, err := replaceTopic("", "other-setting", []byte("keep-me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO ItemTable VALUES (?, ?)", oauthStateKey, extra); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO ItemTable VALUES ('editor-setting','keep-editor')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	target := resolvedTarget{home: home, target: Target{User: "fixture-user", Client: "legacy-ide"}}
	credential := testCredential()
	credential.Email = "person@example.com"
	if err = writeLegacy(context.Background(), target, credential); err != nil {
		t.Fatal(err)
	}
	result, err := readLegacy(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != credential.Token || result.Email != credential.Email {
		t.Fatal("legacy token/user status does not match")
	}
	db, err = legacyDB(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var saved string
	if err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", oauthStateKey).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if payload, err := topicPayload(saved, "other-setting"); err != nil || string(payload) != "keep-me" {
		t.Fatal("unrelated protobuf topic lost")
	}
	if err = db.QueryRow("SELECT value FROM ItemTable WHERE key = 'editor-setting'").Scan(&saved); err != nil || saved != "keep-editor" {
		t.Fatal("unrelated SQLite setting lost")
	}
	if _, err = replaceTopic(base64.StdEncoding.EncodeToString([]byte{255}), "oauth", nil); err == nil {
		t.Fatal("malformed protobuf accepted")
	}
	if strings.Contains(saved, "access-secret") {
		t.Fatal("credential copied into unrelated setting")
	}
}
