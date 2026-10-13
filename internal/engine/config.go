package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"appforce/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

func newContext() context.Context {
	return include.Context(service.ContextWithDefaultRegistry(context.Background()))
}

func Build(c model.Config) ([]byte, error) {
	if err := c.Settings.Validate(); err != nil {
		return nil, err
	}
	node, ok := c.SelectedNode()
	if !ok {
		return nil, fmt.Errorf("请先选择代理节点")
	}
	var outbound map[string]any
	if err := json.Unmarshal(node.Outbound, &outbound); err != nil {
		return nil, err
	}
	outbound["tag"] = "selected"
	inbounds := []any{map[string]any{"type": "mixed", "tag": "local", "listen": "127.0.0.1", "listen_port": c.Settings.MixedPort}}
	rules := []any{map[string]any{"inbound": []string{"local"}, "action": "route", "outbound": "selected"}}
	if c.Settings.Mode == "tun" {
		enabled := 0
		for _, rule := range c.Rules {
			if rule.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			return nil, fmt.Errorf("请先添加并启用至少一个程序规则")
		}
		name := "appforce0"
		if runtime.GOOS == "darwin" {
			name = "utun"
		}
		tun := map[string]any{
			"type": "tun", "tag": "tun", "interface_name": name,
			"address":    []string{"172.29.231.1/30", "fdfe:afce:1::1/126"},
			"auto_route": true, "strict_route": c.Settings.StrictRoute,
			"dns_mode": "hijack", "mtu": 1400,
		}
		if runtime.GOOS == "linux" {
			tun["auto_redirect"] = c.Settings.AutoRedirect
		}
		inbounds = append(inbounds, tun)
		rules = append(rules,
			map[string]any{"protocol": "dns", "action": "hijack-dns"},
			map[string]any{"port": 53, "action": "hijack-dns"},
		)
		if path, err := os.Executable(); err == nil {
			rules = append(rules, map[string]any{"process_path": []string{path}, "action": "route", "outbound": "direct"})
		}
		// Preserve access to the management endpoint, SSH and other local services.
		rules = append(rules, map[string]any{"ip_is_private": true, "action": "route", "outbound": "direct"})
		for _, rule := range c.Rules {
			if rule.Enabled {
				rules = append(rules, map[string]any{"process_path": []string{rule.Path}, "action": "route", "outbound": "selected"})
			}
		}
	}
	resolver := map[string]any{"type": "https", "tag": "resolver", "server": c.Settings.DNSAddress, "server_port": 443, "path": "/dns-query"}
	if c.Settings.DNSMode == "proxy" {
		resolver["detour"] = "selected"
	}
	config := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{
			"servers": []any{
				resolver,
				map[string]any{"type": "udp", "tag": "bootstrap", "server": c.Settings.DNSAddress, "server_port": 53},
			},
			"final": "resolver", "strategy": "prefer_ipv4",
		},
		"inbounds":  inbounds,
		"outbounds": []any{outbound, map[string]any{"type": "direct", "tag": "direct"}},
		"route": map[string]any{
			"auto_detect_interface": true, "find_process": c.Settings.Mode == "tun",
			"default_domain_resolver": "bootstrap", "rules": rules, "final": "direct",
		},
	}
	return json.Marshal(config)
}

func decode(ctx context.Context, data []byte) (option.Options, error) {
	return sjson.UnmarshalExtendedContext[option.Options](ctx, data)
}

func ValidateNode(node model.Node) error {
	c := model.DefaultConfig()
	c.Nodes, c.SelectedNodeID, c.Settings.Mode = []model.Node{node}, node.ID, "mixed"
	data, err := Build(c)
	if err != nil {
		return err
	}
	ctx := newContext()
	opts, err := decode(ctx, data)
	if err != nil {
		return err
	}
	// No listeners, routes or interfaces are activated by construction.
	opts.Inbounds = nil
	instance, err := box.New(box.Options{Options: opts, Context: ctx})
	if err != nil {
		return err
	}
	return instance.Close()
}
