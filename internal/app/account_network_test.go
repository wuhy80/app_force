package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"appforce/internal/antigravity"
	"appforce/internal/model"
)

func TestAccountNodeFollowsAntigravityOrDefault(t *testing.T) {
	defaultNode, pinned := model.Node{ID: "default"}, model.Node{ID: "antigravity"}
	for _, mode := range []string{"tun", "mixed"} {
		for _, test := range []struct {
			name, path, node, want string
			disabled, wantError    bool
		}{
			{name: "no rule", want: "default"},
			{name: "unrelated rule", path: `C:\Apps\other.exe`, node: "antigravity", want: "default"},
			{name: "follow default", path: `C:\Apps\Antigravity.exe`, want: "default"},
			{name: "Windows explicit", path: `C:\Apps\ANTIGRAVITY.EXE`, node: "antigravity", want: "antigravity"},
			{name: "macOS explicit", path: "/Applications/Antigravity.app", node: "antigravity", want: "antigravity"},
			{name: "Linux CLI explicit", path: "/usr/local/bin/agy", node: "antigravity", want: "antigravity"},
			{name: "disabled", path: `C:\Apps\Antigravity.exe`, node: "antigravity", disabled: true, want: "default"},
			{name: "missing explicit node", path: `C:\Apps\Antigravity.exe`, node: "removed", wantError: true},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				c := model.DefaultConfig()
				c.Settings.Mode, c.Nodes, c.SelectedNodeID = mode, []model.Node{defaultNode, pinned}, defaultNode.ID
				if test.path != "" {
					c.Rules = []model.Rule{{Path: test.path, Enabled: !test.disabled, NodeID: test.node}}
				}
				node, err := accountNode(c)
				if (err != nil) != test.wantError || err == nil && node.ID != test.want {
					t.Fatalf("got node %s, error %v; want %s, error %t", node.ID, err, test.want, test.wantError)
				}
			})
		}
	}
}

// A real HTTP CONNECT node forwards encrypted Google-fixture traffic. Counts
// prove which node carried OAuth, identity and quota requests, without Google.
func accountProxyFixture(t *testing.T, target string) (model.Node, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		upstream, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		defer upstream.Close()
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		count.Add(1)
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); close(done) }()
		_, _ = io.Copy(connection, upstream)
		_ = connection.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	address := proxy.Listener.Addr().(*net.TCPAddr)
	node, err := model.NewNode("fixture", "test", map[string]any{"type": "http", "server": "127.0.0.1", "server_port": address.Port})
	if err != nil {
		t.Fatal(err)
	}
	return node, count
}

func TestOAuthIdentityRefreshAndQuotaUseTheSameConfiguredNode(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("explicit=", explicit), func(t *testing.T) {
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/token":
					_, _ = io.WriteString(w, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}`)
				case r.URL.Path == "/oauth2/v2/userinfo":
					_, _ = io.WriteString(w, `{"email":"proxy@example.com","verified_email":true}`)
				case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
					_, _ = io.WriteString(w, `{"cloudaicompanionProject":"fixture"}`)
				default:
					_, _ = io.WriteString(w, `{"groups":[{"displayName":"Gemini","buckets":[{"id":"weekly","remainingFraction":0.5}]}]}`)
				}
			}))
			defer provider.Close()
			defaultNode, defaultCount := accountProxyFixture(t, provider.Listener.Addr().String())
			pinnedNode, pinnedCount := accountProxyFixture(t, provider.Listener.Addr().String())
			a, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			a.Accounts.Close()
			accounts, err := antigravity.Open(filepath.Join(t.TempDir(), "accounts"), func() *http.Client {
				client := a.accountClient()
				transport := client.Transport.(*http.Transport)
				transport.TLSClientConfig = provider.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				transport.TLSClientConfig.ServerName = "127.0.0.1"
				return client
			})
			if err != nil {
				t.Fatal(err)
			}
			a.Accounts = accounts
			defer accounts.Close()
			defer a.Stop()
			a.config.Nodes, a.config.SelectedNodeID = []model.Node{defaultNode, pinnedNode}, defaultNode.ID
			a.config.Settings.Mode = "mixed"
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			a.config.Settings.MixedPort = listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			rule := model.Rule{Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true}
			if explicit {
				rule.NodeID = pinnedNode.ID
			}
			a.config.Rules = []model.Rule{rule}
			if err := a.Start(); err != nil {
				t.Fatal(err)
			}
			session, err := a.StartAccountOAuth()
			if err != nil {
				t.Fatal(err)
			}
			authorization, _ := url.Parse(session.URL)
			query := authorization.Query()
			response, err := http.Get(query.Get("redirect_uri") + "?" + url.Values{"state": {query.Get("state")}, "code": {"fixture-code"}}.Encode())
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			view, _ := accounts.OAuthStatus(session.ID)
			if view.Status != "succeeded" || len(accounts.List()) != 1 {
				t.Fatalf("OAuth through application node failed: %s", view.Error)
			}
			if _, err := accounts.RefreshQuota(context.Background(), accounts.List()[0].ID); err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.ImportCredential(context.Background(), "", antigravity.Credential{Token: antigravity.Token{AccessToken: "old", RefreshToken: "fixture-refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour)}, AuthMethod: "consumer"}); err != nil {
				t.Fatal(err)
			}
			used, unused := defaultCount.Load(), pinnedCount.Load()
			if explicit {
				used, unused = unused, used
			}
			if used < 6 || unused != 0 {
				t.Fatalf("wrong proxy route: selected node connections=%d, other node=%d", used, unused)
			}
			if err := a.Stop(); err != nil {
				t.Fatal(err)
			}
			if _, err := a.StartAccountOAuth(); err == nil || !strings.Contains(err.Error(), "代理未启动") {
				t.Fatal("stopped proxy allowed browser authorization")
			}
			if _, err := accounts.RefreshQuota(context.Background(), accounts.List()[0].ID); err == nil || !strings.Contains(err.Error(), "代理未启动") {
				t.Fatal("stopped proxy did not produce actionable quota error", err)
			}
		})
	}
}
