package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentEndpointsRequireAuthenticationAndRejectInvalidInput(t *testing.T) {
	a, handler := setup(t)
	for _, row := range []struct{ method, path string }{
		{"GET", "/api/deployment/platforms"}, {"POST", "/api/deployment/check"}, {"POST", "/api/deployment/jobs"}, {"GET", "/api/deployment/jobs/missing"},
	} {
		if got := call(handler, row.method, row.path, "", "{}").Code; got != http.StatusUnauthorized {
			t.Fatalf("%s exposed: %d", row.path, got)
		}
	}
	for _, path := range []string{"/api/deployment/check", "/api/deployment/jobs"} {
		for _, body := range []string{`{}`, `{"name":"test","host":"https://invalid","username":"u","password":"secret"}`, `{"password":"secret","unexpected":true}`} {
			response := call(handler, "POST", path, a.Token, body)
			if response.Code != 400 || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("invalid deploy input accepted or leaked: %s", response.Body.String())
			}
		}
	}
	if got := call(handler, "GET", "/api/deployment/jobs/missing", a.Token, "").Code; got != 404 {
		t.Fatalf("unknown job status: %d", got)
	}
	if got := call(handler, "GET", "/api/deployment/platforms", a.Token, "").Code; got != 200 {
		t.Fatalf("platform discovery failed: %d", got)
	}
}
