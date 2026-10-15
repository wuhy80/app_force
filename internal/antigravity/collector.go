package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "github.com/sagernet/bbolt"
)

type cursor struct {
	Offset   int64     `json:"offset"`
	LastSize int64     `json:"last_size"`
	Modified time.Time `json:"modified"`
}

func (s *usageStore) cursor(key string) (cursor, error) {
	var value cursor
	err := s.transaction(false, func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte("cursors")).Get([]byte(key))
		if data != nil {
			return json.Unmarshal(data, &value)
		}
		return nil
	})
	return value, err
}

func (s *usageStore) saveCursor(key string, value cursor) error {
	data, _ := json.Marshal(value)
	return s.transaction(true, func(tx *bolt.Tx) error { return tx.Bucket([]byte("cursors")).Put([]byte(key), data) })
}

func (m *Manager) ImportUsage(content, accountID string, target Target) (int, error) {
	resolved, err := resolveTarget(target)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	found := accountID == "" || accountID == "unassigned"
	for _, a := range m.state.Accounts {
		if a.ID == accountID {
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return 0, fmt.Errorf("账号不存在")
	}
	if accountID == "" {
		accountID = "unassigned"
	}
	records, err := ParseUsage(content, accountID, resolved.target.User, "import")
	if err != nil {
		return 0, err
	}
	return m.usage.append(records)
}

func (m *Manager) Usage(ctx context.Context, days int, accountID string, target Target) (UsageReport, error) {
	resolved, err := resolveTarget(target)
	if err != nil {
		return UsageReport{}, err
	}
	warnings, files := m.collect(ctx, resolved)
	report, err := m.usage.report(days, accountID, resolved.target.User)
	if err != nil {
		return report, err
	}
	report.Warnings = warnings
	report.Files = files
	names := map[string]string{"unassigned": "未归属"}
	for _, a := range m.List() {
		names[a.ID] = a.Email
	}
	for i := range report.Accounts {
		if name := names[report.Accounts[i].ID]; name != "" {
			report.Accounts[i].Name = name
		} else {
			report.Accounts[i].Name = "已移除账号"
		}
	}
	return report, nil
}

func (m *Manager) collect(ctx context.Context, target resolvedTarget) ([]string, int) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	warnings := []string{}
	files := 0
	root, err := os.OpenRoot(target.home)
	if err != nil {
		return []string{"无法读取目标用户的会话目录"}, 0
	}
	defer root.Close()
	m.mu.Lock()
	activations := append([]Activation(nil), m.state.Activations...)
	m.mu.Unlock()
	// Confine all reads to the user's home, including symlink traversal. We keep
	// only token counters; prompt text and model responses never enter the DB.
	remaining := int64(24 << 20)
	for _, base := range []string{".gemini/antigravity-cli/brain", ".gemini/antigravity/brain"} {
		dir, err := root.Open(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warnings = append(warnings, "无法读取 "+base)
			continue
		}
		entries, err := dir.ReadDir(-1)
		_ = dir.Close()
		if err != nil {
			warnings = append(warnings, "无法列出会话目录")
			continue
		}
		// Newest sessions first, so bounded scans keep live usage responsive.
		sort.Slice(entries, func(i, j int) bool {
			a, _ := entries[i].Info()
			b, _ := entries[j].Info()
			if a == nil || b == nil {
				return entries[i].Name() > entries[j].Name()
			}
			return a.ModTime().After(b.ModTime())
		})
		for _, entry := range entries {
			if ctx.Err() != nil {
				warnings = append(warnings, "采集已取消，历史数据仍保留")
				return warnings, files
			}
			if !entry.IsDir() {
				continue
			}
			if files >= 2000 || remaining <= 0 {
				warnings = append(warnings, "本次达到采集上限，剩余记录将在后续刷新时读取")
				return warnings, files
			}
			relative := filepath.Join(base, entry.Name(), ".system_generated", "logs", "transcript.jsonl")
			file, err := root.Open(relative)
			if os.IsNotExist(err) {
				relative = filepath.Join(base, entry.Name(), ".system_generated", "logs", "transcript_full.jsonl")
				file, err = root.Open(relative)
			}
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				warnings = appendUnique(warnings, "部分会话日志无法读取")
				continue
			}
			files++
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				_ = file.Close()
				continue
			}
			key := target.target.User + "/" + relative
			previous, err := m.usage.cursor(key)
			if err != nil {
				_ = file.Close()
				warnings = appendUnique(warnings, "无法读取采集进度")
				continue
			}
			if previous.Offset == info.Size() && previous.LastSize == info.Size() && previous.Modified.Equal(info.ModTime()) {
				_ = file.Close()
				continue
			}
			if info.Size() < previous.Offset || (info.Size() == previous.LastSize && !previous.Modified.Equal(info.ModTime())) {
				previous.Offset = 0
			}
			if _, err = file.Seek(previous.Offset, io.SeekStart); err != nil {
				_ = file.Close()
				continue
			}
			reader := bufio.NewReaderSize(io.LimitReader(file, remaining), 65536)
			records := []UsageRecord{}
			offset := previous.Offset
			bad := 0
			for {
				line, readErr := reader.ReadBytes('\n')
				if readErr != nil {
					break
				} // retain an incomplete last line for the next scan
				position := offset
				offset += int64(len(line))
				remaining -= int64(len(line))
				if len(line) > 4<<20 {
					bad++
					continue
				}
				var metadata map[string]json.RawMessage
				_ = json.Unmarshal(line, &metadata)
				fallbackID := entry.Name() + "/" + fmt.Sprint(position)
				if index := metadata["step_index"]; len(index) > 0 {
					fallbackID = entry.Name() + "/step/" + string(index)
				}
				record, found, parseErr := parseUsageObject(line, "unassigned", target.target.User, "transcript", fallbackID)
				if parseErr != nil {
					bad++
					continue
				}
				if !found {
					continue
				}
				// An explicit account email in the log takes precedence. Otherwise
				// retain attribution only within an AppForce-observed switch period.
				var email string
				_ = json.Unmarshal(metadata["account_email"], &email)
				if email != "" {
					record.AccountID = accountID(email)
				} else {
					record.AccountID = accountAt(activations, target.target, record.Time)
				}
				records = append(records, record)
			}
			_ = file.Close()
			if _, err = m.usage.append(records); err != nil {
				warnings = appendUnique(warnings, "保存 token 用量失败: "+err.Error())
				continue
			}
			if bad > 0 {
				warnings = appendUnique(warnings, "部分日志缺少有效的时间或 token 字段，已跳过；不会推算消耗")
			}
			if err = m.usage.saveCursor(key, cursor{offset, info.Size(), info.ModTime()}); err != nil {
				warnings = appendUnique(warnings, "保存采集进度失败")
			}
		}
	}
	return warnings, files
}

func (m *Manager) collectLoop() {
	defer close(m.collectorDone)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.collectorContext.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			targets := map[Target]struct{}{}
			for _, activation := range m.state.Activations {
				targets[activation.Target] = struct{}{}
			}
			m.mu.Unlock()
			for target := range targets {
				if resolved, err := resolveTarget(target); err == nil {
					m.collect(m.collectorContext, resolved)
				}
			}
		}
	}
}

func accountAt(history []Activation, target Target, at time.Time) string {
	result := "unassigned"
	for _, activation := range history {
		if activation.Target == target && !at.Before(activation.Time) {
			result = activation.AccountID
		}
	}
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// ObserveCapture records a verified native login from this point onward. Older
// transcripts remain unassigned rather than being charged to the current user.
func (m *Manager) ObserveCapture(target Target, id string) error {
	resolved, err := resolveTarget(target)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(m.state)
	for i := len(next.Activations) - 1; i >= 0; i-- {
		if next.Activations[i].Target == resolved.target {
			if next.Activations[i].AccountID == id {
				return nil
			}
			break
		}
	}
	next.Activations = append(next.Activations, Activation{resolved.target, id, time.Now()})
	if err = m.vault.save(next); err != nil {
		return err
	}
	m.state = next
	return nil
}

func (m *Manager) MarkExternalChange(target Target) error {
	return m.ObserveCapture(target, "unassigned")
}
