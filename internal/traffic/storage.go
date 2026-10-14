package traffic

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "github.com/sagernet/bbolt"
)

var appsBucket = []byte("applications-v1")
var daysBucket = []byte("daily-v1")
var hoursBucket = []byte("hourly-v1")
var metaBucket = []byte("metadata-v1")

func (r *Recorder) database(fn func(*bolt.DB) error) error {
	db, err := bolt.Open(r.file, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	err = fn(db)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *Recorder) load() error {
	return r.database(func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{appsBucket, daysBucket, hoursBucket, metaBucket} {
				if _, err := tx.CreateBucketIfNotExists(name); err != nil {
					return err
				}
			}
			meta := tx.Bucket(metaBucket)
			if value := meta.Get([]byte("since")); value != nil {
				if err := json.Unmarshal(value, &r.since); err != nil {
					return err
				}
			} else {
				value, _ := json.Marshal(r.since)
				if err := meta.Put([]byte("since"), value); err != nil {
					return err
				}
			}
			if value := meta.Get([]byte("saved_at")); value != nil {
				if err := json.Unmarshal(value, &r.savedAt); err != nil {
					return err
				}
			}
			if err := tx.Bucket(appsBucket).ForEach(func(key, value []byte) error {
				var a appRecord
				if err := json.Unmarshal(value, &a); err != nil {
					return err
				}
				a.last = a.Totals
				r.apps[string(key)] = &a
				return nil
			}); err != nil {
				return err
			}
			for _, data := range []struct {
				bucket []byte
				rows   map[string][2]Totals
			}{{daysBucket, r.days}, {hoursBucket, r.hours}} {
				if err := tx.Bucket(data.bucket).ForEach(func(key, value []byte) error {
					var totals [2]Totals
					if err := json.Unmarshal(value, &totals); err != nil {
						return err
					}
					data.rows[string(key)] = totals
					return nil
				}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// Flush snapshots only changed rows. On failure it requeues them; counters continue in memory.
// The main application's process lock guarantees one writer per data directory.
func (r *Recorder) Flush() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	apps := make(map[string]appRecord, len(r.dirtyApps))
	days := make(map[string][2]Totals, len(r.dirtyDays))
	hours := make(map[string][2]Totals, len(r.dirtyHours))
	for id := range r.dirtyApps {
		apps[id] = *r.apps[id]
	}
	for key := range r.dirtyDays {
		days[key] = r.days[key]
	}
	for key := range r.dirtyHours {
		hours[key] = r.hours[key]
	}
	r.dirtyApps, r.dirtyDays = make(map[string]bool), make(map[string]bool)
	r.dirtyHours = make(map[string]bool)
	r.mu.Unlock()
	if len(apps) == 0 && len(days) == 0 && len(hours) == 0 {
		return nil
	}
	saved := r.now()
	err := r.database(func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			for _, bucket := range [][]byte{appsBucket, daysBucket, hoursBucket, metaBucket} {
				if tx.Bucket(bucket) == nil {
					return fmt.Errorf("流量数据库缺少数据表")
				}
			}
			for id, a := range apps {
				value, err := json.Marshal(a)
				if err != nil {
					return err
				}
				if err := tx.Bucket(appsBucket).Put([]byte(id), value); err != nil {
					return err
				}
			}
			for _, data := range []struct {
				bucket []byte
				rows   map[string][2]Totals
			}{{daysBucket, days}, {hoursBucket, hours}} {
				for key, totals := range data.rows {
					value, err := json.Marshal(totals)
					if err != nil {
						return err
					}
					if err := tx.Bucket(data.bucket).Put([]byte(key), value); err != nil {
						return err
					}
				}
			}
			value, _ := json.Marshal(saved)
			return tx.Bucket(metaBucket).Put([]byte("saved_at"), value)
		})
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		for id := range apps {
			r.dirtyApps[id] = true
		}
		for key := range days {
			r.dirtyDays[key] = true
		}
		for key := range hours {
			r.dirtyHours[key] = true
		}
		r.storageError = fmt.Sprintf("流量历史保存失败，将自动重试：%v", err)
		return err
	}
	r.savedAt, r.storageError = &saved, ""
	return nil
}
