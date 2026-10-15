package antigravity

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAccountNetworkErrorsExplainProxyWithoutLeakingCredentials(t *testing.T) {
	for _, test := range []struct {
		failure error
		want    string
	}{
		{&NetworkError{Message: "代理未启动，请先启动代理"}, "代理未启动"},
		{fmt.Errorf("dial error containing refresh-secret and authorization-code"), "oauth2.googleapis.com"},
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.failure })}
		err := requestJSON(context.Background(), client, "POST", "https://oauth2.googleapis.com/token?code=authorization-code", "access-secret", strings.NewReader("refresh-secret"), "application/x-www-form-urlencoded", nil)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatal("missing actionable proxy error", err)
		}
		for _, secret := range []string{"refresh-secret", "authorization-code", "access-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatal("network error exposed OAuth credentials")
			}
		}
	}
}

type oauthBrowserCloser func() error

func (c oauthBrowserCloser) Close() error { return c() }

func TestOAuthBrowserProxyClosesWhenAuthorizationEnds(t *testing.T) {
	m := newManager(t, t.TempDir())
	session, err := m.StartOAuth()
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	if err := m.OpenOAuthBrowser(session.ID, func(address string) (io.Closer, error) {
		if address != session.URL {
			t.Fatal("browser did not receive original authorization URL")
		}
		return oauthBrowserCloser(func() error { closed = true; return nil }), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelOAuth(session.ID); err != nil || !closed {
		t.Fatal("cancelling login did not close browser proxy", err)
	}
	called := false
	if err := m.OpenOAuthBrowser(session.ID, func(string) (io.Closer, error) { called = true; return nil, nil }); err == nil || called {
		t.Fatal("ended session could open a new browser")
	}
}
