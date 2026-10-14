package api

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"appforce/internal/model"
	"appforce/internal/traffic"
)

func TestTrafficAuthenticationValidationAndConfiguredApplications(t *testing.T) {
	a, handler := setup(t)
	if call(handler, "GET", "/api/traffic", "", "").Code != 401 {
		t.Fatal("traffic history exposed without authentication")
	}
	for _, query := range []string{"days=0", "days=366", "days=7.5", "days=bad", "include_direct=anything", "app=missing"} {
		if response := call(handler, "GET", "/api/traffic?"+query, a.Token, ""); response.Code != 400 {
			t.Fatalf("accepted %s: %s", query, response.Body.String())
		}
	}
	programPath := filepath.Join(t.TempDir(), "saved-program")
	if err := a.SaveRule(model.Rule{Name: "Saved application", Path: programPath, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	response := call(handler, "GET", "/api/traffic?days=30", a.Token, "")
	var report traffic.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(report.History) != 30 || len(report.Applications) != 1 || report.Applications[0].Name != "Saved application" || report.IncludeDirect {
		t.Fatalf("unexpected report: %+v", report)
	}
	response = call(handler, "GET", "/api/traffic?days=1&include_direct=true&app="+traffic.Identity(programPath), a.Token, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Days != 1 || !report.IncludeDirect || len(report.History) != 1 || len(report.HourlyHistory) == 0 || report.HourlyHistory[len(report.HourlyHistory)-1].Hour != report.GeneratedAt.Format("2006-01-02T15:00") {
		t.Fatalf("today response missing local hourly history: %+v", report)
	}
}
