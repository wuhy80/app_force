package api

import (
	"net/http"
	"testing"
)

func TestStartupRequiresAuthenticationAndExplicitBoolean(t *testing.T) {
	a, handler := setup(t)
	if w := call(handler, "PUT", "/api/startup", "", `{"enabled":true}`); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated startup change accepted")
	}
	for _, body := range []string{`{}`, `null`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":true,"unknown":true}`, `{"enabled":true} {}`} {
		if w := call(handler, "PUT", "/api/startup", a.Token, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid startup body accepted: %s", body)
		}
	}
	before := a.Snapshot()
	if before.Startup.Supported || before.Startup.Enabled {
		t.Fatal("unconfigured startup manager advertised support or enabled startup")
	}
	if w := call(handler, "PUT", "/api/startup", a.Token, `{"enabled":true}`); w.Code != http.StatusBadRequest {
		t.Fatal("unsupported environment accepted startup")
	}
	if after := a.Snapshot(); after.Settings != before.Settings || after.Engine.Running || after.Startup.Enabled {
		t.Fatal("startup request changed proxy configuration or state")
	}
}
