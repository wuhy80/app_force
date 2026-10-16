package wfp

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"appforce/internal/model"
)

type recordsKey struct{}

// WithRecords keeps each connection's opaque WFP records attached across the
// proxy protocol stack, down to the physical upstream socket's ControlContext.
func WithRecords(ctx context.Context, records []byte) context.Context {
	return context.WithValue(ctx, recordsKey{}, append([]byte(nil), records...))
}

func Records(ctx context.Context) []byte {
	b, _ := ctx.Value(recordsKey{}).([]byte)
	return b
}

type RouteFunc func(context.Context, net.Conn, Redirect)

type Server struct {
	mu          sync.Mutex
	device      io.Closer
	listeners   []net.Listener
	connections map[net.Conn]struct{}
	closed      bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

type driver interface {
	io.Closer
	Activate([]byte) error
}

// Start installs capture last. Every failure closes the exclusive device handle
// (which removes dynamic filters) before closing the proxy listeners.
func Start(ctx context.Context, rules []model.Rule, route RouteFunc, report func(error)) (*Server, error) {
	p, err := Compile(rules, applicationID)
	if err != nil {
		return nil, err
	}
	d, err := openDriver()
	if err != nil {
		return nil, err
	}
	return start(ctx, p, d, queryRedirect, route, report)
}

type redirectQuery func(net.Conn) ([]byte, []byte, error)

func start(ctx context.Context, p *Policy, d driver, query redirectQuery, route RouteFunc, report func(error)) (*Server, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{device: d, cancel: cancel, connections: make(map[net.Conn]struct{})}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	var ports [2]uint16
	for i, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		network := "tcp4"
		if i == 1 {
			network = "tcp6"
		}
		l, err := net.Listen(network, address)
		if err != nil {
			return nil, fmt.Errorf("WFP 必须同时支持 IPv4/IPv6: %w", err)
		}
		s.listeners = append(s.listeners, l)
		ports[i] = uint16(l.Addr().(*net.TCPAddr).Port)
		s.wg.Add(1)
		go s.accept(ctx, l, p, query, route, report)
	}
	data, err := p.Encode(uint64(os.Getpid()), ports[0], ports[1])
	if err != nil {
		return nil, err
	}
	if err := d.Activate(data); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func (s *Server) accept(ctx context.Context, l net.Listener, p *Policy, query redirectQuery, route RouteFunc, report func(error)) {
	defer s.wg.Done()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		t := &trackedConn{Conn: c, server: s}
		s.connections[t] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			data, records, err := query(c)
			if err != nil {
				_ = t.Close()
				report(err)
				return
			}
			r, err := p.DecodeContext(data)
			if err != nil {
				_ = t.Close()
				report(err)
				return
			}
			route(WithRecords(ctx, records), t, r)
		}()
	}
}

type trackedConn struct {
	net.Conn
	server *Server
	once   sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.server.mu.Lock(); delete(c.server.connections, c); c.server.mu.Unlock() })
	return err
}

func (s *Server) Close() error {
	s.closeOnce.Do(s.close)
	return s.closeErr
}

func (s *Server) close() {
	// Device cleanup synchronously removes filters and waits for callouts.
	s.closeErr = s.device.Close()
	s.cancel()
	s.mu.Lock()
	s.closed = true
	connections := make([]net.Conn, 0, len(s.connections))
	for c := range s.connections {
		connections = append(connections, c)
	}
	s.mu.Unlock()
	for _, l := range s.listeners {
		_ = l.Close()
	}
	for _, c := range connections {
		_ = c.Close()
	}
	s.wg.Wait()
}
