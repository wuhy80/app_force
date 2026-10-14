package engine

import (
	"net/netip"
	"strings"
	"testing"

	"appforce/internal/model"
	"github.com/sagernet/sing-box/adapter"
	routing "github.com/sagernet/sing-box/route/rule"
	M "github.com/sagernet/sing/common/metadata"
)

func TestProgramNodesOverrideDefaultAndKeepDNSAligned(t *testing.T) {
	var nodes []model.Node
	for i, name := range []string{"default", "program", "next default"} {
		node, err := model.NewNode(name, "test", map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": 1080 + i})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	c := model.DefaultConfig()
	c.Nodes = nodes
	c.Settings.DNSMode = "proxy"
	c.Rules = []model.Rule{
		{Name: "default app", Path: "/apps/default", Enabled: true},
		{Name: "program app", Path: `C:\Apps\Antigravity\Antigravity.exe`, NodeID: nodes[1].ID, Enabled: true},
		{Name: "pinned app", Path: "/apps/pinned", NodeID: nodes[0].ID, Enabled: true},
		{Name: "shared node", Path: "/apps/shared", NodeID: nodes[1].ID, Enabled: true},
		{Name: "disabled", Path: "/apps/disabled", NodeID: "missing", Enabled: false},
	}
	for _, selected := range []model.Node{nodes[0], nodes[2]} {
		t.Run(selected.Name, func(t *testing.T) {
			c.SelectedNodeID = selected.ID
			data, err := Build(c)
			if err != nil {
				t.Fatal(err)
			}
			ctx := newContext()
			opts, err := decode(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			wantOutbounds := 3 // default, program, direct
			if selected.ID != nodes[0].ID {
				wantOutbounds++ // formerly default node remains pinned
			}
			if len(opts.Outbounds) != wantOutbounds || len(opts.DNS.Servers) != wantOutbounds {
				t.Fatalf("outbounds or DNS servers were duplicated or omitted: %s", data)
			}
			var routes []adapter.Rule
			for _, options := range opts.Route.Rules {
				rule, err := routing.NewRule(ctx, nil, options, true)
				if err != nil {
					t.Fatal(err)
				}
				routes = append(routes, rule)
			}
			var dnsRules []adapter.DNSRule
			for _, options := range opts.DNS.Rules {
				rule, err := routing.NewDNSRule(ctx, nil, options, true, false)
				if err != nil {
					t.Fatal(err)
				}
				dnsRules = append(dnsRules, rule)
			}
			for _, test := range []struct{ path, want string }{
				{"/apps/default", "selected"},
				{c.Rules[1].Path, model.NodeOutboundTag(nodes[1].ID)},
				{`C:\Apps\Antigravity\resources\bin\language_server_windows_x64.exe`, model.NodeOutboundTag(nodes[1].ID)},
				{"/apps/pinned", nodeTag(c, nodes[0].ID)},
				{"/apps/shared", model.NodeOutboundTag(nodes[1].ID)},
				{"/apps/disabled", "direct"},
				{"/apps/other", "direct"},
			} {
				for _, network := range []string{"tcp", "udp"} {
					metadata := adapter.InboundContext{Inbound: "tun", Network: network,
						Destination: M.SocksaddrFromNetIP(netip.MustParseAddrPort("203.0.113.10:443")),
						ProcessInfo: &adapter.ConnectionOwner{ProcessID: 123, ProcessPath: test.path}}
					got := opts.Route.Final
					for _, rule := range routes {
						if rule.Match(&metadata) {
							got = rule.Action().(*routing.RuleActionRoute).Outbound
							break
						}
					}
					if got != test.want {
						t.Fatalf("%s %s: got %s, want %s", network, test.path, got, test.want)
					}
					if test.want == "direct" {
						continue // DNS for unselected programs is not hijacked.
					}
					metadata.Destination = M.Socksaddr{}
					server := opts.DNS.Final
					for _, rule := range dnsRules {
						if rule.Match(&metadata) {
							server = rule.Action().(*routing.RuleActionDNSRoute).Server
							break
						}
					}
					wantServer := "resolver"
					if test.want != "selected" {
						wantServer += "-" + test.want
					}
					if server != wantServer {
						t.Fatalf("DNS for %s: got %s, want %s", test.path, server, wantServer)
					}
				}
			}
		})
	}
	c.SelectedNodeID = nodes[0].ID
	c.Rules[1].NodeID = "removed-node"
	if _, err := Build(c); err == nil || !strings.Contains(err.Error(), "Antigravity") && !strings.Contains(err.Error(), "program app") {
		t.Fatalf("missing explicit node silently fell back: %v", err)
	}
}
