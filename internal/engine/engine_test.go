package engine

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"appforce/internal/model"
	"appforce/internal/traffic"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/option"
	routing "github.com/sagernet/sing-box/route/rule"
	M "github.com/sagernet/sing/common/metadata"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func socksServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				r := bufio.NewReader(conn)
				header := make([]byte, 2)
				if _, err := io.ReadFull(r, header); err != nil {
					return
				}
				methods := make([]byte, int(header[1]))
				if _, err := io.ReadFull(r, methods); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				request := make([]byte, 4)
				if _, err := io.ReadFull(r, request); err != nil {
					return
				}
				var host string
				switch request[3] {
				case 1:
					b := make([]byte, 4)
					if _, err := io.ReadFull(r, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 3:
					count, err := r.ReadByte()
					if err != nil {
						return
					}
					b := make([]byte, int(count))
					if _, err := io.ReadFull(r, b); err != nil {
						return
					}
					host = string(b)
				case 4:
					b := make([]byte, 16)
					if _, err := io.ReadFull(r, b); err != nil {
						return
					}
					host = net.IP(b).String()
				default:
					return
				}
				p := make([]byte, 2)
				if _, err := io.ReadFull(r, p); err != nil {
					return
				}
				remote, err := net.DialTimeout("tcp", net.JoinHostPort(host, stringPort(int(binary.BigEndian.Uint16(p)))), 3*time.Second)
				if err != nil {
					return
				}
				defer remote.Close()
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(remote, r)
				_, _ = io.Copy(conn, remote)
			}()
		}
	}()
	return listener
}

func stringPort(port int) string {
	var b [5]byte
	i := len(b)
	for {
		i--
		b[i] = byte(port%10) + '0'
		port /= 10
		if port == 0 {
			break
		}
	}
	return string(b[i:])
}

func TestEmbeddedCoreActuallyForwardsAndReportsTraffic(t *testing.T) {
	upstream := socksServer(t)
	node, err := model.NewNode("local test", "test", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": upstream.Addr().(*net.TCPAddr).Port, "version": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNode(node); err != nil {
		t.Fatal(err)
	}
	c := model.DefaultConfig()
	c.Nodes = []model.Node{node}
	c.SelectedNodeID = node.ID
	c.Settings.Mode = "mixed"
	c.Settings.MixedPort = freePort(t)
	dir := t.TempDir()
	recorder, err := traffic.Open(filepath.Join(dir, "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{CachePath: filepath.Join(dir, "cache.db"), Recorder: recorder}
	if err := e.Start(c); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "through embedded sing-box") }))
	defer target.Close()
	proxy, _ := url.Parse("http://127.0.0.1:" + stringPort(c.Settings.MixedPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "through embedded sing-box" {
		t.Fatalf("unexpected body %s", body)
	}
	status := e.Snapshot()
	if !status.Running || status.Upload == 0 || status.Download == 0 {
		t.Fatalf("missing real counters: %+v", status)
	}
	if len(status.Connections) == 0 || status.Connections[0].Node == nil || status.Connections[0].Node.ID != node.ID || status.Connections[0].Node.Server != node.Server {
		t.Fatalf("running connection is missing its actual node: %+v", status.Connections)
	}
	report, err := recorder.Report(7, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Upload != status.Upload || report.Summary.Download != status.Download || len(report.Applications) != 1 || report.Applications[0].Name != "本地代理（未识别应用）" {
		t.Fatalf("application tracker disagrees with core: %+v vs %+v", report, status)
	}
	// Sampling/persistence must run even when no client requests the statistics page.
	deadline := time.Now().Add(7 * time.Second)
	for {
		automatic, err := traffic.Open(filepath.Join(dir, "traffic.db"))
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := automatic.Report(7, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Summary.Download >= report.Summary.Download {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("traffic was not automatically persisted")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Running {
		t.Fatal("engine still running")
	}
	reopened, err := traffic.Open(filepath.Join(dir, "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Report(7, "", false)
	if err != nil || saved.Summary.Download < report.Summary.Download || saved.Summary.ActiveConnections != 0 {
		t.Fatalf("stop did not persist final counters: %+v %v", saved, err)
	}
	if err := e.Start(c); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	resp, err = client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	continued, err := recorder.Report(7, "", false)
	if err != nil || continued.Summary.Download <= saved.Summary.Download {
		t.Fatalf("restart reset history: %+v %v", continued, err)
	}
}

func TestProcessRoutingDefaultsAndValidation(t *testing.T) {
	node, err := model.NewNode("node", "source", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": 1080})
	if err != nil {
		t.Fatal(err)
	}
	c := model.DefaultConfig()
	c.Nodes = []model.Node{node}
	c.SelectedNodeID = node.ID
	if _, err := Build(c); err == nil {
		t.Fatal("TUN without selected programs accepted")
	}
	c.Rules = []model.Rule{{ID: "rule", Path: "/test/selected", Enabled: true}, {ID: "disabled", Path: "/test/disabled", Enabled: false}}
	data, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/test/disabled") {
		t.Fatal("disabled rule leaked into config")
	}
	var config struct {
		Route struct {
			Final string           `json:"final"`
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Route.Final != "direct" {
		t.Fatal("unselected programs must use direct")
	}
	ctx := newContext()
	opts, err := decode(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Route.FindProcess {
		t.Fatal("process lookup disabled")
	}
	if _, err := decode(context.Background(), data); err == nil {
		t.Fatal("decode must require registries")
	}
	for _, choice := range []string{"system", "mixed", "gvisor", ""} {
		c.Settings.TUNStack = choice
		data, err := Build(c)
		if err != nil {
			t.Fatal(err)
		}
		opts, err := decode(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		want := choice
		if want == "" {
			want = "gvisor"
		}
		if opts.Inbounds[1].Options.(*option.TunInboundOptions).Stack != want || opts.Route.Final != "direct" {
			t.Fatalf("core config did not retain stack %q or unselected-program direct routing", choice)
		}
	}
}

func TestProgramRoutingPreservesOtherApplicationsDNS(t *testing.T) {
	node, err := model.NewNode("node", "source", map[string]any{"type": "socks", "server": "proxy.example", "server_port": 1080})
	if err != nil {
		t.Fatal(err)
	}
	selectedPath := `C:\Apps\Antigravity+ (Test)\Antigravity.exe`
	selectedHelper := `C:\Apps\Antigravity+ (Test)\resources\app\bin\language_server_windows_x64.exe`
	disabledAntigravity := `D:\Disabled\Antigravity\Antigravity.exe`
	otherPath := filepath.Join(t.TempDir(), "Other.exe")
	sharedResolver := filepath.Join(t.TempDir(), "svchost.exe")
	disabledPath := filepath.Join(t.TempDir(), "Disabled.exe")
	dingTalkPath := `C:\Program Files (x86)\DingDing\main\current\DingTalk.exe`
	for _, mode := range []string{"system", "direct", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			c := model.DefaultConfig()
			c.Nodes, c.SelectedNodeID = []model.Node{node}, node.ID
			c.Settings.DNSMode = mode
			c.Rules = []model.Rule{{Path: selectedPath, Enabled: true}, {Path: disabledPath, Enabled: false}, {Path: disabledAntigravity, Enabled: false}}
			data, err := buildForPlatform(c, "windows")
			if err != nil {
				t.Fatal(err)
			}
			ctx := newContext()
			opts, err := decode(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			tunOptions := opts.Inbounds[1].Options.(*option.TunInboundOptions)
			if tunOptions.DNSMode != "disabled" || len(tunOptions.DNSAddress) != 0 {
				t.Fatal("TUN still overrides shared system DNS or enables native hijacking")
			}
			if opts.DNS.Servers[0].Type != "local" || opts.DNS.Servers[0].Tag != "bootstrap" {
				t.Fatal("proxy bootstrap does not use independent system DNS")
			}
			if mode == "system" && (len(opts.DNS.Servers) != 1 || opts.DNS.Final != "bootstrap") {
				t.Fatal("system DNS mode still relies on a custom resolver")
			}
			var rules []adapter.Rule
			for _, options := range opts.Route.Rules {
				rule, err := routing.NewRule(ctx, nil, options, true)
				if err != nil {
					t.Fatal(err)
				}
				rules = append(rules, rule)
			}
			selectedDNS := "hijack-dns"
			if mode == "system" {
				selectedDNS = "direct"
			}
			for _, test := range []struct {
				name, processPath, destination, want string
			}{
				{"selected HTTPS", selectedPath, "173.194.203.95:443", "selected"},
				{"selected language server HTTPS", selectedHelper, "173.194.203.95:443", "selected"},
				{"selected newer language server HTTPS", `C:\Apps\Antigravity+ (Test)\bin\language_server.exe`, "173.194.203.95:443", "selected"},
				{"selected uppercase language server HTTPS", strings.ToUpper(selectedHelper), "173.194.203.95:443", "selected"},
				{"other installation language server HTTPS", `C:\Apps\Antigravity+ (Test)-copy\bin\language_server_windows_x64.exe`, "173.194.203.95:443", "direct"},
				{"unselected node HTTPS", `C:\Apps\Antigravity+ (Test)\bin\node.exe`, "173.194.203.95:443", "direct"},
				{"disabled language server HTTPS", `D:\Disabled\Antigravity\bin\language_server_windows_x64.exe`, "173.194.203.95:443", "direct"},
				{"other HTTPS", otherPath, "173.194.203.95:443", "direct"},
				{"unselected DingTalk HTTPS", dingTalkPath, "203.0.113.10:443", "direct"},
				{"unselected DingTalk IPv6 HTTPS", dingTalkPath, "[2001:db8::10]:443", "direct"},
				{"unselected DingTalk renderer HTTPS", `C:\Program Files (x86)\DingDing\main\current\DingTalkRender.exe`, "203.0.113.10:443", "direct"},
				{"unselected DingTalk DNS", dingTalkPath, "8.8.8.8:53", "direct"},
				{"unselected DingTalk router DNS", dingTalkPath, "192.168.1.1:53", "direct"},
				{"unknown HTTPS", "", "173.194.203.95:443", "direct"},
				{"selected DNS", selectedPath, "8.8.8.8:53", selectedDNS},
				{"selected language server DNS", selectedHelper, "8.8.8.8:53", selectedDNS},
				{"selected language server router DNS", selectedHelper, "192.168.1.1:53", selectedDNS},
				{"selected router DNS", selectedPath, "192.168.1.1:53", selectedDNS},
				{"other DNS", otherPath, "8.8.8.8:53", "direct"},
				{"shared Windows resolver", sharedResolver, "8.8.8.8:53", "direct"},
				{"shared router DNS", sharedResolver, "192.168.1.1:53", "direct"},
				{"unknown DNS", "", "8.8.8.8:53", "direct"},
				{"disabled program DNS", disabledPath, "8.8.8.8:53", "direct"},
				{"disabled language server DNS", `D:\Disabled\Antigravity\bin\language_server_windows_x64.exe`, "8.8.8.8:53", "direct"},
				{"selected LAN", selectedPath, "192.168.1.1:443", "direct"},
				{"selected language server LAN", selectedHelper, "192.168.1.1:443", "direct"},
			} {
				for _, network := range []string{"tcp", "udp"} {
					t.Run(test.name+"/"+network, func(t *testing.T) {
						for _, pid := range []uint32{100, 200, 9999} {
							metadata := adapter.InboundContext{
								Inbound: "tun", Network: network,
								Destination: M.SocksaddrFromNetIP(netip.MustParseAddrPort(test.destination)),
							}
							if test.processPath != "" {
								metadata.ProcessInfo = &adapter.ConnectionOwner{ProcessID: pid, ProcessPath: test.processPath}
							}
							got := opts.Route.Final
							for _, rule := range rules {
								if !rule.Match(&metadata) {
									continue
								}
								if action, ok := rule.Action().(*routing.RuleActionRoute); ok {
									got = action.Outbound
								} else {
									got = rule.Action().Type()
								}
								break
							}
							if got != test.want {
								t.Fatalf("unexpected route %q for PID %d, want %q", got, pid, test.want)
							}
						}
					})
				}
			}
		})
	}
}

func TestAppBundleRoutingSurvivesPIDChanges(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("application bundles are macOS-specific")
	}
	node, err := model.NewNode("node", "source", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": 1080})
	if err != nil {
		t.Fatal(err)
	}
	c := model.DefaultConfig()
	c.Nodes, c.SelectedNodeID = []model.Node{node}, node.ID
	c.Rules = []model.Rule{{Path: "/Applications/Browser+.app", Enabled: true}}
	data, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := decode(newContext(), data)
	if err != nil {
		t.Fatal(err)
	}
	var expressions []string
	for _, rule := range opts.Route.Rules {
		if rule.DefaultOptions.RouteOptions.Outbound == "selected" {
			expressions = append(expressions, rule.DefaultOptions.ProcessPathRegex...)
		}
	}
	if len(expressions) != 1 {
		t.Fatalf("bundle routing was not generated: %s", data)
	}
	matcher, err := routing.NewProcessPathRegexItem(expressions)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []uint32{100, 200, 9999} {
		for _, path := range []string{
			"/Applications/Browser+.app/Contents/MacOS/Browser",
			"/Applications/Browser+.app/Contents/Frameworks/Helper.app/Contents/MacOS/Helper",
		} {
			metadata := &adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessID: pid, ProcessPath: path}}
			if !matcher.Match(metadata) {
				t.Fatalf("selected application no longer matched after PID changed to %d: %s", pid, path)
			}
		}
	}
	for _, path := range []string{"/Applications/Browser+.app-copy/Contents/MacOS/Browser", "/Applications/BrowserX.app/Contents/MacOS/Browser"} {
		if matcher.Match(&adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessPath: path}}) {
			t.Fatalf("unselected application was proxied: %s", path)
		}
	}
}

func TestConnectionSnapshotKeepsProxiedTrafficWhenDirectTrafficExceedsLimit(t *testing.T) {
	now := time.Now()
	connections := make([]*trafficcontrol.TrackerMetadata, 0, 252)
	for i := 0; i < 250; i++ {
		connections = append(connections, &trafficcontrol.TrackerMetadata{
			Metadata:  adapter.InboundContext{Domain: stringPort(i) + ".direct.example"},
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Outbound:  "direct", Upload: new(atomic.Int64), Download: new(atomic.Int64),
		})
	}
	defaultNode := model.NodeView{ID: "default", Name: "默认节点", Server: "default.example", Port: 1080}
	programNode := model.NodeView{ID: "program", Name: "程序节点", Server: "program.example", Port: 2080}
	for i, domain := range []string{"older.proxy.example", "newer.proxy.example"} {
		upload, download := new(atomic.Int64), new(atomic.Int64)
		upload.Store(128)
		download.Store(512)
		tag := "selected"
		if i == 1 {
			tag = model.NodeOutboundTag(programNode.ID)
		}
		connections = append(connections, &trafficcontrol.TrackerMetadata{
			Metadata: adapter.InboundContext{Domain: domain, Network: "tcp", ProcessInfo: &adapter.ConnectionOwner{
				ProcessID: 99, ProcessPath: "/selected/program",
			}},
			CreatedAt: now.Add(time.Duration(i-2) * time.Second),
			Outbound:  tag, Upload: upload, Download: download,
		})
	}
	visible := snapshotConnections(connections, map[string]model.NodeView{"selected": defaultNode, model.NodeOutboundTag(programNode.ID): programNode})
	if len(visible) != 200 {
		t.Fatalf("display limit changed: %d", len(visible))
	}
	if !visible[0].Proxied || !visible[1].Proxied || visible[0].Destination != "newer.proxy.example" || visible[1].Destination != "older.proxy.example" {
		t.Fatal("proxy connections were hidden by newer direct traffic or incorrectly ordered")
	}
	if visible[0].PID != 99 || visible[0].ProcessPath != "/selected/program" || visible[0].Upload != 128 || visible[0].Download != 512 {
		t.Fatalf("proxy connection metadata lost: %+v", visible[0])
	}
	if visible[0].Node == nil || *visible[0].Node != programNode || visible[1].Node == nil || *visible[1].Node != defaultNode {
		t.Fatalf("actual outbound node metadata lost: %+v", visible[:2])
	}
	if visible[2].Proxied || visible[2].Destination != "249.direct.example" {
		t.Fatal("remaining space did not show the newest direct connections")
	}
	if visible[2].Node != nil {
		t.Fatal("direct connection incorrectly reports a proxy node")
	}
}
