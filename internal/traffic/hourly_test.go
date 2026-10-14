package traffic

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	bolt "github.com/sagernet/bbolt"
)

func TestHourlyHistoryStartsAtEightOrEarlierTraffic(t *testing.T) {
	for _, test := range []struct {
		name        string
		nowHour     int
		trafficHour int
		startHour   int
	}{
		{"empty afternoon", 14, -1, 8},
		{"first traffic after eight", 14, 11, 8},
		{"first traffic before eight", 14, 5, 5},
		{"traffic at eight", 14, 8, 8},
		{"empty early morning", 6, -1, 6},
		{"early morning traffic", 6, 2, 2},
		{"midnight", 0, 0, 0},
		{"last hour of day", 23, -1, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := openTest(t)
			id := r.Register("app", "/app")
			date := time.Date(2026, 10, 14, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
			now := date.Add(time.Duration(test.trafficHour)*time.Hour + 15*time.Minute)
			r.now = func() time.Time { return now }
			if test.trafficHour >= 0 {
				r.count(id, true, 10, 20)
			}
			now = date.Add(time.Duration(test.nowHour)*time.Hour + 30*time.Minute)
			report := reportTest(t, r, 1, "", false)
			if len(report.HourlyHistory) != test.nowHour-test.startHour+1 {
				t.Fatalf("wrong hour range: %+v", report.HourlyHistory)
			}
			for i, hour := range report.HourlyHistory {
				if want := fmt.Sprintf("2026-10-14T%02d:00", test.startHour+i); hour.Hour != want {
					t.Fatalf("non-contiguous local hours: got %s, want %s", hour.Hour, want)
				}
				want := Totals{}
				if test.startHour+i == test.trafficHour {
					want = Totals{10, 20}
				}
				if hour.Totals != want {
					t.Fatalf("wrong totals for %s: got %+v, want %+v", hour.Hour, hour.Totals, want)
				}
			}
		})
	}
}

func TestHourlyHistoryUsesDeviceCalendarAndFiltersAfterReopen(t *testing.T) {
	r := openTest(t)
	a, b := r.Register("A", "/a"), r.Register("B", "/b")
	// A non-integral UTC offset also catches accidental UTC truncation of hours.
	zone := time.FixedZone("NPT", 5*3600+45*60)
	now := time.Date(2026, 10, 13, 23, 59, 59, 999999999, zone)
	r.now = func() time.Time { return now }
	r.count(a, true, 1000, 2000)
	now = time.Date(2026, 10, 14, 1, 15, 0, 0, zone)
	r.count(a, false, 100, 200)
	now = time.Date(2026, 10, 14, 4, 10, 0, 0, zone)
	r.count(b, true, 5, 6)
	now = time.Date(2026, 10, 14, 6, 59, 59, 999999999, zone)
	r.count(a, true, 10, 20)
	now = now.Add(time.Nanosecond)
	r.count(a, true, 3, 4)
	now = time.Date(2026, 10, 14, 9, 0, 0, 0, zone)
	r.count(a, true, 0, 30)
	now = time.Date(2026, 10, 14, 10, 20, 0, 0, zone)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = r.now
	for _, test := range []struct {
		name   string
		app    string
		direct bool
		start  int
		total  Totals
	}{
		{"all proxy", "", false, 4, Totals{18, 60}},
		{"A proxy", a, false, 6, Totals{13, 54}},
		{"B proxy", b, false, 4, Totals{5, 6}},
		{"A with direct", a, true, 1, Totals{113, 254}},
		{"all with direct", "", true, 1, Totals{118, 260}},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := reportTest(t, reopened, 1, test.app, test.direct)
			before := reportTest(t, r, 1, test.app, test.direct)
			if !reflect.DeepEqual(report.HourlyHistory, before.HourlyHistory) {
				t.Fatalf("hourly history changed after reopen: %+v / %+v", report.HourlyHistory, before.HourlyHistory)
			}
			if len(report.HourlyHistory) != 11-test.start || report.HourlyHistory[0].Hour != fmt.Sprintf("2026-10-14T%02d:00", test.start) {
				t.Fatalf("filter chose wrong start hour: %+v", report.HourlyHistory)
			}
			var total Totals
			for _, hour := range report.HourlyHistory {
				total.add(hour.Totals)
				if hour.Hour == "2026-10-14T08:00" && hour.Totals != (Totals{}) {
					t.Fatalf("idle hour not zero: %+v", hour)
				}
				if test.app == a && !test.direct {
					switch hour.Hour {
					case "2026-10-14T06:00":
						if hour.Totals != (Totals{10, 20}) {
							t.Fatalf("bytes before hour boundary misplaced: %+v", hour)
						}
					case "2026-10-14T07:00":
						if hour.Totals != (Totals{3, 4}) {
							t.Fatalf("bytes after hour boundary misplaced: %+v", hour)
						}
					}
				}
			}
			if total != test.total || report.Summary.Totals != total || report.History[0].Totals != total || report.HourlyUnallocated != (Totals{}) {
				t.Fatalf("hourly and daily filters disagree: hourly=%+v, report=%+v", total, report)
			}
		})
	}
	week := reportTest(t, reopened, 7, a, false)
	if len(week.History) != 7 || len(week.HourlyHistory) != 0 || week.History[5].Totals != (Totals{1000, 2000}) || week.History[6].Totals != (Totals{13, 54}) {
		t.Fatalf("daily history changed: %+v", week)
	}
}

func TestHourlyUpgradePreservesDailyTotalsWithoutInventingHours(t *testing.T) {
	r := openTest(t)
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.FixedZone("CST", 8*3600))
	r.now = func() time.Time { return now }
	id := r.Register("legacy", "/legacy")
	r.count(id, true, 10, 20)
	r.count(id, false, 100, 200)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// An older database has application/daily counters but no hourly bucket.
	if err := r.database(func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(hoursBucket) })
	}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.now = r.now
	before := reportTest(t, upgraded, 1, id, false)
	if before.Summary.Totals != (Totals{10, 20}) || before.HourlyUnallocated != (Totals{10, 20}) || len(before.HourlyHistory) != 1 || before.HourlyHistory[0].Totals != (Totals{}) {
		t.Fatalf("legacy daily history changed or fabricated hours: %+v", before)
	}
	now = now.Add(time.Hour)
	upgraded.count(id, true, 3, 4)
	if err := upgraded.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = r.now
	for _, direct := range []bool{false, true} {
		report := reportTest(t, reopened, 1, id, direct)
		wantOld := Totals{10, 20}
		if direct {
			wantOld = Totals{110, 220}
		}
		if len(report.HourlyHistory) != 1 || report.HourlyHistory[0].Hour != "2026-10-14T07:00" || report.HourlyHistory[0].Totals != (Totals{3, 4}) || report.HourlyUnallocated != wantOld || report.Summary.Upload != wantOld.Upload+3 || report.Summary.Download != wantOld.Download+4 {
			t.Fatalf("legacy and new counters diverged: %+v", report)
		}
	}
}
