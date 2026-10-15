package authbrowser

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthorizationBrowserUsesExplicitProxyAndPrivateProfile(t *testing.T) {
	args := strings.Join(browserArgs("chrome", "https://accounts.google.com/auth?state=fixture", "/temporary/profile", "127.0.0.1:17899"), "\n")
	for _, required := range []string{"--user-data-dir=/temporary/profile", "--incognito", "--proxy-server=http://127.0.0.1:17899", "--proxy-bypass-list=localhost;127.0.0.1;[::1]"} {
		if !strings.Contains(args, required) {
			t.Fatalf("missing authorization isolation argument: %s", required)
		}
	}
	edgeArgs := strings.Join(browserArgs("msedge.exe", "https://accounts.google.com/auth", "/temporary/profile", "127.0.0.1:17899"), "\n")
	if !strings.Contains(edgeArgs, "--inprivate") {
		t.Fatal("Edge authorization must use InPrivate")
	}
}

func TestAuthorizationHTTPSUsesSuppliedNodeAndClosesExistingTunnels(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "authorized through application node")
	}))
	defer target.Close()
	var calls atomic.Int32
	p, err := newProxy(func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "accounts.google.test:443" {
			t.Errorf("wrong authorization destination: %s %s", network, address)
		}
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	address, _ := url.Parse("http://" + p.address)
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.Proxy = http.ProxyURL(address)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("https://accounts.google.test/authorization")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "authorized through application node" || calls.Load() != 1 {
		t.Fatal("authorization traffic did not use supplied node")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get("https://accounts.google.test/authorization"); err == nil {
		response.Body.Close()
		t.Fatal("ended authorization left a usable proxy tunnel")
	}
}

func TestClosingAuthorizationCancelsPendingProxyDial(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	p, err := newProxy(func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		address, _ := url.Parse("http://" + p.address)
		transport := &http.Transport{Proxy: http.ProxyURL(address)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		if response, err := client.Get("https://accounts.google.test/"); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("authorization proxy never attempted connection")
	}
	_ = p.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("closing proxy left its dial running")
	}
	<-finished
}
