package wfp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDriver struct {
	activate func([]byte) error
	close    func() error
}

func (d fakeDriver) Activate(b []byte) error { return d.activate(b) }
func (d fakeDriver) Close() error            { return d.close() }

func TestServerStartsListenersBeforeCaptureAndRemovesCaptureFirst(t *testing.T) {
	p := testPolicy(t)
	var client net.Conn
	var removed atomic.Bool
	routed := make(chan struct{}, 2)
	d := fakeDriver{activate: func(b []byte) error {
		for _, offset := range []int{24, 26} {
			address := fmt.Sprintf("127.0.0.1:%d", binary.LittleEndian.Uint16(b[offset:]))
			if offset == 26 {
				address = fmt.Sprintf("[::1]:%d", binary.LittleEndian.Uint16(b[offset:]))
			}
			c, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				return fmt.Errorf("capture preceded listener readiness: %w", err)
			}
			if client != nil {
				_ = client.Close()
			}
			client = c
		}
		return nil
	}, close: func() error {
		// Device removal must happen while listeners/connections still exist.
		if client == nil {
			t.Error("listeners not started")
		}
		removed.Store(true)
		return nil
	}}
	s, err := start(context.Background(), p, d, func(net.Conn) ([]byte, []byte, error) {
		return contextBytes(p, 1, p.Rules[0].AppID, false), []byte("opaque"), nil
	},
		func(ctx context.Context, c net.Conn, r Redirect) {
			if string(Records(ctx)) != "opaque" || r.PID != 1234 {
				t.Error("lost redirect identity or records")
			}
			routed <- struct{}{}
			_, _ = io.Copy(io.Discard, c)
			if !removed.Load() { // the first test connection was deliberately closed during activation
				_ = c.Close()
				return
			}
			_ = c.Close()
		}, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for i := 0; i < 2; i++ {
		select {
		case <-routed:
		case <-time.After(time.Second):
			t.Fatal("connection was not routed")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("close must be idempotent:", err)
	}
	if !removed.Load() {
		t.Fatal("server did not remove capture")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("stop retained accepted connection")
	}
}

func TestActivationFailureRollsBackDeviceAndListeners(t *testing.T) {
	var addresses []string
	closed := false
	d := fakeDriver{activate: func(b []byte) error {
		addresses = []string{fmt.Sprintf("127.0.0.1:%d", binary.LittleEndian.Uint16(b[24:])), fmt.Sprintf("[::1]:%d", binary.LittleEndian.Uint16(b[26:]))}
		return fmt.Errorf("injected driver activation failure")
	}, close: func() error { closed = true; return nil }}
	s, err := start(context.Background(), testPolicy(t), d, func(net.Conn) ([]byte, []byte, error) { return nil, nil, fmt.Errorf("no context") }, func(context.Context, net.Conn, Redirect) { t.Error("unexpected route") }, func(error) {})
	if err == nil || s != nil || !closed {
		t.Fatal("incomplete driver rollback")
	}
	for _, address := range addresses {
		c, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			c.Close()
			t.Fatal("listener survived failed activation")
		}
	}
}

func TestListenerRejectsConnectionsWithoutKernelContext(t *testing.T) {
	var address string
	d := fakeDriver{activate: func(b []byte) error {
		address = fmt.Sprintf("127.0.0.1:%d", binary.LittleEndian.Uint16(b[24:]))
		return nil
	}, close: func() error { return nil }}
	errors := make(chan error, 1)
	s, err := start(context.Background(), testPolicy(t), d, func(net.Conn) ([]byte, []byte, error) { return nil, nil, fmt.Errorf("not a redirected socket") }, func(context.Context, net.Conn, Redirect) { t.Error("accepted plain TCP connection") }, func(err error) { errors <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err = c.Read(make([]byte, 1)); err == nil {
		t.Fatal("plain socket was kept open")
	}
	select {
	case <-errors:
	case <-time.After(time.Second):
		t.Fatal("missing rejection diagnostic")
	}
}
