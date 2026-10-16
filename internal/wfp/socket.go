package wfp

import (
	"context"
	"fmt"
	"net"
	"time"
)

// DialSocket is the final physical TCP dial, before TLS/proxy handshakes. WFP
// records must be set before connect, not after a sing-box outbound returns.
func DialSocket(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("WFP TCP 原型不支持上游网络 %s", network)
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, ControlContext: socketControl(ctx)}
	return d.DialContext(ctx, network, address)
}
