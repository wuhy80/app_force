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
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"appforce/internal/model"
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
	e := &Engine{CachePath: filepath.Join(t.TempDir(), "cache.db")}
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
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Running {
		t.Fatal("engine still running")
	}
	if err := e.Start(c); err != nil {
		t.Fatalf("restart failed: %v", err)
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
}
