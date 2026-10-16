package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"appforce/internal/model"
	"appforce/internal/wfp"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

func wfpTestConfig(t *testing.T) model.Config {
	t.Helper()
	c := model.DefaultConfig()
	c.Settings.Mode = "wfp"
	c.Settings.MixedPort = freePort(t)
	for i := 0; i < 2; i++ {
		n, err := model.NewNode(fmt.Sprint(i), "test", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": 1080 + i})
		if err != nil {
			t.Fatal(err)
		}
		c.Nodes = append(c.Nodes, n)
	}
	c.SelectedNodeID = c.Nodes[0].ID
	c.Rules = []model.Rule{{Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true}, {Path: `C:\Tools\chosen.exe`, Enabled: true, NodeID: c.Nodes[1].ID}}
	return c
}

func TestWFPConfigurationHasNoTUNOrGlobalDNSAndKeepsPinnedNodes(t *testing.T) {
	c := wfpTestConfig(t)
	data, err := buildForPlatform(c, "windows")
	if err != nil {
		t.Fatal(err)
	}
	ctx := newContext()
	opts, err := decode(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Inbounds) != 1 || opts.Inbounds[0].Type != "mixed" || len(opts.Outbounds) != 4 || opts.Route.AutoDetectInterface || opts.Route.FindProcess || len(opts.DNS.Servers) != 1 {
		t.Fatalf("unexpected capture/configuration: %s", data)
	}
	if !strings.Contains(string(data), nodeTag(c, c.Nodes[1].ID)) || !strings.Contains(string(data), `"action":"reject"`) {
		t.Fatalf("missing pinned route or fail-closed final guard: %s", data)
	}
	for _, host := range []string{"linux", "darwin"} {
		if _, err := buildForPlatform(c, host); err == nil {
			t.Fatal("WFP accepted unsupported host")
		}
	}
	c.Settings.DNSMode = "proxy"
	if _, err := buildForPlatform(c, "windows"); err == nil {
		t.Fatal("WFP changed DNS behavior")
	}
}

func TestWFPRejectsSharedOrUnsupportedUpstreamsBeforeCapture(t *testing.T) {
	for name, node := range map[string]map[string]any{
		"quic":      {"type": "hysteria2"},
		"multiplex": {"type": "vless", "multiplex": map[string]any{"enabled": true}},
		"grpc":      {"type": "vless", "transport": map[string]any{"type": "grpc"}},
		"http2":     {"type": "vmess", "transport": map[string]any{"type": "http"}},
		"plugin":    {"type": "shadowsocks", "plugin": "obfs-local"},
		"tfo":       {"type": "trojan", "tcp_fast_open": true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := prepareWFPNode(node); err == nil {
				t.Fatal("accepted incompatible physical socket sharing")
			}
		})
	}
	for _, typ := range []string{"socks", "http", "shadowsocks", "vmess", "vless", "trojan"} {
		node := map[string]any{"type": typ}
		if err := prepareWFPNode(node); err != nil || node["detour"] != wfp.SocketOutbound {
			t.Fatalf("%s: %v", typ, err)
		}
	}
}

type recordingWFPSocket struct {
	outbound.Adapter
	records chan socketObservation
}

type socketObservation struct {
	records, destination string
	metadata             adapter.InboundContext
}

func (o *recordingWFPSocket) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	o.records <- socketObservation{string(wfp.Records(ctx)), dest.String(), *adapter.ContextFrom(ctx)}
	return (&net.Dialer{}).DialContext(ctx, network, dest.String())
}
func (o *recordingWFPSocket) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("TCP only")
}

// Exercises real sing-box routing and SOCKS forwarding on the development host.
// Kernel context and the physical socket IOCTL are supplied by test doubles;
// Windows callout/driver behavior is deliberately not claimed by this test.
func TestWFPTransparentRoutingPreservesIdentityPinnedNodeAndRedirectRecords(t *testing.T) {
	upstreams := []net.Listener{socksServer(t), socksServer(t)}
	c := wfpTestConfig(t)
	for i, l := range upstreams {
		n, err := model.NewNode(fmt.Sprint(i), "test", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port})
		if err != nil {
			t.Fatal(err)
		}
		c.Nodes[i] = n
	}
	c.SelectedNodeID = c.Nodes[0].ID
	c.Rules[1].NodeID = c.Nodes[1].ID
	data, err := buildForPlatform(c, "windows")
	if err != nil {
		t.Fatal(err)
	}
	registry := include.OutboundRegistry()
	records := make(chan socketObservation, 4)
	outbound.Register[wfpSocketOptions](registry, wfp.SocketOutbound, func(context.Context, adapter.Router, log.ContextLogger, string, wfpSocketOptions) (adapter.Outbound, error) {
		return &recordingWFPSocket{outbound.NewAdapter(wfp.SocketOutbound, wfp.SocketOutbound, []string{"tcp"}, nil), records}, nil
	})
	ctx := box.Context(service.ContextWithDefaultRegistry(context.Background()), include.InboundRegistry(), registry,
		include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "transparent-response") }))
	defer server.Close()
	for i := range c.Rules {
		client, accepted := net.Pipe()
		_ = client.SetDeadline(time.Now().Add(3 * time.Second))
		redirect := wfp.Redirect{Rule: wfp.Rule{ID: uint32(i + 1), Program: c.Rules[i]}, PID: 1234, ProcessPath: c.Rules[i].Path, Destination: netip.MustParseAddrPort(server.Listener.Addr().String())}
		go routeWFP(instance.Router())(wfp.WithRecords(ctx, []byte(fmt.Sprint(i))), accepted, redirect)
		_, err = io.WriteString(client, "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		_ = client.Close()
		if err != nil || string(body) != "transparent-response" {
			t.Fatalf("transparent forwarding failed: %q %v", body, err)
		}
		select {
		case got := <-records:
			if got.records != fmt.Sprint(i) || got.destination != upstreams[i].Addr().String() ||
				got.metadata.Inbound != wfpInbound(uint32(i+1)) || got.metadata.Outbound != nodeTag(c, c.Rules[i].NodeID) ||
				got.metadata.ProcessInfo == nil || got.metadata.ProcessInfo.ProcessID != 1234 || got.metadata.ProcessInfo.ProcessPath != c.Rules[i].Path {
				t.Fatalf("lost per-connection records, node or process metadata: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("physical upstream lost WFP records")
		}
	}
}
