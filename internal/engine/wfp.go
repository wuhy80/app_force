package engine

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"appforce/internal/wfp"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

type wfpSocketOptions struct{}
type wfpSocketOutbound struct{ outbound.Adapter }

func registerWFPSocket(registry *outbound.Registry) {
	outbound.Register[wfpSocketOptions](registry, wfp.SocketOutbound, func(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ wfpSocketOptions) (adapter.Outbound, error) {
		return &wfpSocketOutbound{outbound.NewAdapter(wfp.SocketOutbound, tag, []string{"tcp"}, nil)}, nil
	})
}

func (o *wfpSocketOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return wfp.DialSocket(ctx, network, destination.String())
}

func (o *wfpSocketOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("WFP TCP 原型不支持上游 UDP")
}

func wfpInbound(id uint32) string { return "wfp-" + strconv.FormatUint(uint64(id), 10) }

func routeWFP(router adapter.Router) wfp.RouteFunc {
	return func(ctx context.Context, conn net.Conn, r wfp.Redirect) {
		router.RouteConnectionEx(ctx, conn, adapter.InboundContext{
			Inbound: wfpInbound(r.Rule.ID), InboundType: "wfp", Network: "tcp",
			Source: M.SocksaddrFromNet(conn.RemoteAddr()), Destination: M.SocksaddrFromNetIP(r.Destination),
			ProcessInfo: &adapter.ConnectionOwner{ProcessID: r.PID, ProcessPath: r.ProcessPath},
		}, nil)
	}
}
