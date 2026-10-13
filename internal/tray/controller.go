// Package tray coordinates local proxy actions independently of the desktop's selected device.
package tray

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Action uint8

const (
	Show Action = iota
	Start
	Stop
	Exit
)

type State struct {
	Running bool
	Node    string
	Mode    string
	Rules   int
}

type View struct {
	State
	Busy bool
}

func (v View) StatusText() string {
	if v.Busy {
		return "本机代理 · 正在处理"
	}
	if v.Running {
		return "本机代理 · 运行中"
	}
	return "本机代理 · 已停止"
}

func (v View) NodeText() string {
	if v.Node == "" {
		return "当前节点 · 未选择"
	}
	return "当前节点 · " + v.Node
}

func (v View) ModeText() string {
	if v.Mode == "mixed" {
		return "本地 SOCKS / HTTP"
	}
	return fmt.Sprintf("程序透明代理 · %d 个启用规则", v.Rules)
}

func (v View) CanStart() bool { return !v.Busy && !v.Running }
func (v View) CanStop() bool  { return !v.Busy && v.Running }

type Backend interface {
	TrayState() State
	Start() error
	Stop() error
}

type Hooks struct {
	Render func(View)
	Show   func()
	Exit   func()
	Error  func(error)
}

// Controller serializes proxy operations. Window and exit actions remain responsive
// while a proxy start is in progress; Close waits for an in-flight operation.
type Controller struct {
	backend   Backend
	hooks     Hooks
	ctx       context.Context
	cancel    context.CancelFunc
	busy      atomic.Bool
	actions   chan Action
	done      chan struct{}
	closeOnce sync.Once
}

func NewController(backend Backend, hooks Hooks) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{backend: backend, hooks: hooks, ctx: ctx, cancel: cancel, actions: make(chan Action, 1), done: make(chan struct{})}
	go c.run()
	return c
}

func (c *Controller) Submit(action Action) bool {
	if c.ctx.Err() != nil {
		return false
	}
	switch action {
	case Show:
		if c.hooks.Show != nil {
			c.hooks.Show()
		}
		return true
	case Exit:
		c.cancel()
		if c.hooks.Exit != nil {
			c.hooks.Exit()
		}
		return true
	case Start, Stop:
		if !c.busy.CompareAndSwap(false, true) {
			return false
		}
		select {
		case c.actions <- action:
			return true
		case <-c.ctx.Done():
			c.busy.Store(false)
			return false
		}
	default:
		return false
	}
}

func (c *Controller) render() {
	if c.hooks.Render != nil {
		c.hooks.Render(View{State: c.backend.TrayState(), Busy: c.busy.Load()})
	}
}

func (c *Controller) run() {
	defer close(c.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	c.render()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.render()
		case action := <-c.actions:
			if c.ctx.Err() != nil {
				c.busy.Store(false)
				return
			}
			c.render()
			var err error
			state := c.backend.TrayState()
			if action == Start && !state.Running {
				err = c.backend.Start()
			}
			if action == Stop && state.Running {
				err = c.backend.Stop()
			}
			c.busy.Store(false)
			if c.ctx.Err() != nil {
				return
			}
			c.render()
			if err != nil && c.hooks.Error != nil {
				c.hooks.Error(err)
			}
		}
	}
}

func (c *Controller) Close() {
	c.closeOnce.Do(c.cancel)
	<-c.done
}
