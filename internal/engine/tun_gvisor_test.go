//go:build with_gvisor

package engine

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"appforce/internal/model"
	"appforce/internal/traffic"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/link/channel"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv4"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv6"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// This fixture supplies only the packet link, without creating an OS interface,
// changing routes, or requiring administrator privileges.
type memoryTUN struct{ endpoint *channel.Endpoint }

func (t *memoryTUN) Read([]byte) (int, error)             { return 0, io.EOF }
func (t *memoryTUN) Write([]byte) (int, error)            { return 0, io.ErrClosedPipe }
func (t *memoryTUN) Name() (string, error)                { return "memory-tun", nil }
func (t *memoryTUN) Start() error                         { return nil }
func (t *memoryTUN) Close() error                         { t.endpoint.Close(); return nil }
func (t *memoryTUN) UpdateRouteOptions(tun.Options) error { return nil }
func (t *memoryTUN) NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error) {
	return t.endpoint, stack.NICOptions{}, nil
}
func (t *memoryTUN) WritePacket(packet *stack.PacketBuffer) (int, error) {
	var packets stack.PacketBufferList
	packets.PushBack(packet)
	if _, err := t.endpoint.WritePackets(packets); err != nil {
		return 0, net.ErrClosed
	}
	return packet.Size(), nil
}

type memoryTUNHandler struct {
	router      adapter.Router
	destination M.Socksaddr
	processPath string
}

func (h *memoryTUNHandler) JudgeFlow(uint8, netip.AddrPort, netip.AddrPort, []byte) tun.FlowVerdict {
	return tun.FlowVerdict{Action: tun.ActionAccept}
}
func (h *memoryTUNHandler) NewDNSPacket([]byte, M.Socksaddr, M.Socksaddr, N.PacketWriter) {}
func (h *memoryTUNHandler) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
	}
}
func (h *memoryTUNHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	// Forward to a real HTTPS fixture while retaining application attribution.
	h.router.RouteConnectionEx(ctx, conn, adapter.InboundContext{
		Inbound: "tun", InboundType: "tun", Network: "tcp",
		Source: source, Destination: h.destination,
		ProcessInfo: &adapter.ConnectionOwner{ProcessID: 1234, ProcessPath: h.processPath},
	}, onClose)
}

func TestWindowsTUNDownloadsLargeHTTPSResponsesForUnselectedProgram(t *testing.T) {
	body := bytes.Repeat([]byte("image-payload-0123456789"), 16384)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer target.Close()
	upstream := socksServer(t)
	node, err := model.NewNode("fixture", "test", map[string]any{
		"type": "socks", "server": "127.0.0.1", "server_port": upstream.Addr().(*net.TCPAddr).Port,
	})
	if err != nil {
		t.Fatal(err)
	}
	const processPath = `C:\Program Files (x86)\DingDing\main\current\DingTalk.exe`
	for _, family := range []struct {
		name, source, destination string
		protocol                  tcpip.NetworkProtocolNumber
	}{
		{"ipv4", "172.29.231.2/30", "203.0.113.10", ipv4.ProtocolNumber},
		{"ipv6", "fdfe:afce:1::2/126", "2001:db8::10", ipv6.ProtocolNumber},
	} {
		t.Run(family.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(newContext(), 10*time.Second)
			defer cancel()
			c := model.DefaultConfig()
			c.Nodes, c.SelectedNodeID = []model.Node{node}, node.ID
			c.Rules = []model.Rule{{Path: `C:\Apps\selected.exe`, Enabled: true}}
			data, err := buildForPlatform(c, "windows")
			if err != nil {
				t.Fatal(err)
			}
			opts, err := decode(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			tunOptions := opts.Inbounds[1].Options.(*option.TunInboundOptions)
			opts.Inbounds = nil
			opts.Route.FindProcess = false // The fixture supplies process ownership.
			// The real target is loopback; omit its private-address exemption so
			// this transfer exercises the unselected-program routing decision.
			opts.Route.Rules = slices.DeleteFunc(opts.Route.Rules, func(rule option.Rule) bool {
				return rule.DefaultOptions.IPIsPrivate
			})
			instance, err := box.New(box.Options{Options: opts, Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			recorder, err := traffic.Open(filepath.Join(t.TempDir(), "traffic.db"))
			if err != nil {
				t.Fatal(err)
			}
			tracker := traffic.NewTracker(recorder, c.Rules)
			defer tracker.Close()
			instance.Router().AppendTracker(tracker)
			if err := instance.Start(); err != nil {
				t.Fatal(err)
			}
			virtualTUN := &memoryTUN{endpoint: channel.New(1024, tunOptions.MTU, "")}
			defer virtualTUN.Close()
			backend, err := tun.NewStack(tunOptions.Stack, tun.StackOptions{
				Context: ctx, Tun: virtualTUN,
				TunOptions: tun.Options{
					Name: "memory-tun", MTU: tunOptions.MTU,
					Inet4Address: []netip.Prefix{tunOptions.Address[0]},
					Inet6Address: []netip.Prefix{tunOptions.Address[1]},
				},
				Handler: &memoryTUNHandler{router: instance.Router(), destination: M.SocksaddrFromNet(target.Listener.Addr()), processPath: processPath},
				Logger:  logger.NOP(), UDPTimeout: time.Second, ICMPTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Start(); err != nil {
				_ = backend.Close()
				t.Fatal(err)
			}
			defer backend.Close()
			clientStack := stack.New(stack.Options{
				NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
				TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
			})
			defer clientStack.Close()
			clientLink := channel.New(1024, tunOptions.MTU, "")
			defer clientLink.Close()
			if err := clientStack.CreateNIC(1, clientLink); err != nil {
				t.Fatal(err)
			}
			source := netip.MustParsePrefix(family.source)
			if err := clientStack.AddProtocolAddress(1, tcpip.ProtocolAddress{
				Protocol:          family.protocol,
				AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFromSlice(source.Addr().AsSlice()), PrefixLen: source.Bits()},
			}, stack.AddressProperties{}); err != nil {
				t.Fatal(err)
			}
			clientStack.SetRouteTable([]tcpip.Route{
				{Destination: header.IPv4EmptySubnet, NIC: 1},
				{Destination: header.IPv6EmptySubnet, NIC: 1},
			})
			var largestPacket atomic.Int64
			var pumps sync.WaitGroup
			pump := func(from, to *channel.Endpoint) {
				defer pumps.Done()
				for packet := from.ReadContext(ctx); packet != nil; packet = from.ReadContext(ctx) {
					for previous := largestPacket.Load(); int64(packet.Size()) > previous; previous = largestPacket.Load() {
						if largestPacket.CompareAndSwap(previous, int64(packet.Size())) {
							break
						}
					}
					incoming := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: packet.ToBuffer()})
					to.InjectInbound(packet.NetworkProtocolNumber, incoming)
					incoming.DecRef()
					packet.DecRef()
				}
			}
			pumps.Add(2)
			go pump(clientLink, virtualTUN.endpoint)
			go pump(virtualTUN.endpoint, clientLink)
			defer func() { cancel(); pumps.Wait() }()
			transport := &http.Transport{
				TLSClientConfig:   target.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
				DisableKeepAlives: true,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return gonet.DialContextTCP(ctx, clientStack, tcpip.FullAddress{
						NIC: 1, Addr: tcpip.AddrFromSlice(netip.MustParseAddr(family.destination).AsSlice()), Port: 443,
					}, family.protocol)
				},
			}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
			response, err := client.Get(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("large HTTPS response was truncated: received %d of %d bytes, error: %v", len(got), len(body), err)
			}
			if largestPacket.Load() > int64(tunOptions.MTU) {
				t.Fatalf("packet exceeds TUN MTU: %d > %d", largestPacket.Load(), tunOptions.MTU)
			}
			all, err := recorder.Report(7, "", true)
			if err != nil || all.Summary.Download < int64(len(body)) {
				t.Fatalf("TUN transfer missing from traffic counters: %+v, %v", all.Summary, err)
			}
			proxied, err := recorder.Report(7, "", false)
			if err != nil || proxied.Summary.Download != 0 || proxied.Summary.Upload != 0 {
				t.Fatalf("unselected program was unexpectedly proxied: %+v, %v", proxied.Summary, err)
			}
		})
	}
}
