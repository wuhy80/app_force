package startup

import (
	"errors"
	"testing"
)

type fakeBackend struct {
	enabled bool
	err     error
}

func (b *fakeBackend) Enabled() (bool, error) { return b.enabled, b.err }
func (b *fakeBackend) SetEnabled(enabled bool) error {
	if b.err != nil {
		return b.err
	}
	b.enabled = enabled
	return nil
}

func TestFailedChangesKeepPreviousStartupChoice(t *testing.T) {
	b := &fakeBackend{}
	m := &Manager{backend: b, status: Status{Supported: true}}
	if m.Status().Enabled {
		t.Fatal("startup must default to off")
	}
	for _, enabled := range []bool{true, false} {
		if err := m.SetEnabled(enabled); err != nil {
			t.Fatal(err)
		}
		if b.enabled != enabled || m.Status().Enabled != enabled {
			t.Fatal("successful changes must update the system and displayed choice")
		}
		b.err = errors.New("scheduler unavailable")
		if err := m.SetEnabled(!enabled); err == nil {
			t.Fatal("failed system changes must be reported")
		}
		if m.Status().Enabled != enabled || m.Status().Error == "" || b.enabled != enabled {
			t.Fatal("failed change lost the previous choice or error")
		}
		b.err = nil
	}
	if err := m.SetEnabled(false); err != nil || m.Status().Error != "" {
		t.Fatal("retry did not clear the error")
	}
}

func TestUnavailableStartupCannotBeEnabled(t *testing.T) {
	for _, m := range []*Manager{nil, {}, {status: Status{Supported: true, Error: "missing user"}}} {
		if err := m.SetEnabled(true); err == nil || m.Status().Enabled {
			t.Fatal("unavailable startup accepted enabling")
		}
	}
}
