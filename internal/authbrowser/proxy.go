package authbrowser

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type DialContext func(context.Context, string, string) (net.Conn, error)

type proxy struct {
	address string
	server  *http.Server
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	tunnels map[net.Conn]bool
}

func newProxy(dial DialContext) (*proxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proxy{address: listener.Addr().String(), cancel: cancel, tunnels: map[net.Conn]bool{}}
	p.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	p.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "Only HTTPS authorization is supported", http.StatusMethodNotAllowed)
			return
		}
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || host == "" || port != "443" {
			http.Error(w, "Invalid HTTPS destination", http.StatusBadRequest)
			return
		}
		dialCtx, dialCancel := context.WithTimeout(r.Context(), 20*time.Second)
		upstream, err := dial(dialCtx, "tcp", r.Host)
		dialCancel()
		if err != nil {
			http.Error(w, "Antigravity proxy connection failed", http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.tunnels[client], p.tunnels[upstream] = true, true
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			delete(p.tunnels, client)
			delete(p.tunnels, upstream)
			p.mu.Unlock()
		}()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := buffer.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffer)
			_ = upstream.Close()
			close(done)
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	})
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *proxy) Close() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.cancel()
		for connection := range p.tunnels {
			_ = connection.Close()
		}
	}
	p.mu.Unlock()
	return p.server.Close()
}
