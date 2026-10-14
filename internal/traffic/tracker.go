package traffic

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"appforce/internal/model"
	"github.com/sagernet/sing-box/adapter"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.ConnectionTracker = (*Tracker)(nil)

// Tracker is one engine session. Closing it freezes counters before the final disk flush.
type Tracker struct {
	mu       sync.RWMutex
	closed   bool
	recorder *Recorder
	rules    []model.Rule
}

func NewTracker(r *Recorder, rules []model.Rule) *Tracker {
	r.resetLive()
	for _, rule := range rules {
		r.Register(rule.Name, rule.Path)
	}
	matchers := append([]model.Rule{}, rules...)
	// Parent application groups win over separately saved helper executables.
	// For nested installations, choose the most specific parent directory first.
	sort.SliceStable(matchers, func(i, j int) bool {
		a, b := len(matchers[i].ProcessPathRegex()) > 0, len(matchers[j].ProcessPathRegex()) > 0
		if a != b {
			return a
		}
		return len(matchers[i].Path) > len(matchers[j].Path)
	})
	return &Tracker{recorder: r, rules: matchers}
}

func (t *Tracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.recorder.resetLive()
}

func (t *Tracker) handle(metadata adapter.InboundContext, outbound adapter.Outbound) *handle {
	programPath, name := "", ""
	if p := metadata.ProcessInfo; p != nil {
		programPath = p.ProcessPath
	}
	if programPath != "" {
		for _, rule := range t.rules {
			if rule.MatchesPath(programPath) {
				name, programPath = rule.Name, rule.Path
				break
			}
		}
		if name == "" {
			// Bundle helpers share the outer application's identity even without a saved rule.
			if i := strings.Index(programPath, ".app/"); i >= 0 {
				programPath = programPath[:i+4]
			}
		}
	} else if metadata.InboundType == "mixed" || metadata.InboundType == "http" || metadata.InboundType == "socks" {
		name, programPath = "本地代理（未识别应用）", "unknown:local-proxy"
	} else {
		name, programPath = "未识别应用", "unknown:process"
	}
	id := t.recorder.Register(name, programPath)
	return &handle{tracker: t, id: id, proxied: outbound != nil && model.IsProxyOutbound(outbound.Tag())}
}

type handle struct {
	tracker *Tracker
	id      string
	proxied bool
	mu      sync.Mutex
	started bool
	closed  atomic.Bool
}

func (h *handle) start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started || h.closed.Load() {
		return
	}
	h.tracker.mu.RLock()
	defer h.tracker.mu.RUnlock()
	if h.tracker.closed {
		return
	}
	h.started = true
	h.tracker.recorder.active(h.id, h.proxied, 1)
}

func (h *handle) end() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Swap(true) || !h.started {
		return
	}
	h.tracker.mu.RLock()
	defer h.tracker.mu.RUnlock()
	if !h.tracker.closed {
		h.tracker.recorder.active(h.id, h.proxied, -1)
	}
}

func (h *handle) count(upload, download int64) {
	h.tracker.mu.RLock()
	defer h.tracker.mu.RUnlock()
	if !h.tracker.closed {
		h.tracker.recorder.count(h.id, h.proxied, upload, download)
	}
}

func (t *Tracker) RoutedConnection(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ adapter.Rule, outbound adapter.Outbound) net.Conn {
	h := t.handle(metadata, outbound)
	h.start()
	return &connTracker{ExtendedConn: bufio.NewCounterConn(conn,
		[]N.CountFunc{func(n int64) { h.count(n, 0) }}, []N.CountFunc{func(n int64) { h.count(0, n) }}), handle: h}
}

func (t *Tracker) RoutedPacketConnection(_ context.Context, conn N.PacketConn, metadata adapter.InboundContext, _ adapter.Rule, outbound adapter.Outbound) N.PacketConn {
	h := t.handle(metadata, outbound)
	h.start()
	return &packetTracker{PacketConn: bufio.NewCounterPacketConn(conn,
		[]N.CountFunc{func(n int64) { h.count(n, 0) }}, []N.CountFunc{func(n int64) { h.count(0, n) }}), handle: h}
}

func (t *Tracker) RoutedFlow(_ context.Context, metadata adapter.InboundContext, _ adapter.Rule, outbound adapter.Outbound) tun.FlowTracker {
	return &flowTracker{handle: t.handle(metadata, outbound)}
}

type connTracker struct {
	N.ExtendedConn
	handle *handle
}

func (t *connTracker) Close() error            { err := t.ExtendedConn.Close(); t.handle.end(); return err }
func (t *connTracker) Upstream() any           { return t.ExtendedConn }
func (t *connTracker) ReaderReplaceable() bool { return true }
func (t *connTracker) WriterReplaceable() bool { return true }

type packetTracker struct {
	N.PacketConn
	handle *handle
}

func (t *packetTracker) Close() error            { err := t.PacketConn.Close(); t.handle.end(); return err }
func (t *packetTracker) Upstream() any           { return t.PacketConn }
func (t *packetTracker) ReaderReplaceable() bool { return true }
func (t *packetTracker) WriterReplaceable() bool { return true }

type flowTracker struct{ handle *handle }

func (t *flowTracker) AttachFlow(tun.FlowHandle)     { t.handle.start() }
func (t *flowTracker) CountForward(n int)            { t.handle.count(int64(n), 0) }
func (t *flowTracker) CountReverse(n int)            { t.handle.count(0, int64(n)) }
func (t *flowTracker) FlowEstablished()              {}
func (t *flowTracker) CloseFlow(tun.FlowCloseReason) { t.handle.end() }
