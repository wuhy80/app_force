package engine

import (
	"context"
	"net"
	"testing"
	"time"

	"appforce/internal/model"
)

func TestNodeDialNeverFallsBackAndStopClosesAuthorizationConnections(t *testing.T) {
	upstream := socksServer(t)
	node, err := model.NewNode("fixture", "test", map[string]any{"type": "socks", "version": "5", "server": "127.0.0.1", "server_port": upstream.Addr().(*net.TCPAddr).Port})
	if err != nil {
		t.Fatal(err)
	}
	c := model.DefaultConfig()
	c.Nodes, c.SelectedNodeID, c.Settings.Mode, c.Settings.MixedPort = []model.Node{node}, node.ID, "mixed", freePort(t)
	e := new(Engine)
	if _, err := e.DialNodeContext(context.Background(), node.ID, "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("stopped engine allowed account connection")
	}
	if err := e.Start(c); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if _, err := e.DialNodeContext(context.Background(), "missing", "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("missing node fell back to default")
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	connection, err := e.DialNodeContext(context.Background(), node.ID, "tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("stopping proxy left an authorization connection open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("authorization connection was not closed by proxy stop")
	}
}
