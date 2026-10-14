package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"appforce/internal/app"
	"appforce/internal/model"
)

func TestProgramNodeSelectionPersistsAndCanFollowDefault(t *testing.T) {
	dir := t.TempDir()
	a, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	if _, _, err := a.Import(context.Background(), app.ImportRequest{Content: "socks5://127.0.0.1:1080#default\nsocks5://127.0.0.1:2080#program"}, ""); err != nil {
		t.Fatal(err)
	}
	nodes := a.Snapshot().Nodes
	handler := Handler(a)
	save := func(rule model.Rule, want int) {
		t.Helper()
		body, err := json.Marshal(rule)
		if err != nil {
			t.Fatal(err)
		}
		response := call(handler, "POST", "/api/rules", a.Token, string(body))
		if response.Code != want {
			t.Fatalf("save rule: got %d, want %d: %s", response.Code, want, response.Body.String())
		}
	}
	save(model.Rule{Name: "Browser", Path: filepath.Join(dir, "browser"), NodeID: nodes[1].ID, Enabled: true}, 200)
	rule := a.Snapshot().Rules[0].Rule
	if rule.NodeID != nodes[1].ID {
		t.Fatal("program node not exposed in state")
	}
	if err := a.SelectNode(nodes[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.SelectNode(nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Rules[0].NodeID != nodes[1].ID {
		t.Fatal("default selection overwrote program node")
	}
	rule.Enabled = false
	save(rule, 200)
	rule.Enabled = true
	save(rule, 200)
	reopened, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Stop() })
	if saved := reopened.Snapshot().Rules[0]; saved.NodeID != nodes[1].ID || !saved.Enabled {
		t.Fatalf("toggle or restart lost program node: %+v", saved)
	}
	bad := rule
	bad.NodeID = "missing-node"
	save(bad, 400)
	if a.Snapshot().Rules[0].NodeID != nodes[1].ID {
		t.Fatal("failed selection changed saved program node")
	}
	rule.NodeID = ""
	save(rule, 200)
	if a.Snapshot().Rules[0].NodeID != "" {
		t.Fatal("program could not return to following default")
	}
	// Old API clients and configurations omit node_id and retain default behavior.
	save(model.Rule{Name: "Legacy", Path: filepath.Join(dir, "legacy"), Enabled: true}, 200)
	if a.Snapshot().Rules[1].NodeID != "" {
		t.Fatal("legacy rule unexpectedly overrides the default node")
	}
}
