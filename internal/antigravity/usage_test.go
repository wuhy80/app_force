package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func usageJSON(id string, at time.Time) string {
	return `{"id":"` + id + `","time":"` + at.Format(time.RFC3339Nano) + `","model":"gemini-test","input_tokens":100,"output_tokens":20,"cached_tokens":50,"thinking_tokens":5,"total_tokens":120}`
}

func TestUsagePersistenceDeduplicationAndAtomicConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := usageJSON("response-1", time.Now().Add(-time.Minute))
	records, err := ParseUsage(content, "one", "test-user", "import")
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.append(records); err != nil || count != 1 {
		t.Fatalf("first import: %d %v", count, err)
	}
	reopened, err := openUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := reopened.append(records); err != nil || count != 0 {
		t.Fatalf("duplicate import: %d %v", count, err)
	}
	conflict := records[0]
	conflict.Output++
	extra := records[0]
	extra.ID = "new-response"
	if _, err := store.append([]UsageRecord{extra, conflict}); err == nil {
		t.Fatal("conflicting ID accepted")
	}
	report, err := store.report(7, "", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total != 120 || report.Summary.Cache != 50 || report.Summary.Thinking != 5 || report.Summary.Requests != 1 || report.Lifetime.Total != 120 {
		t.Fatalf("wrong totals/double counted: %+v", report)
	}
	if other, err := store.report(7, "", "another-user"); err != nil || other.Summary.Requests != 0 {
		t.Fatal("usage crossed users")
	}
	if other, err := store.report(7, "another-account", "test-user"); err != nil || other.Summary.Requests != 0 {
		t.Fatal("usage crossed accounts")
	}
	if _, err = ParseUsage(strings.Replace(content, `"input_tokens":100`, `"input_tokens":-1`, 1), "one", "test-user", "import"); err == nil {
		t.Fatal("negative tokens accepted")
	}
	if _, err = ParseUsage(content+" ]", "one", "test-user", "import"); err == nil {
		t.Fatal("trailing malformed JSON accepted")
	}
}

func TestGeminiAndAGYDoNotCountCacheThinkingOrCumulativeResultsTwice(t *testing.T) {
	at := time.Now().Add(-time.Minute).Format(time.RFC3339)
	gemini := `{"responseId":"gemini-response","timestamp":"` + at + `","modelVersion":"gemini","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":50,"thoughtsTokenCount":5,"totalTokenCount":125}}`
	agy := `{"event":"step_update","timestamp":"` + at + `","model":"gemini","step_update":{"conversation_id":"session","step_index":1,"state":"DONE","usage":{"input_tokens":100,"output_tokens":20,"thinking_tokens":5,"cache_read_tokens":150,"total_tokens":120}}}`
	active := strings.Replace(agy, `"state":"DONE"`, `"state":"ACTIVE"`, 1)
	cumulative := `{"event":"result","result":{"usage":{"total_tokens":10000}}}`
	records, err := ParseUsage(gemini+"\n"+active+"\n"+agy+"\n"+cumulative, "one", "user", "import")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Total != 125 || records[1].Total != 120 || records[1].Cache != 150 {
		t.Fatalf("double count or unsupported client: %+v", records)
	}
}

func TestCollectorKeepsUnknownHistoryAndHandlesPartialLines(t *testing.T) {
	m := newManager(t, t.TempDir())
	home := t.TempDir()
	target := resolvedTarget{home: home, target: Target{User: "fixture-user", Client: "system"}}
	at := time.Now().Add(-time.Minute)
	m.state.Activations = []Activation{{Target: target.target, AccountID: "new-account", Time: at.Add(30 * time.Second)}}
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "session", ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first := usageJSON("old-response", at)
	second := usageJSON("new-response", at.Add(40*time.Second))
	path := filepath.Join(dir, "transcript.jsonl")
	split := len(second) / 2
	if err := os.WriteFile(path, []byte(first+"\n"+second[:split]), 0600); err != nil {
		t.Fatal(err)
	}
	warnings, _ := m.collect(context.Background(), target)
	if len(warnings) > 0 {
		t.Fatal(warnings)
	}
	report, err := m.usage.report(7, "", "fixture-user")
	if err != nil || report.Summary.Requests != 1 || report.Accounts[0].ID != "unassigned" {
		t.Fatal("historical log attributed to current account")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(second[split:] + "\n")
	_ = f.Close()
	m.collect(context.Background(), target)
	m.collect(context.Background(), target)
	report, err = m.usage.report(7, "", "fixture-user")
	if err != nil || report.Summary.Requests != 2 || report.Summary.Total != 240 {
		t.Fatalf("partial line/duplicate collection: %+v %v", report, err)
	}
	byAccount, err := m.usage.report(7, "new-account", "fixture-user")
	if err != nil || byAccount.Summary.Requests != 1 {
		t.Fatal("new activation not applied to new records")
	}
	// Only counters/IDs are persisted, not any prompt content from the transcript.
	promptLine := strings.Replace(usageJSON("private-response", at), `"model":`, `"prompt":"private-prompt-content","model":`, 1)
	if err := os.WriteFile(path, []byte(promptLine+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.collect(context.Background(), target)
	data, _ := os.ReadFile(m.usage.path)
	if strings.Contains(string(data), "private-prompt-content") {
		t.Fatal("prompt text stored in token database")
	}
}

func TestHourlyHistoryStartsAtEightOrEarlierUsage(t *testing.T) {
	store, err := openUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	hour := 5
	if now.Hour() < hour {
		hour = 0
	}
	at := start.Add(time.Duration(hour) * time.Hour)
	records, err := ParseUsage(usageJSON("early", at), "one", "user", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.append(records); err != nil {
		t.Fatal(err)
	}
	report, err := store.report(1, "", "user")
	if err != nil {
		t.Fatal(err)
	}
	if report.History[0].Time != at.Format("2006-01-02T15:00") {
		data, _ := json.Marshal(report.History)
		t.Fatalf("wrong hourly start: %s", data)
	}
}
