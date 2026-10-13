package tray

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

type fakeBackend struct {
	mu            sync.Mutex
	state         State
	starts, stops int
	startEntered  chan struct{}
	startRelease  chan struct{}
	startErr      error
}

func (b *fakeBackend) TrayState() State { b.mu.Lock(); defer b.mu.Unlock(); return b.state }
func (b *fakeBackend) Start() error {
	b.mu.Lock()
	b.starts++
	b.mu.Unlock()
	if b.startEntered != nil {
		b.startEntered <- struct{}{}
	}
	if b.startRelease != nil {
		<-b.startRelease
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startErr != nil {
		return b.startErr
	}
	b.state.Running = true
	return nil
}
func (b *fakeBackend) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	b.state.Running = false
	return nil
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tray event")
		var zero T
		return zero
	}
}

func waitView(t *testing.T, views <-chan View, accept func(View) bool) View {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case view := <-views:
			if accept(view) {
				return view
			}
		case <-timer.C:
			t.Fatal("timed out waiting for tray state")
			return View{}
		}
	}
}

func TestOperationsAreSerializedAndExitRemainsAvailable(t *testing.T) {
	backend := &fakeBackend{startEntered: make(chan struct{}, 1), startRelease: make(chan struct{})}
	views := make(chan View, 32)
	shown := make(chan struct{}, 1)
	exited := make(chan struct{}, 1)
	c := NewController(backend, Hooks{Render: func(v View) { views <- v }, Show: func() { shown <- struct{}{} }, Exit: func() { exited <- struct{}{} }})
	defer c.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.startRelease) }) }
	defer release()
	if !c.Submit(Start) {
		t.Fatal("start rejected")
	}
	receive(t, backend.startEntered)
	view := waitView(t, views, func(v View) bool { return v.Busy })
	if view.CanStart() || view.CanStop() {
		t.Fatal("busy proxy actions must be disabled")
	}
	if c.Submit(Start) || c.Submit(Stop) {
		t.Fatal("accepted overlapping engine operation")
	}
	if !c.Submit(Show) {
		t.Fatal("show rejected while starting")
	}
	receive(t, shown)
	if !c.Submit(Exit) {
		t.Fatal("exit rejected while starting")
	}
	receive(t, exited)
	if c.Submit(Start) {
		t.Fatal("accepted operation after exit")
	}
	closed := make(chan struct{})
	closing := make(chan struct{})
	go func() { close(closing); c.Close(); close(closed) }()
	receive(t, closing)
	select {
	case <-closed:
		t.Fatal("closed before in-flight engine operation finished")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	receive(t, closed)
}

func TestFailedStartReportsErrorAndAllowsRetry(t *testing.T) {
	failure := errors.New("missing node")
	backend := &fakeBackend{startErr: failure}
	views := make(chan View, 32)
	errorsSeen := make(chan error, 1)
	c := NewController(backend, Hooks{Render: func(v View) { views <- v }, Error: func(err error) { errorsSeen <- err }})
	defer c.Close()
	if !c.Submit(Start) {
		t.Fatal("start rejected")
	}
	if !errors.Is(receive(t, errorsSeen), failure) {
		t.Fatal("missing engine error")
	}
	view := waitView(t, views, func(v View) bool { return !v.Busy && !v.Running })
	if !view.CanStart() || view.CanStop() {
		t.Fatal("incorrect state after failed start")
	}
	backend.mu.Lock()
	backend.startErr = nil
	backend.mu.Unlock()
	if !c.Submit(Start) {
		t.Fatal("retry rejected")
	}
	waitView(t, views, func(v View) bool { return !v.Busy && v.Running })
	if !c.Submit(Stop) {
		t.Fatal("stop rejected")
	}
	waitView(t, views, func(v View) bool { return !v.Busy && !v.Running })
}

func TestStateFollowsUIAndStaleActionsDoNotRestartEngine(t *testing.T) {
	backend := &fakeBackend{}
	views := make(chan View, 32)
	c := NewController(backend, Hooks{Render: func(v View) { views <- v }})
	defer c.Close()
	receive(t, views)
	// Simulate a configuration and engine change made outside of the tray.
	backend.mu.Lock()
	backend.state = State{Running: true, Node: "new node", Mode: "mixed", Rules: 2}
	backend.mu.Unlock()
	view := waitView(t, views, func(v View) bool { return v.Running && v.Node == "new node" })
	if view.CanStart() || !view.CanStop() {
		t.Fatal("menu did not follow external change")
	}
	if !c.Submit(Start) {
		t.Fatal("start intent rejected")
	}
	waitView(t, views, func(v View) bool { return v.Busy })
	waitView(t, views, func(v View) bool { return !v.Busy })
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.starts != 0 {
		t.Fatal("stale start restarted running proxy")
	}
}

func TestSubscriptionNamesCannotInjectMenuMarkupOrInvalidUTF16(t *testing.T) {
	if got := menuText("A&B\t菜单\x00\n"); got != "A&&B 菜单  " {
		t.Fatalf("unsafe menu text: %q", got)
	}
	for _, name := range []string{strings.Repeat("a", 126) + "🌏", strings.Repeat("🌏", 100), "节点\x00test"} {
		tip := tooltip(name)
		end := 0
		for end < len(tip) && tip[end] != 0 {
			end++
		}
		if end > 127 {
			t.Fatal("tooltip lacks terminator")
		}
		decoded := string(utf16.Decode(tip[:end]))
		if strings.ContainsRune(decoded, '\ufffd') {
			t.Fatalf("split UTF16 surrogate: %q", decoded)
		}
	}
}
