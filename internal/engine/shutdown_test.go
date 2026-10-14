package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"appforce/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	outboundregistry "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

func TestAlreadyClosedErrorsDoNotHideOtherCleanupFailures(t *testing.T) {
	failure := errors.New("failed to restore interface")
	closed := &net.OpError{Op: "close", Net: "ip4", Addr: &net.IPAddr{IP: net.IPv4zero}, Err: net.ErrClosed}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"reported direct outbound error", E.Cause(E.Cause(closed, "close outbound/direct[direct]"), "close outbound"), true},
		{"closed file", fmt.Errorf("close file: %w", os.ErrClosed), true},
		{"multiple closed sockets", E.Cause(E.Errors(closed, fmt.Errorf("other socket: %w", net.ErrClosed)), "close outbound"), true},
		{"real failure", failure, false},
		{"joined real failure", errors.Join(closed, failure), false},
		{"nested joined real failure", E.Cause(E.Errors(closed, failure), "close outbound"), false},
		{"canceled context", context.Canceled, false},
		{"timeout", context.DeadlineExceeded, false},
		{"similar text", errors.New(net.ErrClosed.Error()), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isAlreadyClosedError(test.err); got != test.want {
				t.Fatalf("closed classification = %v, want %v: %v", got, test.want, test.err)
			}
		})
	}
}

type shutdownTestOutbound struct {
	adapter.Outbound
	cleanup func() error
}

func (o *shutdownTestOutbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if err := o.Outbound.(adapter.Lifecycle).Start(stage, scope); err != nil {
		return err
	}
	if stage == adapter.StartStateInitialize {
		scope.Add(o.cleanup)
	}
	return nil
}

func TestStopWithAlreadyClosedSocketCanRestart(t *testing.T) {
	for _, realFailure := range []bool{false, true} {
		name := "closed socket"
		if realFailure {
			name = "closed socket and real failure"
		}
		t.Run(name, func(t *testing.T) {
			// A real socket's second Close reproduces the core's cleanup race
			// without opening an OS TUN interface or changing system routes.
			socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			if err := socket.Close(); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("failed to restore interface")
			ctx, cancel := context.WithCancel(newContext())
			defer cancel()
			registry := service.FromContext[adapter.OutboundRegistry](ctx).(*outboundregistry.Registry)
			outboundregistry.Register[option.DirectOutboundOptions](registry, "direct", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DirectOutboundOptions) (adapter.Outbound, error) {
				outbound, err := direct.NewOutbound(ctx, router, logger, tag, options)
				if err != nil {
					return nil, err
				}
				return &shutdownTestOutbound{Outbound: outbound, cleanup: func() error {
					closed := socket.Close()
					if realFailure {
						return E.Errors(closed, failure)
					}
					return closed
				}}, nil
			})
			data := []byte(`{"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
			opts, err := decode(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := box.New(box.Options{Options: opts, Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			if err := instance.Start(); err != nil {
				t.Fatal(err)
			}
			e := &Engine{instance: instance, cancel: cancel, status: Status{Running: true}, CachePath: filepath.Join(t.TempDir(), "cache.db")}
			err = e.Stop()
			if realFailure {
				if !errors.Is(err, failure) || e.Snapshot().Error == "" {
					t.Fatalf("real cleanup failure was hidden: %v", err)
				}
				return
			}
			if err != nil || e.Snapshot().Error != "" || e.Snapshot().Running {
				t.Fatalf("already-closed socket left engine in failed state: %v, %+v", err, e.Snapshot())
			}
			node, err := model.NewNode("restart", "test", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": 1080})
			if err != nil {
				t.Fatal(err)
			}
			config := model.DefaultConfig()
			config.Nodes, config.SelectedNodeID = []model.Node{node}, node.ID
			config.Settings.Mode, config.Settings.MixedPort = "mixed", freePort(t)
			if err := e.Start(config); err != nil {
				t.Fatalf("new configuration could not start after cleanup: %v", err)
			}
			defer e.Stop()
			if !e.Snapshot().Running {
				t.Fatal("replacement engine is not running")
			}
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			if err := e.Stop(); err != nil {
				t.Fatalf("repeated stop failed: %v", err)
			}
		})
	}
}
