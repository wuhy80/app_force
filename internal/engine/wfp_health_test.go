package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"appforce/internal/model"
)

type healthTestSession struct {
	check    func() error
	closeErr error
	closed   atomic.Int32
}

func (s *healthTestSession) Check() error { return s.check() }
func (s *healthTestSession) Close() error {
	s.closed.Add(1)
	return s.closeErr
}

// Use a real core/listener with a fake capture session so cleanup and restart
// behavior can be checked without a Windows driver or OS route changes.
func healthTestEngine(t *testing.T, capture wfpSession) (*Engine, model.Config) {
	t.Helper()
	c := wfpTestConfig(t)
	c.Settings.Mode = "mixed"
	e := &Engine{CachePath: filepath.Join(t.TempDir(), "cache.db")}
	if err := e.Start(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	e.mu.Lock()
	e.wfp, e.status.Mode = capture, "wfp"
	e.mu.Unlock()
	return e, c
}

func waitHealthSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("WFP watchdog did not reach the expected state")
	}
}

func TestWFPHealthFailureStopsCoreConnectionsAndReportsCleanupError(t *testing.T) {
	session := &healthTestSession{
		check:    func() error { return errors.New("dynamic filters lost") },
		closeErr: errors.New("capture cleanup failed"),
	}
	e, c := healthTestEngine(t, session)
	connection, peer := net.Pipe()
	defer peer.Close()
	e.mu.Lock()
	e.nodeConnections = map[net.Conn]struct{}{connection: {}}
	e.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, done := make(chan time.Time, 1), make(chan struct{})
	go func() { defer close(done); e.watchWFP(ctx, session, ticks) }()
	ticks <- time.Now()
	waitHealthSignal(t, done)
	status := e.Snapshot()
	if status.Running || status.Mode != "wfp" ||
		!strings.Contains(status.Error, "dynamic filters lost") || !strings.Contains(status.Error, "capture cleanup failed") {
		t.Fatalf("capture failure left misleading status: %+v", status)
	}
	if session.closed.Load() != 1 {
		t.Fatal("failed capture session was not closed exactly once")
	}
	if len(e.Logs.Entries()) == 0 || e.Logs.Entries()[len(e.Logs.Entries())-1].Message != status.Error {
		t.Fatal("capture failure was not recorded in the application log")
	}
	client, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", stringPort(c.Settings.MixedPort)), time.Second)
	if err == nil {
		_ = client.Close()
		t.Fatal("core listener still accepts connections after capture failure")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("account connection survived capture failure: %v", err)
	}
}

func TestDelayedWFPHealthFailureCannotStopReplacement(t *testing.T) {
	checking, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseCheck := func() { once.Do(func() { close(release) }) }
	defer releaseCheck()
	old := &healthTestSession{check: func() error {
		close(checking)
		<-release
		return errors.New("old session failure")
	}}
	e, c := healthTestEngine(t, old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, done := make(chan time.Time, 1), make(chan struct{})
	go func() { defer close(done); e.watchWFP(ctx, old, ticks) }()
	ticks <- time.Now()
	waitHealthSignal(t, checking)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(c); err != nil {
		t.Fatal(err)
	}
	replacement := &healthTestSession{check: func() error { return nil }}
	e.mu.Lock()
	e.wfp, e.status.Mode = replacement, "wfp"
	e.mu.Unlock()
	releaseCheck()
	waitHealthSignal(t, done)
	if status := e.Snapshot(); !status.Running || status.Mode != "wfp" || status.Error != "" {
		t.Fatalf("old result affected the replacement: %+v", status)
	}
	if old.closed.Load() != 1 || replacement.closed.Load() != 0 {
		t.Fatal("old watchdog closed the replacement session")
	}
	client, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", stringPort(c.Settings.MixedPort)), time.Second)
	if err != nil {
		t.Fatalf("replacement listener stopped: %v", err)
	}
	_ = client.Close()
}

func TestWFPWatchCancellationDoesNotReportFailure(t *testing.T) {
	var checks atomic.Int32
	session := &healthTestSession{check: func() error {
		checks.Add(1)
		return errors.New("should not check canceled session")
	}}
	e, _ := healthTestEngine(t, session)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ticks, done := make(chan time.Time, 1), make(chan struct{})
	ticks <- time.Now() // Both cases are ready: neither may trigger a check.
	go func() { defer close(done); e.watchWFP(ctx, session, ticks) }()
	waitHealthSignal(t, done)
	if status := e.Snapshot(); !status.Running || status.Error != "" || checks.Load() != 0 || session.closed.Load() != 0 {
		t.Fatalf("watch cancellation was mistaken for capture failure: %+v", status)
	}
}
