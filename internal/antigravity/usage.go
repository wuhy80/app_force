package antigravity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"

	bolt "github.com/sagernet/bbolt"
)

// Total follows the client's reported total. Cache/thinking are kept separately:
// different clients include them in different counters, so never sum them twice.
type Tokens struct {
	Input    int64 `json:"input_tokens"`
	Output   int64 `json:"output_tokens"`
	Cache    int64 `json:"cached_tokens"`
	Thinking int64 `json:"thinking_tokens"`
	Total    int64 `json:"total_tokens"`
	Requests int64 `json:"requests"`
}

func (t *Tokens) add(other Tokens) {
	t.Input += other.Input
	t.Output += other.Output
	t.Cache += other.Cache
	t.Thinking += other.Thinking
	t.Total += other.Total
	t.Requests += other.Requests
}

type UsageRecord struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	AccountID string    `json:"account_id"`
	User      string    `json:"user"`
	Model     string    `json:"model"`
	Source    string    `json:"source"`
	Tokens
}

type UsageRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tokens
}
type UsageBucket struct {
	Time string `json:"time"`
	Tokens
}
type UsageReport struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Days        int           `json:"days"`
	CollectedAt *time.Time    `json:"collected_at,omitempty"`
	Summary     Tokens        `json:"summary"`
	Lifetime    Tokens        `json:"lifetime"`
	Accounts    []UsageRow    `json:"accounts"`
	Models      []UsageRow    `json:"models"`
	History     []UsageBucket `json:"history"`
	Warnings    []string      `json:"warnings"`
	Files       int           `json:"files"`
}

type usageStore struct {
	path string
	mu   sync.Mutex
}

func openUsage(dir string) (*usageStore, error) {
	s := &usageStore{path: filepath.Join(dir, "token-usage.db")}
	err := s.transaction(true, func(tx *bolt.Tx) error {
		for _, name := range []string{"records", "cursors", "meta"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	})
	return s, err
}

func (s *usageStore) transaction(write bool, fn func(*bolt.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := bolt.Open(s.path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	if write {
		return db.Update(fn)
	}
	return db.View(fn)
}

func (s *usageStore) append(records []UsageRecord) (int, error) {
	count := 0
	err := s.transaction(true, func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("records"))
		for _, record := range records {
			if err := validateRecord(record); err != nil {
				return err
			}
			// Account assignment is intentionally not part of the deduplication key.
			// Reimporting the same response cannot charge it to another account.
			sum := sha256.Sum256([]byte(record.User + "\x00" + record.ID))
			key := sum[:]
			data, _ := json.Marshal(record)
			if old := bucket.Get(key); old != nil {
				var previous UsageRecord
				_ = json.Unmarshal(old, &previous)
				if previous.Tokens != record.Tokens || !previous.Time.Equal(record.Time) || previous.Model != record.Model {
					return fmt.Errorf("用量记录 ID 冲突，未导入本批数据")
				}
				if previous.AccountID == "unassigned" && record.AccountID != "unassigned" && record.Source == "import" {
					previous.AccountID = record.AccountID
					updated, _ := json.Marshal(previous)
					if err := bucket.Put(key, updated); err != nil {
						return err
					}
				}
				continue
			}
			if err := bucket.Put(key, data); err != nil {
				return err
			}
			count++
		}
		if len(records) > 0 {
			now, _ := json.Marshal(time.Now())
			return tx.Bucket([]byte("meta")).Put([]byte("collected_at"), now)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func validateRecord(record UsageRecord) error {
	if record.ID == "" || len(record.ID) > 1024 || record.Model == "" || len(record.Model) > 200 || len(record.User) > 256 || len(record.Source) > 256 {
		return fmt.Errorf("用量记录需要有效的 ID 和模型名称")
	}
	if record.Time.IsZero() || record.Time.After(time.Now().Add(5*time.Minute)) || record.Time.Year() < 2020 {
		return fmt.Errorf("用量记录需要有效的时间，不能使用未来时间")
	}
	for _, value := range []int64{record.Input, record.Output, record.Cache, record.Thinking, record.Total} {
		if value < 0 || value > 1e12 {
			return fmt.Errorf("token 数量必须为非负整数")
		}
	}
	if record.Requests != 1 {
		return fmt.Errorf("每条用量记录必须对应一次响应")
	}
	return nil
}

func (s *usageStore) report(days int, accountID, user string) (UsageReport, error) {
	if days < 1 || days > 365 {
		return UsageReport{}, fmt.Errorf("统计天数应在 1 到 365 之间")
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1-days)
	report := UsageReport{GeneratedAt: now, Days: days, Accounts: []UsageRow{}, Models: []UsageRow{}, History: []UsageBucket{}, Warnings: []string{}}
	accounts := map[string]*UsageRow{}
	models := map[string]*UsageRow{}
	history := map[string]*Tokens{}
	err := s.transaction(false, func(tx *bolt.Tx) error {
		if value := tx.Bucket([]byte("meta")).Get([]byte("collected_at")); value != nil {
			var t time.Time
			if json.Unmarshal(value, &t) == nil {
				report.CollectedAt = &t
			}
		}
		return tx.Bucket([]byte("records")).ForEach(func(_, value []byte) error {
			var record UsageRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("token 历史记录损坏")
			}
			if user != "" && record.User != user {
				return nil
			}
			if accountID != "" && record.AccountID != accountID {
				return nil
			}
			report.Lifetime.add(record.Tokens)
			if record.Time.Before(start) || record.Time.After(now) {
				return nil
			}
			report.Summary.add(record.Tokens)
			if accounts[record.AccountID] == nil {
				accounts[record.AccountID] = &UsageRow{ID: record.AccountID, Name: record.AccountID}
			}
			accounts[record.AccountID].add(record.Tokens)
			if models[record.Model] == nil {
				models[record.Model] = &UsageRow{ID: record.Model, Name: record.Model}
			}
			models[record.Model].add(record.Tokens)
			layout := "2006-01-02"
			if days == 1 {
				layout = "2006-01-02T15:00"
			}
			key := record.Time.In(now.Location()).Format(layout)
			if history[key] == nil {
				history[key] = &Tokens{}
			}
			history[key].add(record.Tokens)
			return nil
		})
	})
	if err != nil {
		return report, err
	}
	for _, row := range accounts {
		report.Accounts = append(report.Accounts, *row)
	}
	for _, row := range models {
		report.Models = append(report.Models, *row)
	}
	sort.Slice(report.Accounts, func(i, j int) bool { return report.Accounts[i].ID < report.Accounts[j].ID })
	sort.Slice(report.Models, func(i, j int) bool {
		if report.Models[i].Total == report.Models[j].Total {
			return report.Models[i].ID < report.Models[j].ID
		}
		return report.Models[i].Total > report.Models[j].Total
	})
	if days == 1 {
		first := start.Add(8 * time.Hour)
		if first.After(now) {
			first = start.Add(time.Duration(now.Hour()) * time.Hour)
		}
		for key := range history {
			if t, err := time.ParseInLocation("2006-01-02T15:00", key, now.Location()); err == nil && t.Before(first) {
				first = t
			}
		}
		for t := first; !t.After(now); t = t.Add(time.Hour) {
			key := t.Format("2006-01-02T15:00")
			tokens := Tokens{}
			if history[key] != nil {
				tokens = *history[key]
			}
			report.History = append(report.History, UsageBucket{key, tokens})
		}
	} else {
		for t := start; !t.After(now); t = t.AddDate(0, 0, 1) {
			key := t.Format("2006-01-02")
			tokens := Tokens{}
			if history[key] != nil {
				tokens = *history[key]
			}
			report.History = append(report.History, UsageBucket{key, tokens})
		}
	}
	return report, nil
}

// ParseUsage accepts canonical AppForce records and per-response agy/Gemini
// records. It skips cumulative session results and unfinished streaming updates.
func ParseUsage(content string, accountID, user, source string) ([]UsageRecord, error) {
	if len(content) > 16<<20 {
		return nil, fmt.Errorf("用量文件不能超过 16 MB")
	}
	data := bytes.TrimSpace([]byte(content))
	if len(data) == 0 {
		return nil, fmt.Errorf("用量文件为空")
	}
	var values []json.RawMessage
	if data[0] == '[' {
		if json.Unmarshal(data, &values) != nil {
			return nil, fmt.Errorf("用量 JSON 数组无效")
		}
		if len(values) > 50000 {
			return nil, fmt.Errorf("一次最多导入 50000 条用量记录")
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var value json.RawMessage
			if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("用量 JSON / JSONL 格式无效")
			}
			values = append(values, value)
			if len(values) > 50000 {
				return nil, fmt.Errorf("一次最多导入 50000 条用量记录")
			}
		}
	}
	records := []UsageRecord{}
	for index, raw := range values {
		record, found, err := parseUsageObject(raw, accountID, user, source, "")
		if err != nil {
			return nil, fmt.Errorf("第 %d 条用量记录无效: %w", index+1, err)
		}
		if found {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("没有找到包含 token、响应 ID、时间和模型的逐次用量记录；会话累计结果不会重复计入")
	}
	return records, nil
}

func parseUsageObject(raw []byte, accountID, user, source, fallbackID string) (UsageRecord, bool, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return UsageRecord{}, false, fmt.Errorf("记录不是 JSON 对象")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return UsageRecord{}, false, fmt.Errorf("每条用量记录只能包含一个 JSON 对象")
	}
	if object == nil {
		return UsageRecord{}, false, nil
	}
	if stringValue(object["event"]) == "result" {
		return UsageRecord{}, false, nil
	}
	data := object
	if state := stringValue(field(object, "state", "status")); state == "ACTIVE" || state == "RUNNING" || state == "PENDING" {
		return UsageRecord{}, false, nil
	}
	if conversation := stringValue(field(object, "conversation_id", "conversationId")); conversation != "" {
		if index := stringValue(field(object, "step_index", "stepIndex")); index != "" {
			fallbackID = conversation + "/step/" + index
		}
	}
	if step, ok := object["step_update"].(map[string]any); ok {
		data = step
		if state := stringValue(field(step, "state", "status")); state != "DONE" && state != "COMPLETED" {
			return UsageRecord{}, false, nil
		}
		conversation := stringValue(step["conversation_id"])
		index := stringValue(step["step_index"])
		if conversation != "" && index != "" {
			fallbackID = conversation + "/step/" + index
		}
	}
	usage, ok := field(data, "usage", "usageMetadata", "usage_metadata", "token_usage").(map[string]any)
	if !ok {
		if response, ok := data["response"].(map[string]any); ok {
			usage, _ = field(response, "usageMetadata", "usage").(map[string]any)
		}
	}
	if usage == nil {
		if field(data, "input_tokens", "promptTokenCount") == nil {
			return UsageRecord{}, false, nil
		}
		usage = data
	}
	if field(usage, "input_tokens", "prompt_tokens", "promptTokenCount", "inputTokens", "output_tokens", "completion_tokens", "candidatesTokenCount", "outputTokens", "total_tokens", "totalTokenCount", "totalTokens") == nil {
		return UsageRecord{}, false, nil
	}
	record := UsageRecord{ID: stringValue(field(data, "id", "responseId", "response_id", "step_id")), AccountID: accountID, User: user, Source: source, Model: stringValue(field(data, "model", "modelName", "model_name", "modelVersion"))}
	if record.ID == "" {
		record.ID = fallbackID
	}
	if record.Model == "" {
		record.Model = stringValue(field(object, "model", "modelName", "model_name", "modelVersion"))
	}
	record.Time = parseTime(field(data, "time", "timestamp", "created_at", "createdAt", "start_time"))
	if record.Time.IsZero() {
		record.Time = parseTime(field(object, "time", "timestamp", "created_at", "createdAt"))
	}
	var err error
	get := func(names ...string) int64 {
		value := field(usage, names...)
		if value == nil {
			return 0
		}
		number, ok := value.(json.Number)
		if !ok {
			err = fmt.Errorf("token 数量必须是整数")
			return 0
		}
		result, e := number.Int64()
		if e != nil {
			err = fmt.Errorf("token 数量必须是整数")
		}
		return result
	}
	record.Input = get("input_tokens", "prompt_tokens", "promptTokenCount", "inputTokens")
	record.Output = get("output_tokens", "completion_tokens", "candidatesTokenCount", "outputTokens")
	record.Cache = get("cached_tokens", "cache_read_tokens", "cachedContentTokenCount", "cacheReadTokens")
	record.Thinking = get("thinking_tokens", "reasoning_tokens", "thoughtsTokenCount", "thinkingTokens")
	record.Total = get("total_tokens", "totalTokenCount", "totalTokens")
	if field(usage, "total_tokens", "totalTokenCount", "totalTokens") == nil {
		record.Total = record.Input + record.Output
		if usage["thoughtsTokenCount"] != nil {
			record.Total += record.Thinking
		}
	}
	record.Requests = 1
	if err != nil {
		return UsageRecord{}, false, err
	}
	if record.Model == "" {
		record.Model = "未知模型"
	}
	if err = validateRecord(record); err != nil {
		return UsageRecord{}, false, err
	}
	return record, true, nil
}

func parseTime(value any) time.Time {
	if text, ok := value.(string); ok {
		t, _ := time.Parse(time.RFC3339Nano, text)
		return t
	}
	if number, ok := value.(json.Number); ok {
		v, err := number.Int64()
		if err == nil {
			if v > 1e12 {
				return time.UnixMilli(v)
			}
			return time.Unix(v, 0)
		}
	}
	return time.Time{}
}
