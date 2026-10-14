//go:build linux || darwin || openharmony

package mobile

import (
	"appforce/internal/model"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

const (
	SchemaVersion   = 1
	MaxApplications = 64
	DefaultBundle   = "com.appforce.mobile"
)

var bundlePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

type Application struct {
	BundleName string `json:"bundle_name"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
}

type Settings struct {
	BootstrapDNS string `json:"bootstrap_dns"`
	RemoteDNS    string `json:"remote_dns"`
}

type Config struct {
	Version        int            `json:"version"`
	Revision       uint64         `json:"revision"`
	SelfBundle     string         `json:"self_bundle"`
	Sources        []model.Source `json:"sources"`
	Nodes          []model.Node   `json:"nodes"`
	SelectedNodeID string         `json:"selected_node_id"`
	Applications   []Application  `json:"applications"`
	Settings       Settings       `json:"settings"`
}

func defaultConfig() Config {
	return Config{Version: SchemaVersion, SelfBundle: DefaultBundle, Sources: []model.Source{}, Nodes: []model.Node{}, Applications: []Application{}, Settings: Settings{BootstrapDNS: "223.5.5.5", RemoteDNS: "1.1.1.1"}}
}

func normalizeApplication(a Application, selfBundle string) (Application, error) {
	a.BundleName = strings.TrimSpace(a.BundleName)
	a.Name = strings.TrimSpace(a.Name)
	if len(a.BundleName) > 128 || !bundlePattern.MatchString(a.BundleName) {
		return a, fmt.Errorf("请输入有效的应用包名，例如 com.example.app")
	}
	if a.BundleName == selfBundle {
		return a, fmt.Errorf("不能将 AppForce 自身加入代理范围")
	}
	if a.Name == "" {
		a.Name = a.BundleName
	}
	if len([]rune(a.Name)) > 100 {
		return a, fmt.Errorf("应用名称过长")
	}
	return a, nil
}

func (c Config) selectedNode() (model.Node, error) {
	for _, n := range c.Nodes {
		if n.ID == c.SelectedNodeID {
			return n, nil
		}
	}
	return model.Node{}, fmt.Errorf("请先选择可用的代理节点")
}

func (c Config) validate() error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("不支持的配置版本 %d", c.Version)
	}
	if !bundlePattern.MatchString(c.SelfBundle) {
		return fmt.Errorf("应用自身包名无效")
	}
	if len(c.Applications) > MaxApplications {
		return fmt.Errorf("当前版本最多支持 %d 个应用", MaxApplications)
	}
	seen := map[string]bool{}
	for _, a := range c.Applications {
		if _, err := normalizeApplication(a, c.SelfBundle); err != nil {
			return err
		}
		if seen[a.BundleName] {
			return fmt.Errorf("应用包名重复")
		}
		seen[a.BundleName] = true
	}
	for _, ip := range []string{c.Settings.BootstrapDNS, c.Settings.RemoteDNS} {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !addr.IsGlobalUnicast() || addr.IsPrivate() {
			return fmt.Errorf("DNS 服务器必须是公共 IP 地址")
		}
	}
	return nil
}

type State struct {
	Version        string           `json:"version"`
	Revision       uint64           `json:"revision"`
	SelfBundle     string           `json:"self_bundle"`
	Sources        []model.Source   `json:"sources"`
	Nodes          []model.NodeView `json:"nodes"`
	SelectedNodeID string           `json:"selected_node_id"`
	Applications   []Application    `json:"applications"`
	Settings       Settings         `json:"settings"`
	Runtime        RuntimeState     `json:"runtime"`
}

func publicState(c Config, runtime RuntimeState) State {
	nodes := make([]model.NodeView, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodes = append(nodes, n.View())
	}
	sources := append([]model.Source{}, c.Sources...)
	for i := range sources {
		if sources[i].URL != "" {
			sources[i].URL = "remote"
		}
	}
	return State{model.Version, c.Revision, c.SelfBundle, sources, nodes, c.SelectedNodeID, append([]Application{}, c.Applications...), c.Settings, runtime}
}
