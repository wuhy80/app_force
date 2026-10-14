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
	return buildForPlatform(c, runtime.GOOS)
}

func buildForPlatform(c model.Config, goos string) ([]byte, error) {
	if err := c.Settings.Validate(); err != nil {
		return nil, err
	}
	nodes, err := outboundNodes(c)
	if err != nil {
		return nil, err
	}
	outbounds := make([]any, 0, len(nodes)+1)
	for _, node := range nodes {
		var outbound map[string]any
		if err := json.Unmarshal(node.Outbound, &outbound); err != nil {
			return nil, err
		}
		outbound["tag"] = nodeTag(c, node.ID)
		outbounds = append(outbounds, outbound)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})
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
		if goos == "darwin" {
			name = "utun"
		}
		tun := map[string]any{
			"type": "tun", "tag": "tun", "interface_name": name,
			"address":    []string{"172.29.231.1/30", "fdfe:afce:1::1/126"},
			"auto_route": true, "strict_route": c.Settings.StrictRoute,
			// Native interface DNS affects every application, including Windows DNS Client.
			// Keep it unchanged and intercept only identifiable selected-program queries.
			"dns_mode": "disabled", "mtu": 1400, "stack": c.Settings.EffectiveTUNStack(),
		}
		if goos == "linux" {
			tun["auto_redirect"] = c.Settings.AutoRedirect
		}
		inbounds = append(inbounds, tun)
		if path, err := os.Executable(); err == nil {
			rules = append(rules, map[string]any{"process_path": []string{path}, "action": "route", "outbound": "direct"})
		}
		if c.Settings.DNSMode != "system" {
			for _, rule := range c.Rules {
				if rule.Enabled {
					dnsRule := programMatch(rule)
					dnsRule["port"] = 53
					dnsRule["action"] = "hijack-dns"
					rules = append(rules, dnsRule)
				}
			}
		}
		// Shared OS resolvers cannot be attributed to the requesting application.
		// Preserve their destination, and other applications' DNS, even in proxy DNS mode.
		rules = append(rules, map[string]any{"port": 53, "action": "route", "outbound": "direct"})
		// Preserve access to the management endpoint, SSH and other local services.
		rules = append(rules, map[string]any{"ip_is_private": true, "action": "route", "outbound": "direct"})
		for _, rule := range c.Rules {
			if rule.Enabled {
				route := programMatch(rule)
				route["action"], route["outbound"] = "route", nodeTag(c, rule.NodeID)
				rules = append(rules, route)
			}
		}
	}
	// Bootstrap must stay independent of the proxy and use the existing system DNS.
	dnsServers := []any{map[string]any{"type": "local", "tag": "bootstrap"}}
	dnsRules := []any{}
	dnsFinal := "bootstrap"
	if c.Settings.DNSMode != "system" {
		resolver := map[string]any{"type": "https", "tag": "resolver", "server": c.Settings.DNSAddress, "server_port": 443, "path": "/dns-query"}
		if c.Settings.DNSMode == "proxy" {
			resolver["detour"] = "selected"
		}
		dnsServers = append(dnsServers, resolver)
		dnsFinal = "resolver"
		if c.Settings.DNSMode == "proxy" && c.Settings.Mode == "tun" {
			// Identifiable program DNS uses the same node as that program's connections.
			for _, node := range nodes[1:] {
				tag := nodeTag(c, node.ID)
				dnsServers = append(dnsServers, map[string]any{
					"type": "https", "tag": "resolver-" + tag, "server": c.Settings.DNSAddress,
					"server_port": 443, "path": "/dns-query", "detour": tag,
				})
			}
			for _, rule := range c.Rules {
				if rule.Enabled {
					dnsRule := programMatch(rule)
					server := "resolver"
					if tag := nodeTag(c, rule.NodeID); tag != "selected" {
						server = "resolver-" + tag
					}
					dnsRule["action"], dnsRule["server"] = "route", server
					dnsRules = append(dnsRules, dnsRule)
				}
			}
		}
	}
	config := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{
			"servers": dnsServers,
			"rules":   dnsRules,
			"final":   dnsFinal, "strategy": "prefer_ipv4",
		},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"auto_detect_interface": true, "find_process": c.Settings.Mode == "tun",
			"default_domain_resolver": "bootstrap", "rules": rules, "final": "direct",
		},
	}
	return json.Marshal(config)
}

func nodeTag(c model.Config, nodeID string) string {
	if nodeID == "" || nodeID == c.SelectedNodeID {
		return "selected"
	}
	return model.NodeOutboundTag(nodeID)
}

// Only enabled rules need extra outbounds; explicit missing nodes never fall back.
func outboundNodes(c model.Config) ([]model.Node, error) {
	selected, ok := c.SelectedNode()
	if !ok {
		return nil, fmt.Errorf("请先选择代理节点")
	}
	nodes := []model.Node{selected}
	seen := map[string]bool{selected.ID: true}
	if c.Settings.Mode == "tun" {
		for _, rule := range c.Rules {
			if !rule.Enabled || rule.NodeID == "" || seen[rule.NodeID] {
				continue
			}
			node, ok := c.Node(rule.NodeID)
			if !ok {
				name := rule.Name
				if name == "" {
					name = rule.Path
				}
				return nil, fmt.Errorf("程序 %s 指定的节点不存在，请重新选择节点或跟随默认节点", name)
			}
			nodes = append(nodes, node)
			seen[node.ID] = true
		}
	}
	return nodes, nil
}

func programMatch(rule model.Rule) map[string]any {
	if expressions := rule.ProcessPathRegex(); len(expressions) != 0 {
		return map[string]any{"process_path_regex": expressions}
	}
	return map[string]any{"process_path": []string{rule.Path}}
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
