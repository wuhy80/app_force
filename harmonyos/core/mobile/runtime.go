//go:build linux || darwin || openharmony

package mobile

import (
	"appforce/internal/engine"
	"appforce/internal/model"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/log"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/service"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Connection struct {
	ID          string `json:"id"`
	Network     string `json:"network"`
	Destination string `json:"destination"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

type RuntimeState struct {
	Phase        string            `json:"phase"`
	Active       bool              `json:"active"`
	SessionID    string            `json:"session_id"`
	Revision     uint64            `json:"revision"`
	UpdatedAt    int64             `json:"updated_at"`
	StartedAt    int64             `json:"started_at"`
	Error        string            `json:"error"`
	Upload       int64             `json:"upload"`
	Download     int64             `json:"download"`
	UploadRate   int64             `json:"upload_rate"`
	DownloadRate int64             `json:"download_rate"`
	Connections  []Connection      `json:"connections"`
	Logs         []engine.LogEntry `json:"logs"`
}

type stopRequest struct {
	SessionID string `json:"session_id"`
	CreatedAt int64  `json:"created_at"`
}

func emptyRuntime() RuntimeState {
	return RuntimeState{Phase: "stopped", Connections: []Connection{}, Logs: []engine.LogEntry{}}
}

func readRuntime(dir string) RuntimeState {
	state := emptyRuntime()
	err := readJSON(filepath.Join(dir, "runtime.json"), &state, 1<<20)
	if err != nil && !os.IsNotExist(err) {
		state.Error = "运行状态读取失败"
	}
	lock := flock.New(filepath.Join(dir, "vpn.lock"))
	defer lock.Close()
	free, lockErr := lock.TryLock()
	state.Active = lockErr != nil || !free
	if !state.Active {
		if state.Phase != "error" {
			state.Phase = "stopped"
		}
		state.UploadRate, state.DownloadRate = 0, 0
		state.Connections = []Connection{}
	} else if state.UpdatedAt == 0 || time.Now().UnixMilli()-state.UpdatedAt > 10000 {
		state.Phase = "unknown"
		state.Error = "后台状态暂未更新，请停止代理后重试"
	}
	return state
}

type Runtime struct {
	opMu      sync.Mutex
	mu        sync.Mutex
	instance  *box.Box
	traffic   *trafficcontrol.Manager
	lock      *flock.Flock
	dir       string
	state     RuntimeState
	logs      engine.Logs
	stop      chan struct{}
	done      chan struct{}
	protector *socketGuard
}

// Stop waits for every in-flight native callback before its context is released.
// Later dial attempts fail here instead of calling a freed N-API callback.
type socketGuard struct {
	mu       sync.RWMutex
	closed   bool
	callback SocketProtector
}

func (g *socketGuard) protect(fd int) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return fmt.Errorf("VPN 已停止")
	}
	return g.callback(fd)
}

func (g *socketGuard) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

func DupTUN(fd int) TunOpener {
	return func(options *tun.Options) (tun.Tun, error) {
		if fd <= 0 {
			return nil, fmt.Errorf("系统 TUN 描述符无效")
		}
		copyFD, err := unix.Dup(fd)
		if err != nil {
			return nil, err
		}
		options.FileDescriptor = copyFD
		device, err := tun.New(*options)
		if err != nil {
			_ = unix.Close(copyFD)
		}
		return device, err
	}
}

func (r *Runtime) Start(dir string, open TunOpener, protect SocketProtector) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if r.instance != nil {
		return fmt.Errorf("代理已经运行")
	}
	if open == nil || protect == nil {
		return fmt.Errorf("TUN 或 socket 保护回调缺失")
	}
	s, err := OpenStore(dir)
	if err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, "vpn.lock"))
	free, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return err
	}
	if !free {
		_ = lock.Close()
		return fmt.Errorf("已有 VPN 内核正在运行")
	}
	r.mu.Lock()
	r.lock, r.dir = lock, s.dir
	r.state = emptyRuntime()
	r.state.Phase = "starting"
	r.state.SessionID = model.NewID()
	r.state.StartedAt = time.Now().UnixMilli()
	r.logs = engine.Logs{}
	r.protector = &socketGuard{callback: protect}
	r.mu.Unlock()
	defer func() {
		if r.instance == nil {
			r.protector.close()
			_ = r.lock.Close()
			r.lock = nil
		}
	}()
	c, err := s.locked(nil, false)
	if err == nil {
		r.mu.Lock()
		r.state.Revision = c.Revision
		r.mu.Unlock()
		err = r.publish()
	}
	var data []byte
	if err == nil {
		data, err = BuildCoreConfig(c)
	}
	if err == nil {
		ctx := coreContext(newPlatform(open, r.protector.protect))
		opts, decodeErr := decodeCore(ctx, data)
		err = decodeErr
		if err == nil {
			instance, newErr := box.New(box.Options{Context: ctx, Options: opts, PlatformLogWriter: &r.logs})
			err = newErr
			if err == nil {
				err = instance.Start()
			}
			if err != nil {
				r.protector.close()
				if instance != nil {
					_ = instance.Close()
				}
			} else {
				r.instance = instance
				r.traffic = service.PtrFromContext[trafficcontrol.Manager](ctx)
			}
		}
	}
	if err != nil {
		r.mu.Lock()
		r.state.Phase, r.state.Error = "error", err.Error()
		r.mu.Unlock()
		r.logs.WriteMessage(log.LevelError, err.Error())
		_ = r.publish()
		return err
	}
	r.mu.Lock()
	r.state.Phase = "running"
	r.mu.Unlock()
	r.logs.WriteMessage(log.LevelInfo, "鸿蒙 VPN 内核已启动，所选应用使用当前节点")
	if err = r.publish(); err != nil {
		r.protector.close()
		_ = r.instance.Close()
		r.mu.Lock()
		r.instance = nil
		r.traffic = nil
		r.state.Phase, r.state.Error = "error", "保存运行状态失败"
		r.mu.Unlock()
		_ = r.publish()
		return fmt.Errorf("保存运行状态失败: %w", err)
	}
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	go r.loop()
	return nil
}

func (r *Runtime) snapshot() RuntimeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state
	state.UpdatedAt = time.Now().UnixMilli()
	state.Active = state.Phase == "running" || state.Phase == "starting"
	state.Logs = r.logs.Entries()
	state.Connections = []Connection{}
	if r.traffic != nil {
		state.Upload, state.Download = r.traffic.Total()
		for _, conn := range r.traffic.Connections() {
			if len(state.Connections) >= 100 {
				break
			}
			state.Connections = append(state.Connections, Connection{conn.ID.String(), conn.Metadata.Network, conn.Metadata.Destination.String(), conn.Upload.Load(), conn.Download.Load()})
		}
	}
	if r.state.UpdatedAt > 0 {
		elapsed := state.UpdatedAt - r.state.UpdatedAt
		if elapsed > 0 {
			state.UploadRate = (state.Upload - r.state.Upload) * 1000 / elapsed
			state.DownloadRate = (state.Download - r.state.Download) * 1000 / elapsed
		}
	}
	if state.Phase != "running" {
		state.UploadRate, state.DownloadRate = 0, 0
	}
	r.state = state
	return state
}

func (r *Runtime) publish() error {
	return atomicJSON(filepath.Join(r.dir, "runtime.json"), r.snapshot())
}

func (r *Runtime) loop() {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			var command stopRequest
			r.mu.Lock()
			startedAt, sessionID := r.state.StartedAt, r.state.SessionID
			r.mu.Unlock()
			if readJSON(filepath.Join(r.dir, "stop.json"), &command, 4096) == nil && command.CreatedAt >= startedAt && command.SessionID == sessionID {
				go func() { _ = r.Stop() }()
				return
			}
			if err := r.publish(); err != nil {
				r.logs.WriteMessage(log.LevelError, "保存运行状态失败")
			}
		}
	}
}

func (r *Runtime) Stop() error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if r.instance == nil {
		return nil
	}
	close(r.stop)
	<-r.done
	// Capture the last counters before closing the manager.
	r.snapshot()
	r.protector.close()
	err := r.instance.Close()
	if err != nil && (os.IsNotExist(err) || err == net.ErrClosed) {
		err = nil
	}
	r.mu.Lock()
	r.instance, r.traffic = nil, nil
	r.state.Phase = "stopped"
	r.state.Active = false
	if err != nil {
		r.state.Phase, r.state.Error = "error", err.Error()
	}
	r.mu.Unlock()
	r.logs.WriteMessage(log.LevelInfo, "代理已停止")
	writeErr := r.publish()
	_ = r.lock.Close()
	r.lock = nil
	if err != nil {
		return err
	}
	return writeErr
}

func (r *Runtime) Reply() Reply {
	r.opMu.Lock()
	dir := r.dir
	r.opMu.Unlock()
	if dir == "" {
		return Reply{OK: true}
	}
	data, _ := json.Marshal(Request{Method: "state", Directory: dir})
	return Call(data)
}
