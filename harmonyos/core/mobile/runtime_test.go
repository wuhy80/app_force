//go:build with_gvisor && (linux || darwin || openharmony)

package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/link/channel"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv4"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv6"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
	tun "github.com/sagernet/sing-tun"
	"golang.org/x/sys/unix"
)

// Exercise the production TUN inbound without changing host routes or requiring
// VPN permission. Only the OS packet device and protect API are substituted.
type packetTUN struct{ endpoint *channel.Endpoint }

func (d *packetTUN) Read([]byte) (int, error)             { return 0, io.EOF }
func (d *packetTUN) Write([]byte) (int, error)            { return 0, io.ErrClosedPipe }
func (d *packetTUN) Name() (string, error)                { return "packet-fixture", nil }
func (d *packetTUN) Start() error                         { return nil }
func (d *packetTUN) Close() error                         { d.endpoint.Close(); return nil }
func (d *packetTUN) UpdateRouteOptions(tun.Options) error { return nil }
func (d *packetTUN) NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error) {
	return d.endpoint, stack.NICOptions{}, nil
}
func (d *packetTUN) WritePacket(packet *stack.PacketBuffer) (int, error) {
	var packets stack.PacketBufferList
	packets.PushBack(packet)
	if _, err := d.endpoint.WritePackets(packets); err != nil {
		return 0, net.ErrClosed
	}
	return packet.Size(), nil
}

func TestRuntimeForwardsHTTPSAndFailsClosedWhenProtectionFails(t *testing.T) {
	for _, family := range []struct {
		name, source, destination string
		protocol                  tcpip.NetworkProtocolNumber
	}{
		// sing-box treats documentation ranges as non-public and directs them
		// through its LAN exemption. These addresses are intercepted in-memory;
		// the CONNECT fixture always forwards to the local TLS server.
		{"ipv4", "172.29.231.2/30", "1.2.3.4", ipv4.ProtocolNumber},
		{"ipv6", "fdfe:afce:1::2/126", "2001:4860::8888", ipv6.ProtocolNumber},
	} {
		for _, deny := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deny=%v", family.name, deny), func(t *testing.T) {
				payload := bytes.Repeat([]byte("AppForce mobile HTTPS payload\n"), 16384)
				target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
				defer target.Close()
				var upstreamHits atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					upstreamHits.Add(1)
					if req.Method != http.MethodConnect || req.Host != net.JoinHostPort(family.destination, "443") {
						t.Errorf("unexpected proxy request: %s %s", req.Method, req.Host)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					remote, err := net.DialTimeout("tcp", target.Listener.Addr().String(), time.Second)
					if err != nil {
						t.Error(err)
						w.WriteHeader(502)
						return
					}
					defer remote.Close()
					client, buffered, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer client.Close()
					_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
					_ = buffered.Flush()
					copied := make(chan struct{})
					go func() { _, _ = io.Copy(remote, buffered); _ = remote.Close(); close(copied) }()
					_, _ = io.Copy(client, remote)
					_ = client.Close()
					<-copied
				}))
				defer upstream.Close()
				dir := t.TempDir()
				content := fmt.Sprintf(`{"outbounds":[{"type":"http","tag":"fixture","server":"127.0.0.1","server_port":%d}]}`, upstream.Listener.Addr().(*net.TCPAddr).Port)
				if reply := invoke(t, Request{Method: "import", Directory: dir, Content: content}); !reply.OK {
					t.Fatal(reply.Error)
				}
				if reply := invoke(t, Request{Method: "saveApplication", Directory: dir, Application: Application{BundleName: "com.example.browser", Enabled: true}}); !reply.OK {
					t.Fatal(reply.Error)
				}
				virtual := &packetTUN{endpoint: channel.New(1024, TUNMTU, "")}
				var protected atomic.Int64
				var runtime Runtime
				if err := runtime.Start(dir, func(opts *tun.Options) (tun.Tun, error) {
					if opts.AutoRoute || opts.MTU != TUNMTU {
						return nil, fmt.Errorf("invalid mobile TUN options")
					}
					return virtual, nil
				}, func(fd int) error {
					protected.Add(1)
					if _, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil {
						return err
					}
					if deny {
						return fmt.Errorf("fixture denied protection")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				defer runtime.Stop()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				clientStack := stack.New(stack.Options{
					NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
					TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
				})
				defer clientStack.Close()
				clientLink := channel.New(1024, TUNMTU, "")
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
				clientStack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}, {Destination: header.IPv6EmptySubnet, NIC: 1}})
				var pumps sync.WaitGroup
				pump := func(from, to *channel.Endpoint) {
					defer pumps.Done()
					for packet := from.ReadContext(ctx); packet != nil; packet = from.ReadContext(ctx) {
						incoming := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: packet.ToBuffer()})
						to.InjectInbound(packet.NetworkProtocolNumber, incoming)
						incoming.DecRef()
						packet.DecRef()
					}
				}
				pumps.Add(2)
				go pump(clientLink, virtual.endpoint)
				go pump(virtual.endpoint, clientLink)
				defer func() { cancel(); pumps.Wait() }()
				transport := &http.Transport{DisableKeepAlives: true,
					TLSClientConfig: target.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return gonet.DialContextTCP(ctx, clientStack, tcpip.FullAddress{
							NIC: 1, Addr: tcpip.AddrFromSlice(netip.MustParseAddr(family.destination).AsSlice()), Port: 443,
						}, family.protocol)
					},
				}
				defer transport.CloseIdleConnections()
				response, err := (&http.Client{Transport: transport, Timeout: 20 * time.Second}).Get(target.URL)
				if deny {
					if response != nil {
						_ = response.Body.Close()
					}
					if err == nil || upstreamHits.Load() != 0 || protected.Load() == 0 {
						t.Fatalf("protection failure did not fail closed: err=%v hits=%d calls=%d", err, upstreamHits.Load(), protected.Load())
					}
				} else {
					if err != nil {
						t.Fatalf("%v; logs: %+v", err, runtime.snapshot().Logs)
					}
					got, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if readErr != nil || !bytes.Equal(got, payload) {
						t.Fatalf("truncated HTTPS response: %d/%d, %v", len(got), len(payload), readErr)
					}
					state := runtime.snapshot()
					if upstreamHits.Load() != 1 || protected.Load() == 0 || state.Download < int64(len(payload)) {
						t.Fatalf("missing proxy/protection/traffic: hits=%d protect=%d state=%+v", upstreamHits.Load(), protected.Load(), state)
					}
				}
				if reply := invoke(t, Request{Method: "requestStop", Directory: dir}); !reply.OK {
					t.Fatal(reply.Error)
				}
				deadline := time.Now().Add(3 * time.Second)
				for readRuntime(dir).Active && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				state := readRuntime(dir)
				if state.Active || state.Phase != "stopped" {
					t.Fatalf("mailbox stop did not release runtime: %+v", state)
				}
				if reply := invoke(t, Request{Method: "saveSettings", Directory: dir, Settings: defaultConfig().Settings}); !reply.OK {
					t.Fatal(reply.Error)
				}
				encoded, _ := json.Marshal(state.Logs)
				if !bytes.Contains(encoded, []byte(`"message"`)) {
					t.Fatal("log contract must use lowercase JSON fields")
				}
				// A later session must ignore stop requests from the session above.
				if err := runtime.Start(dir, func(*tun.Options) (tun.Tun, error) { return &packetTUN{channel.New(64, TUNMTU, "")}, nil }, func(int) error { return nil }); err != nil {
					t.Fatal(err)
				}
				runtime.mu.Lock()
				runtime.state.StartedAt = state.StartedAt // Identity must reject even a fresh/stale timestamp collision.
				runtime.mu.Unlock()
				time.Sleep(1100 * time.Millisecond)
				if !readRuntime(dir).Active {
					t.Fatal("old session stop command stopped a new session")
				}
			})
		}
	}
}

func TestFailureStatusDoesNotChangeConfiguration(t *testing.T) {
	dir, before := importedConfig(t)
	failed := invoke(t, Request{Method: "recordFailure", Directory: dir, Error: "系统拒绝 VPN 授权"})
	if !failed.OK || failed.State.Revision != before.Revision || failed.State.Runtime.Phase != "error" {
		t.Fatalf("failure changed configuration or disappeared: %+v", failed)
	}
	var runtime Runtime
	if err := runtime.Start(dir, func(*tun.Options) (tun.Tun, error) { return nil, fmt.Errorf("fixture open failed") }, func(int) error { return nil }); err == nil {
		t.Fatal("startup with no application succeeded")
	}
	if readRuntime(dir).Active {
		t.Fatal("failed start leaked the runtime lock")
	}
	if reply := invoke(t, Request{Method: "saveApplication", Directory: dir, Application: Application{BundleName: "com.example.browser", Enabled: true}}); !reply.OK {
		t.Fatal(reply.Error)
	}
	if err := runtime.Start(dir, func(*tun.Options) (tun.Tun, error) { return nil, fmt.Errorf("fixture open failed") }, func(int) error { return nil }); err == nil {
		t.Fatal("TUN failure was ignored")
	}
	if readRuntime(dir).Active {
		t.Fatal("failed TUN start leaked the runtime lock")
	}
	if reply := invoke(t, Request{Method: "state", Directory: dir}); !reply.OK || reply.State.Runtime.Phase != "error" {
		t.Fatal("startup error state was lost")
	}
}

func TestSocketGuardWaitsForNativeCallbackBeforeRelease(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	guard := &socketGuard{callback: func(int) error {
		calls.Add(1)
		close(entered)
		<-finish
		return nil
	}}
	protectDone := make(chan error, 1)
	go func() { protectDone <- guard.protect(10) }()
	<-entered
	closed := make(chan struct{})
	go func() { guard.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("native callback context could be released while still in use")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	if err := <-protectDone; err != nil {
		t.Fatal(err)
	}
	<-closed
	if err := guard.protect(11); err == nil || calls.Load() != 1 {
		t.Fatal("stopped core accessed the released native callback context")
	}
}
