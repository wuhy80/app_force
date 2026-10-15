package engine

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"sync"
	"time"

	"appforce/internal/model"
	"appforce/internal/traffic"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}
type Logs struct {
	mu      sync.Mutex
	entries []LogEntry
}

// sing-box's platform formatter emits ANSI codes independently of its console settings.
// Also accept the visible ESC symbol used when control characters are rendered as text.
var logControlCodes = regexp.MustCompile(`(?:\x1b|\x{241b})\[[0-?]*[ -/]*[@-~]`)

func (l *Logs) WriteMessage(level log.Level, message string) {
	message = logControlCodes.ReplaceAllString(message, "")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, LogEntry{time.Now(), log.FormatLevel(level), message})
	if len(l.entries) > 250 {
		l.entries = l.entries[len(l.entries)-250:]
	}
}

func (l *Logs) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogEntry{}, l.entries...)
}

type Connection struct {
	ID          string          `json:"id"`
	Network     string          `json:"network"`
	Destination string          `json:"destination"`
	ProcessPath string          `json:"process_path"`
	PID         uint32          `json:"pid"`
	Proxied     bool            `json:"proxied"`
	Outbound    string          `json:"outbound"`
	Node        *model.NodeView `json:"node,omitempty"`
	Upload      int64           `json:"upload"`
	Download    int64           `json:"download"`
}

type Status struct {
	Running     bool         `json:"running"`
	Mode        string       `json:"mode"`
	StartedAt   *time.Time   `json:"started_at"`
	Error       string       `json:"error"`
	Connections []Connection `json:"connections"`
	Upload      int64        `json:"upload"`
	Download    int64        `json:"download"`
}

type Engine struct {
	mu              sync.Mutex
	instance        *box.Box
	traffic         *trafficcontrol.Manager
	nodesByOutbound map[string]model.NodeView
	nodeConnections map[net.Conn]struct{}
	cancel          context.CancelFunc
	status          Status
	Logs            Logs
	CachePath       string
	Recorder        *traffic.Recorder
	tracker         *traffic.Tracker
	samplingStop    chan struct{}
	samplingDone    chan struct{}
}

func (e *Engine) Start(c model.Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance != nil {
		return fmt.Errorf("代理已经运行")
	}
	e.status = Status{Mode: c.Settings.Mode}
	data, err := Build(c)
	if err != nil {
		e.status.Error = err.Error()
		return err
	}
	ctx, cancel := context.WithCancel(newContext())
	opts, err := decode(ctx, data)
	if err != nil {
		cancel()
		e.status.Error = err.Error()
		return err
	}
	opts.Experimental = &option.ExperimentalOptions{CacheFile: &option.CacheFileOptions{Path: e.CachePath}}
	instance, err := box.New(box.Options{Options: opts, Context: ctx, PlatformLogWriter: &e.Logs})
	if err == nil {
		if e.Recorder != nil {
			e.tracker = traffic.NewTracker(e.Recorder, c.Rules)
			instance.Router().AppendTracker(e.tracker)
		}
		err = instance.Start()
	}
	if err != nil {
		if instance != nil {
			_ = instance.Close()
		}
		cancel()
		if e.tracker != nil {
			e.tracker.Close()
			e.tracker = nil
			_ = e.Recorder.Flush()
		}
		e.status.Error = fmt.Sprintf("代理启动失败: %v", err)
		return fmt.Errorf("%s", e.status.Error)
	}
	e.instance, e.cancel = instance, cancel
	e.traffic = service.PtrFromContext[trafficcontrol.Manager](ctx)
	// Keep node details tied to this running instance, not to later saved selections.
	e.nodesByOutbound = make(map[string]model.NodeView)
	nodes, _ := outboundNodes(c) // Build already validated every active node.
	for _, node := range nodes {
		e.nodesByOutbound[nodeTag(c, node.ID)] = node.View()
	}
	for _, inbound := range opts.Inbounds {
		if tun, ok := inbound.Options.(*option.TunInboundOptions); ok {
			e.Logs.WriteMessage(log.LevelInfo, fmt.Sprintf("TUN 已启动（网络栈: %s）", tun.Stack))
		}
	}
	now := time.Now()
	e.status = Status{Running: true, Mode: c.Settings.Mode, StartedAt: &now}
	if e.Recorder != nil {
		e.samplingStop, e.samplingDone = make(chan struct{}), make(chan struct{})
		go e.sampleTraffic(e.samplingStop, e.samplingDone)
	}
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for connection := range e.nodeConnections {
		_ = connection.Close()
	}
	e.nodeConnections = nil
	if e.instance == nil {
		e.status.Running = false
		if e.Recorder != nil {
			e.flushTraffic()
		}
		return nil
	}
	if e.samplingStop != nil {
		close(e.samplingStop)
		<-e.samplingDone
		e.samplingStop, e.samplingDone = nil, nil
	}
	err := e.instance.Close()
	if isAlreadyClosedError(err) {
		err = nil
	}
	e.cancel()
	e.instance, e.traffic, e.cancel = nil, nil, nil
	e.nodesByOutbound = nil
	e.status = Status{}
	if e.tracker != nil {
		e.tracker.Close()
		e.tracker = nil
		e.flushTraffic()
	}
	if err != nil {
		e.status.Error = err.Error()
	}
	return err
}

func (e *Engine) flushTraffic() {
	if err := e.Recorder.Flush(); err != nil {
		e.Logs.WriteMessage(log.LevelError, fmt.Sprintf("保存流量历史失败: %v", err))
	}
}

func (e *Engine) sampleTraffic(stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			e.Recorder.Sample()
			ticks++
			if ticks%5 == 0 {
				_ = e.Recorder.Flush()
			}
		}
	}
}

func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := e.status
	status.Connections = []Connection{}
	if e.traffic == nil {
		return status
	}
	status.Upload, status.Download = e.traffic.Total()
	status.Connections = snapshotConnections(e.traffic.Connections(), e.nodesByOutbound)
	return status
}

func snapshotConnections(connections []*trafficcontrol.TrackerMetadata, nodes map[string]model.NodeView) []Connection {
	// Keep proxied traffic visible when direct traffic exceeds the display limit.
	sort.Slice(connections, func(i, j int) bool {
		a, b := connections[i], connections[j]
		if model.IsProxyOutbound(a.Outbound) != model.IsProxyOutbound(b.Outbound) {
			return model.IsProxyOutbound(a.Outbound)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID.String() < b.ID.String()
	})
	result := make([]Connection, 0, min(len(connections), 200))
	for _, c := range connections {
		path := ""
		var pid uint32
		if p := c.Metadata.ProcessInfo; p != nil {
			path, pid = p.ProcessPath, p.ProcessID
		}
		destination := c.Metadata.Destination.String()
		if c.Metadata.Domain != "" {
			destination = c.Metadata.Domain
		}
		connection := Connection{
			ID: c.ID.String(), Network: c.Metadata.Network, Destination: destination, ProcessPath: path, PID: pid,
			Proxied: model.IsProxyOutbound(c.Outbound), Outbound: c.Outbound, Upload: c.Upload.Load(), Download: c.Download.Load(),
		}
		if node, ok := nodes[c.Outbound]; ok {
			connection.Node = &node
		}
		result = append(result, connection)
		if len(result) >= 200 {
			break
		}
	}
	return result
}

// Probe makes an HTTPS request through the actual outbound, without touching TUN or system routes.
func Probe(ctx context.Context, node model.Node) (uint16, error) {
	c := model.DefaultConfig()
	c.Nodes, c.SelectedNodeID, c.Settings.Mode = []model.Node{node}, node.ID, "mixed"
	data, err := Build(c)
	if err != nil {
		return 0, err
	}
	boxCtx := newContext()
	opts, err := decode(boxCtx, data)
	if err != nil {
		return 0, err
	}
	opts.Inbounds = nil
	opts.Route.FindProcess = false
	instance, err := box.New(box.Options{Options: opts, Context: boxCtx})
	if err != nil {
		return 0, err
	}
	defer instance.Close()
	if err = instance.Start(); err != nil {
		return 0, err
	}
	outbound, ok := instance.Outbound().Outbound("selected")
	if !ok {
		return 0, fmt.Errorf("节点不存在")
	}
	return urltest.URLTest(ctx, "https://www.gstatic.com/generate_204", outbound)
}
