package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const Version = "0.1.1"

type Node struct {
	ID       string          `json:"id"`
	SourceID string          `json:"source_id"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Server   string          `json:"server"`
	Port     int             `json:"port"`
	Outbound json.RawMessage `json:"outbound"`
}

type NodeView struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
}

func (n Node) View() NodeView { return NodeView{n.ID, n.SourceID, n.Name, n.Type, n.Server, n.Port} }

func NewNode(name, sourceID string, outbound map[string]any) (Node, error) {
	typ, _ := outbound["type"].(string)
	server, _ := outbound["server"].(string)
	port, err := Number(outbound["server_port"])
	if typ == "" || strings.TrimSpace(server) == "" || err != nil || port < 1 || port > 65535 {
		return Node{}, fmt.Errorf("节点必须包含协议、服务器和有效端口")
	}
	delete(outbound, "tag")
	for _, field := range []string{"detour", "bind_interface", "inet4_bind_address", "inet6_bind_address", "routing_mark", "domain_resolver", "network_namespace"} {
		if _, exists := outbound[field]; exists {
			return Node{}, fmt.Errorf("独立节点不支持 %s；请移除对外部环境的依赖", field)
		}
	}
	raw, err := json.Marshal(outbound)
	if err != nil {
		return Node{}, err
	}
	sum := sha256.Sum256(append([]byte(sourceID+"\x00"), raw...))
	if strings.TrimSpace(name) == "" {
		name = net.JoinHostPort(server, fmt.Sprint(port))
	}
	return Node{hex.EncodeToString(sum[:12]), sourceID, name, typ, server, port, raw}, nil
}

func Number(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("必须是整数")
		}
		return int(v), nil
	case json.Number:
		i, err := v.Int64()
		return int(i), err
	case string:
		var i int
		_, err := fmt.Sscanf(v, "%d", &i)
		if err != nil || fmt.Sprint(i) != v {
			return 0, fmt.Errorf("必须是整数")
		}
		return i, nil
	default:
		return 0, fmt.Errorf("必须是整数")
	}
}

type Source struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Warnings  []string  `json:"warnings"`
}

type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
	NodeID  string `json:"node_id,omitempty"`
}

func NodeOutboundTag(nodeID string) string { return "node-" + nodeID }

func IsProxyOutbound(tag string) bool {
	return tag == "selected" || strings.HasPrefix(tag, "node-")
}

func NormalizeRule(rule Rule) (Rule, error) {
	rule.Path = strings.TrimSpace(rule.Path)
	if !filepath.IsAbs(rule.Path) || strings.ContainsAny(rule.Path, "\x00\r\n") {
		return rule, fmt.Errorf("请选择或填写程序的绝对路径")
	}
	rule.Path = filepath.Clean(rule.Path)
	if rule.Name == "" {
		rule.Name = filepath.Base(rule.Path)
		if IsAppBundle(rule.Path) {
			rule.Name = strings.TrimSuffix(rule.Name, ".app")
		}
	}
	// Match the executable's real path, including selections made through symlinks.
	// Missing files remain valid so rules can be saved before installation.
	if resolved, err := filepath.EvalSymlinks(rule.Path); err == nil {
		rule.Path = resolved
	}
	if len(rule.Name) > 200 {
		return rule, fmt.Errorf("程序名称过长")
	}
	if rule.ID == "" {
		rule.ID = NewID()
	}
	return rule, nil
}

func SamePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func IsAppBundle(path string) bool {
	return runtime.GOOS == "darwin" && strings.HasSuffix(filepath.Clean(path), ".app")
}

// MatchesPath includes the helper executables covered by an application rule.
func (r Rule) MatchesPath(path string) bool {
	if expressions := r.ProcessPathRegex(); len(expressions) != 0 {
		for _, expression := range expressions {
			if regexp.MustCompile(expression).MatchString(path) {
				return true
			}
		}
		return false
	}
	return SamePath(r.Path, path)
}

const DefaultTUNStack = "gvisor"

type Settings struct {
	Mode         string `json:"mode"`
	TUNStack     string `json:"tun_stack"`
	MixedPort    int    `json:"mixed_port"`
	DNSMode      string `json:"dns_mode"`
	DNSAddress   string `json:"dns_address"`
	StrictRoute  bool   `json:"strict_route"`
	AutoRedirect bool   `json:"auto_redirect"`
}

// EffectiveTUNStack supports configurations and API clients predating this setting.
func (s Settings) EffectiveTUNStack() string {
	if s.TUNStack == "" {
		return DefaultTUNStack
	}
	return s.TUNStack
}

func (s Settings) Validate() error {
	if s.Mode != "tun" && s.Mode != "mixed" {
		return fmt.Errorf("代理模式必须是 tun 或 mixed")
	}
	switch s.EffectiveTUNStack() {
	case "gvisor", "mixed", "system":
	default:
		return fmt.Errorf("TUN 网络栈必须是 gvisor、mixed 或 system")
	}
	if s.MixedPort < 1024 || s.MixedPort > 65535 {
		return fmt.Errorf("本地代理端口必须在 1024–65535 之间")
	}
	if s.DNSMode != "system" && s.DNSMode != "direct" && s.DNSMode != "proxy" {
		return fmt.Errorf("DNS 模式必须是 system、direct 或 proxy")
	}
	if s.DNSMode != "system" && net.ParseIP(s.DNSAddress) == nil {
		return fmt.Errorf("DNS 服务器必须是 IP 地址")
	}
	return nil
}

type Config struct {
	Version        int      `json:"version"`
	SelectedNodeID string   `json:"selected_node_id"`
	Nodes          []Node   `json:"nodes"`
	Sources        []Source `json:"sources"`
	Rules          []Rule   `json:"rules"`
	Devices        []Device `json:"devices"`
	Settings       Settings `json:"settings"`
}

type Device struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Token             string `json:"token,omitempty"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
}

func DefaultConfig() Config {
	return Config{Version: 1, Nodes: []Node{}, Sources: []Source{}, Rules: []Rule{}, Devices: []Device{}, Settings: Settings{Mode: "tun", TUNStack: DefaultTUNStack, MixedPort: 17890, DNSMode: "system", DNSAddress: "8.8.8.8", AutoRedirect: true}}
}

func (c Config) SelectedNode() (Node, bool) {
	return c.Node(c.SelectedNodeID)
}

func (c Config) Node(id string) (Node, bool) {
	for _, n := range c.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func (c Config) Clone() Config {
	b, _ := json.Marshal(c)
	var copy Config
	_ = json.Unmarshal(b, &copy)
	return copy
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
