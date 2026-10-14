// Package traffic records application traffic independently of the connection display.
package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type Totals struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

func (t *Totals) add(other Totals) { t.Upload += other.Upload; t.Download += other.Download }

type Metrics struct {
	Totals
	LifetimeUpload    int64   `json:"lifetime_upload"`
	LifetimeDownload  int64   `json:"lifetime_download"`
	UploadRate        float64 `json:"upload_rate"`
	DownloadRate      float64 `json:"download_rate"`
	ActiveConnections int64   `json:"active_connections"`
}

type Application struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Metrics
}

type Day struct {
	Date string `json:"date"`
	Totals
}

type Hour struct {
	// A local calendar hour on the recording device, without browser timezone conversion.
	Hour string `json:"hour"`
	Totals
}

type Report struct {
	GeneratedAt    time.Time     `json:"generated_at"`
	CollectedSince time.Time     `json:"collected_since"`
	SavedAt        *time.Time    `json:"saved_at"`
	StorageError   string        `json:"storage_error"`
	Days           int           `json:"days"`
	IncludeDirect  bool          `json:"include_direct"`
	Applications   []Application `json:"applications"`
	Summary        Metrics       `json:"summary"`
	History        []Day         `json:"history"`
	HourlyHistory  []Hour        `json:"hourly_history,omitempty"`
	// Older daily records cannot be assigned to an hour after upgrading.
	HourlyUnallocated Totals `json:"hourly_unallocated"`
}

type appRecord struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Totals [2]Totals `json:"totals"` // proxy, direct
	active [2]int64
	last   [2]Totals
	rates  [2][2]float64
}

type Recorder struct {
	mu           sync.Mutex
	flushMu      sync.Mutex
	file         string
	now          func() time.Time
	apps         map[string]*appRecord
	days         map[string][2]Totals // local calendar date + NUL + stable application ID
	hours        map[string][2]Totals // local calendar hour + NUL + stable application ID
	dirtyApps    map[string]bool
	dirtyDays    map[string]bool
	dirtyHours   map[string]bool
	since        time.Time
	savedAt      *time.Time
	storageError string
	lastSample   time.Time
}

// Open uses short database transactions, so an idle recorder holds no file lock.
func Open(file string) (*Recorder, error) {
	r := &Recorder{file: file, now: time.Now, apps: make(map[string]*appRecord), days: make(map[string][2]Totals), hours: make(map[string][2]Totals),
		dirtyApps: make(map[string]bool), dirtyDays: make(map[string]bool), dirtyHours: make(map[string]bool), since: time.Now(), lastSample: time.Now()}
	if err := r.load(); err != nil {
		return nil, fmt.Errorf("读取流量历史失败: %w", err)
	}
	return r, nil
}

// Identity deliberately excludes PID and rule ID. Windows spelling/case variants share history.
func Identity(programPath string) string {
	canonical := strings.ReplaceAll(programPath, `\`, "/")
	if (len(canonical) > 2 && canonical[1] == ':') || strings.HasPrefix(canonical, "//") {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:16])
}

func (r *Recorder) Register(name, programPath string) string {
	id := Identity(programPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" {
		name = strings.TrimSuffix(path.Base(strings.ReplaceAll(programPath, `\`, "/")), ".app")
	}
	a := r.apps[id]
	if a == nil {
		a = &appRecord{ID: id, Name: name, Path: programPath}
		r.apps[id] = a
	} else if a.Name == name && a.Path == programPath {
		return id
	}
	a.Name, a.Path = name, programPath
	r.dirtyApps[id] = true
	return id
}

func scope(proxied bool) int {
	if proxied {
		return 0
	}
	return 1
}

func (r *Recorder) count(id string, proxied bool, upload, download int64) {
	if upload <= 0 && download <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.apps[id]
	if a == nil {
		return
	}
	value := Totals{max(upload, 0), max(download, 0)}
	index := scope(proxied)
	a.Totals[index].add(value)
	now := r.now()
	key := now.Format("2006-01-02") + "\x00" + id
	day := r.days[key]
	day[index].add(value)
	r.days[key] = day
	r.dirtyApps[id], r.dirtyDays[key] = true, true
	hourKey := now.Format("2006-01-02T15:00") + "\x00" + id
	hour := r.hours[hourKey]
	hour[index].add(value)
	r.hours[hourKey], r.dirtyHours[hourKey] = hour, true
}

func (r *Recorder) active(id string, proxied bool, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.apps[id]; a != nil {
		i := scope(proxied)
		a.active[i] = max(0, a.active[i]+delta)
	}
}

// Sample is called once a second independently of UI requests.
func (r *Recorder) Sample() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	elapsed := now.Sub(r.lastSample).Seconds()
	if elapsed <= 0 {
		return
	}
	for _, a := range r.apps {
		for i := range a.Totals {
			a.rates[i] = [2]float64{float64(a.Totals[i].Upload-a.last[i].Upload) / elapsed, float64(a.Totals[i].Download-a.last[i].Download) / elapsed}
			a.last[i] = a.Totals[i]
		}
	}
	r.lastSample = now
}

func (r *Recorder) resetLive() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.apps {
		a.active = [2]int64{}
		a.rates = [2][2]float64{}
		a.last = a.Totals
	}
	r.lastSample = r.now()
}

func (r *Recorder) Report(days int, appID string, includeDirect bool) (Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if days < 1 || days > 365 {
		return Report{}, fmt.Errorf("统计天数应在 1 到 365 之间")
	}
	if appID != "" && r.apps[appID] == nil {
		return Report{}, fmt.Errorf("统计应用不存在")
	}
	now := r.now()
	report := Report{GeneratedAt: now, CollectedSince: r.since, StorageError: r.storageError,
		Days: days, IncludeDirect: includeDirect, Applications: []Application{}, History: make([]Day, days)}
	if r.savedAt != nil {
		saved := *r.savedAt
		report.SavedAt = &saved
	}
	end := 1
	if includeDirect {
		end = 2
	}
	periods := make(map[string]Totals)
	// Calendar arithmetic preserves midnight boundaries and DST transitions on the recording device.
	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, i-days+1).Format("2006-01-02")
		report.History[i].Date = date
	}
	dayIndexes := make(map[string]int, days)
	for i, day := range report.History {
		dayIndexes[day.Date] = i
	}
	for key, totals := range r.days {
		date, id, _ := strings.Cut(key, "\x00")
		i, inPeriod := dayIndexes[date]
		if !inPeriod {
			continue
		}
		period := periods[id]
		for s := 0; s < end; s++ {
			period.add(totals[s])
			if appID == "" || appID == id {
				report.History[i].Totals.add(totals[s])
			}
		}
		periods[id] = period
	}
	for id, a := range r.apps {
		metrics := Metrics{Totals: periods[id]}
		for s := 0; s < end; s++ {
			metrics.LifetimeUpload += a.Totals[s].Upload
			metrics.LifetimeDownload += a.Totals[s].Download
			metrics.UploadRate += a.rates[s][0]
			metrics.DownloadRate += a.rates[s][1]
			metrics.ActiveConnections += a.active[s]
		}
		// Direct-only applications appear when the user explicitly includes direct traffic.
		if !includeDirect && a.Totals[0] == (Totals{}) && a.active[0] == 0 && a.Totals[1] != (Totals{}) {
			continue
		}
		report.Applications = append(report.Applications, Application{ID: id, Name: a.Name, Path: a.Path, Metrics: metrics})
		if appID == "" || appID == id {
			report.Summary.Totals.add(metrics.Totals)
			report.Summary.LifetimeUpload += metrics.LifetimeUpload
			report.Summary.LifetimeDownload += metrics.LifetimeDownload
			report.Summary.UploadRate += metrics.UploadRate
			report.Summary.DownloadRate += metrics.DownloadRate
			report.Summary.ActiveConnections += metrics.ActiveConnections
		}
	}
	sort.Slice(report.Applications, func(i, j int) bool {
		a, b := report.Applications[i], report.Applications[j]
		if a.Upload+a.Download != b.Upload+b.Download {
			return a.Upload+a.Download > b.Upload+b.Download
		}
		return a.ID < b.ID
	})
	if days == 1 {
		var allocated Totals
		report.HourlyHistory, allocated = r.todayHours(now, appID, end)
		report.HourlyUnallocated = Totals{
			Upload:   max(0, report.Summary.Upload-allocated.Upload),
			Download: max(0, report.Summary.Download-allocated.Download),
		}
	}
	return report, nil
}

// todayHours runs with mu held. Each bar groups a local clock hour; repeated hours
// at the end of daylight saving time share a bar, just as they share a daily total.
func (r *Recorder) todayHours(now time.Time, appID string, scopeEnd int) ([]Hour, Totals) {
	date := now.Format("2006-01-02")
	indexes := make(map[string]int, 24)
	for hour := 0; hour <= now.Hour(); hour++ {
		indexes[fmt.Sprintf("%sT%02d:00", date, hour)] = hour
	}
	var buckets [24]Totals
	for key, totals := range r.hours {
		hour, id, _ := strings.Cut(key, "\x00")
		index, today := indexes[hour]
		if !today || (appID != "" && appID != id) {
			continue
		}
		for s := 0; s < scopeEnd; s++ {
			buckets[index].add(totals[s])
		}
	}
	// Before 08:00, show the current hour or the earliest traffic, without future bars.
	start := min(8, now.Hour())
	for hour := 0; hour < start; hour++ {
		if buckets[hour] != (Totals{}) {
			start = hour
			break
		}
	}
	history := make([]Hour, 0, now.Hour()-start+1)
	var allocated Totals
	for hour := start; hour <= now.Hour(); hour++ {
		history = append(history, Hour{Hour: fmt.Sprintf("%sT%02d:00", date, hour), Totals: buckets[hour]})
		allocated.add(buckets[hour])
	}
	return history, allocated
}
