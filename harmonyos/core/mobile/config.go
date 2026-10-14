//go:build linux || darwin || openharmony

package mobile

import (
	"appforce/internal/model"
	"context"
	"encoding/json"
	"fmt"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

const (
	TUNIPv4 = "172.29.231.1"
	TUNIPv6 = "fdfe:afce:1::1"
	TUNDNS  = "172.29.231.2"
	TUNMTU  = 1400
)

type VPNPlan struct {
	Applications []string `json:"applications"`
	IPv4         string   `json:"ipv4"`
	IPv6         string   `json:"ipv6"`
	DNS          string   `json:"dns"`
	MTU          int      `json:"mtu"`
}

func PrepareVPN(c Config) (VPNPlan, error) {
	if err := c.validate(); err != nil {
		return VPNPlan{}, err
	}
	if _, err := c.selectedNode(); err != nil {
		return VPNPlan{}, err
	}
	apps := []string{}
	for _, a := range c.Applications {
		if a.Enabled {
			apps = append(apps, a.BundleName)
		}
	}
	// An empty OS allowlist can capture the entire phone. Never create that VPN.
	if len(apps) == 0 {
		return VPNPlan{}, fmt.Errorf("请先添加并启用至少一个应用")
	}
	return VPNPlan{apps, TUNIPv4, TUNIPv6, TUNDNS, TUNMTU}, nil
}

func BuildCoreConfig(c Config) ([]byte, error) {
	if _, err := PrepareVPN(c); err != nil {
		return nil, err
	}
	node, _ := c.selectedNode()
	var outbound map[string]any
	if err := json.Unmarshal(node.Outbound, &outbound); err != nil {
		return nil, fmt.Errorf("节点配置无效")
	}
	outbound["tag"] = "selected"
	return json.Marshal(map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{
			"servers": []any{
				// An omitted detour uses the protected default dialer. sing-box
				// rejects detouring DNS to a direct outbound with no dialer options.
				map[string]any{"type": "udp", "tag": "bootstrap", "server": c.Settings.BootstrapDNS},
				map[string]any{"type": "https", "tag": "remote", "server": c.Settings.RemoteDNS, "path": "/dns-query", "detour": "selected"},
			},
			"final": "remote", "strategy": "prefer_ipv4",
		},
		"inbounds": []any{map[string]any{
			"type": "tun", "tag": "phone", "interface_name": "appforce-vpn",
			"address":    []string{TUNIPv4 + "/30", TUNIPv6 + "/126"},
			"auto_route": false, "strict_route": false, "dns_mode": "disabled", "mtu": TUNMTU, "stack": "gvisor",
		}},
		"outbounds": []any{outbound, map[string]any{"type": "direct", "tag": "direct"}},
		"route": map[string]any{
			// sing-box only installs its platform socket protector with this enabled.
			// The platform supplies a logical protected outlet, never OS route discovery.
			"auto_detect_interface": true, "find_process": false, "default_domain_resolver": "bootstrap",
			"rules": []any{
				map[string]any{"port": 53, "action": "hijack-dns"},
				map[string]any{"ip_is_private": true, "action": "route", "outbound": "direct"},
			},
			"final": "selected",
		},
	})
}

func coreContext(p adapter.PlatformInterface) context.Context {
	ctx := include.Context(service.ContextWithDefaultRegistry(context.Background()))
	service.MustRegister[adapter.PlatformInterface](ctx, p)
	return ctx
}

func decodeCore(ctx context.Context, data []byte) (option.Options, error) {
	return sjson.UnmarshalExtendedContext[option.Options](ctx, data)
}

func validateNode(n model.Node) error {
	c := defaultConfig()
	c.Nodes = []model.Node{n}
	c.SelectedNodeID = n.ID
	c.Applications = []Application{{BundleName: "com.example.validation", Name: "validation", Enabled: true}}
	data, err := BuildCoreConfig(c)
	if err != nil {
		return err
	}
	ctx := coreContext(newPlatform(nil, nil))
	opts, err := decodeCore(ctx, data)
	if err != nil {
		return err
	}
	opts.Inbounds = nil
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return err
	}
	return instance.Close()
}
