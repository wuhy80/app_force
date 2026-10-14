// Package startup manages the current user's persistent Windows logon task.
package startup

import (
	"fmt"
	"sync"
)

type Status struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Error     string `json:"error,omitempty"`
}

type backend interface {
	Enabled() (bool, error)
	SetEnabled(bool) error
}

type Manager struct {
	mu      sync.Mutex
	backend backend
	status  Status
}

// New reads the persisted task without creating or enabling one by default.
// args must contain only the normal launch flags, with absolute file paths.
func New(args []string) *Manager {
	b, err := newSystemBackend(args)
	m := &Manager{backend: b, status: Status{Supported: supported}}
	if err == nil && b != nil {
		m.status.Enabled, err = b.Enabled()
	}
	if err != nil {
		m.status.Error = err.Error()
	}
	return m
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) SetEnabled(enabled bool) error {
	if m == nil {
		return fmt.Errorf("当前运行环境不支持 Windows 开机自动启动")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.status.Supported {
		return fmt.Errorf("当前运行环境不支持 Windows 开机自动启动")
	}
	if m.backend == nil {
		return fmt.Errorf("无法设置 Windows 开机自动启动: %s", m.status.Error)
	}
	if err := m.backend.SetEnabled(enabled); err != nil {
		m.status.Error = err.Error()
		return err
	}
	m.status.Enabled = enabled
	m.status.Error = ""
	return nil
}
