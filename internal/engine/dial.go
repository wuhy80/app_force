package engine

import (
	"context"
	"fmt"
	"net"
	"sync"

	M "github.com/sagernet/sing/common/metadata"
)

// DialNodeContext uses a running node directly, without the mixed inbound's
// default-node rule or the calling process's TUN rule.
func (e *Engine) DialNodeContext(ctx context.Context, nodeID, network, address string) (net.Conn, error) {
	e.mu.Lock()
	if e.instance == nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("代理未启动，请先启动 Antigravity 的代理后重试")
	}
	tag := ""
	for candidate, node := range e.nodesByOutbound {
		if node.ID == nodeID {
			tag = candidate
			break
		}
	}
	instance := e.instance
	outbound, ok := instance.Outbound().Outbound(tag)
	e.mu.Unlock()
	if tag == "" || !ok {
		return nil, fmt.Errorf("Antigravity 的代理节点未运行，请检查程序规则并重新启动代理")
	}
	connection, err := outbound.DialContext(ctx, network, M.ParseSocksaddr(address))
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance != instance {
		_ = connection.Close()
		return nil, fmt.Errorf("代理已停止或更新，请重新授权")
	}
	if e.nodeConnections == nil {
		e.nodeConnections = make(map[net.Conn]struct{})
	}
	e.nodeConnections[connection] = struct{}{}
	return &nodeConnection{Conn: connection, engine: e}, nil
}

type nodeConnection struct {
	net.Conn
	engine *Engine
	once   sync.Once
}

func (c *nodeConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.engine.mu.Lock()
		delete(c.engine.nodeConnections, c.Conn)
		c.engine.mu.Unlock()
	})
	return err
}
