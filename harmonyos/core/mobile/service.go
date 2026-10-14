//go:build linux || darwin || openharmony

package mobile

import (
	"appforce/internal/model"
	"appforce/internal/subscription"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type Request struct {
	Method      string      `json:"method"`
	Directory   string      `json:"directory"`
	Name        string      `json:"name"`
	Content     string      `json:"content"`
	SourceID    string      `json:"source_id"`
	URL         string      `json:"url"`
	NodeID      string      `json:"node_id"`
	SelfBundle  string      `json:"self_bundle"`
	Application Application `json:"application"`
	Settings    Settings    `json:"settings"`
	Error       string      `json:"error"`
}

type Reply struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	State     *State   `json:"state,omitempty"`
	VPN       *VPNPlan `json:"vpn,omitempty"`
	Imported  int      `json:"imported,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	SourceURL string   `json:"source_url,omitempty"`
}

func failure(err error) Reply { return Reply{Error: err.Error()} }

func Call(data []byte) Reply {
	if len(data) > 16<<20 {
		return failure(fmt.Errorf("请求内容过大"))
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return failure(fmt.Errorf("请求格式无效"))
	}
	s, err := OpenStore(req.Directory)
	if err != nil {
		return failure(err)
	}
	var cfg Config
	var vpn *VPNPlan
	var imported int
	var warnings []string
	var sourceURL string
	switch req.Method {
	case "initialize":
		cfg, err = s.locked(nil, false)
		if err == nil && req.SelfBundle != "" && cfg.SelfBundle != req.SelfBundle {
			cfg, err = s.locked(func(c *Config) error {
				if !bundlePattern.MatchString(req.SelfBundle) {
					return fmt.Errorf("应用自身包名无效")
				}
				for _, a := range c.Applications {
					if a.BundleName == req.SelfBundle {
						return fmt.Errorf("已有规则包含应用自身，请移除此规则")
					}
				}
				c.SelfBundle = req.SelfBundle
				return nil
			}, true)
		}
	case "state":
		cfg, err = s.locked(nil, false)
	case "prepareVpn":
		cfg, err = s.locked(func(c *Config) error {
			p, e := PrepareVPN(*c)
			if e != nil {
				return e
			}
			node, _ := c.selectedNode()
			if e := validateNode(node); e != nil {
				return fmt.Errorf("当前节点不可用: %w", e)
			}
			vpn = &p
			return nil
		}, false)
	case "import":
		cfg, err = s.locked(func(c *Config) error {
			name := strings.TrimSpace(req.Name)
			if name == "" {
				name = "导入的节点"
			}
			if len([]rune(name)) > 100 {
				return fmt.Errorf("订阅名称过长")
			}
			if req.URL != "" {
				u, e := url.Parse(req.URL)
				if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
					return fmt.Errorf("订阅地址必须是有效的 HTTPS 地址")
				}
			}
			id := req.SourceID
			if id == "" {
				id = model.NewID()
			} else {
				found := false
				for _, src := range c.Sources {
					if src.ID == id {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("订阅不存在")
				}
			}
			parsed, e := subscription.Parse([]byte(req.Content), id)
			if e != nil {
				return e
			}
			if len(parsed.Nodes) > 2000 {
				return fmt.Errorf("单次最多导入 2000 个节点")
			}
			good := []model.Node{}
			warnings = append([]string{}, parsed.Warnings...)
			for _, n := range parsed.Nodes {
				if e := validateNode(n); e != nil {
					warnings = append(warnings, n.Name+": 节点参数不受当前内核支持")
					continue
				}
				good = append(good, n)
			}
			if len(good) == 0 {
				return fmt.Errorf("没有可用节点，原有订阅保持不变")
			}
			kept := []model.Node{}
			for _, n := range c.Nodes {
				if n.SourceID != id {
					kept = append(kept, n)
				}
			}
			if len(kept)+len(good) > 4000 {
				return fmt.Errorf("手机端最多保存 4000 个节点")
			}
			c.Nodes = append(kept, good...)
			src := model.Source{ID: id, Name: name, URL: req.URL, UpdatedAt: time.Now(), Warnings: warnings}
			updated := false
			for i := range c.Sources {
				if c.Sources[i].ID == id {
					c.Sources[i] = src
					updated = true
				}
			}
			if !updated {
				c.Sources = append(c.Sources, src)
			}
			if _, e := c.selectedNode(); e != nil {
				c.SelectedNodeID = good[0].ID
			}
			imported = len(good)
			return nil
		}, true)
	case "selectNode":
		cfg, err = s.locked(func(c *Config) error { c.SelectedNodeID = req.NodeID; _, e := c.selectedNode(); return e }, true)
	case "saveApplication":
		cfg, err = s.locked(func(c *Config) error {
			a, e := normalizeApplication(req.Application, c.SelfBundle)
			if e != nil {
				return e
			}
			for i := range c.Applications {
				if c.Applications[i].BundleName == a.BundleName {
					c.Applications[i] = a
					return nil
				}
			}
			c.Applications = append(c.Applications, a)
			return nil
		}, true)
	case "removeApplication":
		cfg, err = s.locked(func(c *Config) error {
			next := []Application{}
			for _, a := range c.Applications {
				if a.BundleName != req.Application.BundleName {
					next = append(next, a)
				}
			}
			c.Applications = next
			return nil
		}, true)
	case "saveSettings":
		cfg, err = s.locked(func(c *Config) error { c.Settings = req.Settings; return nil }, true)
	case "removeSource":
		cfg, err = s.locked(func(c *Config) error {
			sources := []model.Source{}
			nodes := []model.Node{}
			for _, src := range c.Sources {
				if src.ID != req.SourceID {
					sources = append(sources, src)
				}
			}
			for _, n := range c.Nodes {
				if n.SourceID != req.SourceID {
					nodes = append(nodes, n)
				}
			}
			c.Sources, c.Nodes = sources, nodes
			if _, e := c.selectedNode(); e != nil {
				c.SelectedNodeID = ""
			}
			return nil
		}, true)
	case "sourceURL":
		cfg, err = s.locked(func(c *Config) error {
			for _, src := range c.Sources {
				if src.ID == req.SourceID {
					sourceURL = src.URL
					return nil
				}
			}
			return fmt.Errorf("订阅不存在")
		}, false)
	case "requestStop":
		status := readRuntime(s.dir)
		err = atomicJSON(filepath.Join(s.dir, "stop.json"), stopRequest{SessionID: status.SessionID, CreatedAt: time.Now().UnixMilli()})
		if err == nil {
			cfg, err = s.locked(nil, false)
		}
	case "recordFailure":
		cfg, err = s.locked(func(c *Config) error {
			lock := flock.New(filepath.Join(s.dir, "vpn.lock"))
			defer lock.Close()
			free, e := lock.TryLock()
			if e != nil {
				return e
			}
			if !free {
				return fmt.Errorf("代理正在运行，不能覆盖其状态")
			}
			state := emptyRuntime()
			state.Phase, state.Error, state.UpdatedAt = "error", req.Error, time.Now().UnixMilli()
			runes := []rune(state.Error)
			if len(runes) > 1000 {
				state.Error = string(runes[:1000])
			}
			return atomicJSON(filepath.Join(s.dir, "runtime.json"), state)
		}, false)
	default:
		return failure(fmt.Errorf("不支持的操作"))
	}
	if err != nil {
		return failure(err)
	}
	state := publicState(cfg, readRuntime(s.dir))
	return Reply{OK: true, State: &state, VPN: vpn, Imported: imported, Warnings: warnings, SourceURL: sourceURL}
}
