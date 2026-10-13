package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"appforce/internal/model"
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

func (l *Logs) WriteMessage(level log.Level, message string) {
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
	ID          string `json:"id"`
	Network     string `json:"network"`
	Destination string `json:"destination"`
	ProcessPath string `json:"process_path"`
	PID         uint32 `json:"pid"`
	Proxied     bool   `json:"proxied"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
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
	mu        sync.Mutex
	instance  *box.Box
	traffic   *trafficcontrol.Manager
	cancel    context.CancelFunc
	status    Status
	Logs      Logs
	CachePath string
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
		err = instance.Start()
	}
	if err != nil {
		if instance != nil {
			_ = instance.Close()
		}
		cancel()
		e.status.Error = fmt.Sprintf("代理启动失败: %v", err)
		return fmt.Errorf("%s", e.status.Error)
	}
	e.instance, e.cancel = instance, cancel
	e.traffic = service.PtrFromContext[trafficcontrol.Manager](ctx)
	now := time.Now()
	e.status = Status{Running: true, Mode: c.Settings.Mode, StartedAt: &now}
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance == nil {
		e.status.Running = false
		return nil
	}
	err := e.instance.Close()
	e.cancel()
	e.instance, e.traffic, e.cancel = nil, nil, nil
	e.status = Status{}
	if err != nil {
		e.status.Error = err.Error()
	}
	return err
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
	for _, c := range e.traffic.Connections() {
		path := ""
		var pid uint32
		if p := c.Metadata.ProcessInfo; p != nil {
			path, pid = p.ProcessPath, p.ProcessID
		}
		destination := c.Metadata.Destination.String()
		if c.Metadata.Domain != "" {
			destination = c.Metadata.Domain
		}
		status.Connections = append(status.Connections, Connection{c.ID.String(), c.Metadata.Network, destination, path, pid, c.Outbound == "selected", c.Upload.Load(), c.Download.Load()})
		if len(status.Connections) >= 200 {
			break
		}
	}
	return status
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
