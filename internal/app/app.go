package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"appforce/internal/deploy"
	"appforce/internal/engine"
	"appforce/internal/model"
	"appforce/internal/store"
	"appforce/internal/subscription"
	"appforce/internal/traffic"
)

type App struct {
	mu       sync.Mutex
	config   model.Config
	store    *store.Store
	Engine   engine.Engine
	Token    string
	Deployer *deploy.Manager
}

type Snapshot struct {
	Version        string           `json:"version"`
	Hostname       string           `json:"hostname"`
	OS             string           `json:"os"`
	Nodes          []model.NodeView `json:"nodes"`
	Sources        []model.Source   `json:"sources"`
	Rules          []model.RuleView `json:"rules"`
	Devices        []model.Device   `json:"devices"`
	SelectedNodeID string           `json:"selected_node_id"`
	Settings       model.Settings   `json:"settings"`
	Engine         engine.Status    `json:"engine"`
}

func Open(dir string) (*App, error) {
	s, c, token, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	recorder, err := traffic.Open(filepath.Join(dir, "traffic.db"))
	if err != nil {
		return nil, err
	}
	a := &App{store: s, config: c, Token: token, Engine: engine.Engine{CachePath: filepath.Join(dir, "engine-cache.db"), Recorder: recorder}}
	a.Deployer = deploy.NewManager(a.saveDeployedDevice)
	return a, nil
}

func (a *App) Traffic(days int, appID string, includeDirect bool) (traffic.Report, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, rule := range a.config.Rules {
		a.Engine.Recorder.Register(rule.Name, rule.Path)
	}
	return a.Engine.Recorder.Report(days, appID, includeDirect)
}

func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	nodes := make([]model.NodeView, 0, len(a.config.Nodes))
	for _, node := range a.config.Nodes {
		nodes = append(nodes, node.View())
	}
	rules := make([]model.RuleView, 0, len(a.config.Rules))
	for _, rule := range a.config.Rules {
		rules = append(rules, rule.View())
	}
	devices := append([]model.Device{}, a.config.Devices...)
	for i := range devices {
		devices[i].Token = ""
	}
	// Subscription URLs can contain credentials; expose only the source label and refresh capability.
	sources := append([]model.Source{}, a.config.Sources...)
	for i := range sources {
		if sources[i].URL != "" {
			sources[i].URL = "remote"
		}
	}
	hostname, _ := os.Hostname()
	return Snapshot{model.Version, hostname, runtime.GOOS, nodes, sources, rules, devices, a.config.SelectedNodeID, a.config.Settings, a.Engine.Snapshot()}
}

// applyLocked applies a running configuration transactionally, restoring the prior engine on failure.
func (a *App) applyLocked(next model.Config) error {
	running := a.Engine.Snapshot().Running
	if running {
		if _, err := engine.Build(next); err != nil {
			return err
		}
		if err := a.Engine.Stop(); err != nil {
			return fmt.Errorf("停止旧配置失败: %w", err)
		}
		if err := a.Engine.Start(next); err != nil {
			restoreErr := a.Engine.Start(a.config)
			if restoreErr != nil {
				return fmt.Errorf("新配置失败: %v；旧配置恢复失败: %v", err, restoreErr)
			}
			return fmt.Errorf("新配置失败，已恢复原配置: %w", err)
		}
	}
	if err := a.store.Save(next); err != nil {
		if running {
			_ = a.Engine.Stop()
			if restoreErr := a.Engine.Start(a.config); restoreErr != nil {
				return fmt.Errorf("保存失败: %v；恢复失败: %v", err, restoreErr)
			}
		}
		return err
	}
	a.config = next
	return nil
}

func (a *App) Start() error { a.mu.Lock(); defer a.mu.Unlock(); return a.Engine.Start(a.config) }
func (a *App) Stop() error  { a.mu.Lock(); defer a.mu.Unlock(); return a.Engine.Stop() }

func (a *App) SelectNode(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	next.SelectedNodeID = id
	if _, ok := next.SelectedNode(); !ok {
		return fmt.Errorf("节点不存在")
	}
	return a.applyLocked(next)
}

func (a *App) SetSettings(settings model.Settings) error {
	settings.TUNStack = settings.EffectiveTUNStack()
	if err := settings.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	next.Settings = settings
	return a.applyLocked(next)
}

func (a *App) SaveRule(rule model.Rule) error {
	rule, err := model.NormalizeRule(rule)
	if err != nil {
		return err
	}
	if path, e := os.Executable(); e == nil && rule.MatchesPath(path) {
		return fmt.Errorf("代理服务本身不能加入代理规则")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	if rule.NodeID != "" && rule.Enabled {
		if _, ok := next.Node(rule.NodeID); !ok {
			return fmt.Errorf("程序指定的节点不存在，请重新选择节点或跟随默认节点")
		}
	}
	for i, existing := range next.Rules {
		if existing.ID == rule.ID || model.SamePath(existing.Path, rule.Path) {
			rule.ID = existing.ID
			next.Rules[i] = rule
			return a.applyLocked(next)
		}
	}
	next.Rules = append(next.Rules, rule)
	return a.applyLocked(next)
}

func (a *App) DeleteRule(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	for i, rule := range next.Rules {
		if rule.ID == id {
			next.Rules = append(next.Rules[:i], next.Rules[i+1:]...)
			return a.applyLocked(next)
		}
	}
	return fmt.Errorf("规则不存在")
}

type ImportRequest struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func fetch(ctx context.Context, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("订阅地址必须是 HTTP/HTTPS URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("订阅地址格式错误")
	}
	req.Header.Set("User-Agent", "AppForce/"+model.Version)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("订阅下载失败，请检查地址及网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, subscription.MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取订阅失败")
	}
	if len(data) > subscription.MaxSize {
		return nil, fmt.Errorf("订阅超过 8 MiB")
	}
	return data, nil
}

func (a *App) Import(ctx context.Context, req ImportRequest, sourceID string) (int, []string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "导入订阅"
	}
	if len(name) > 200 {
		return 0, nil, fmt.Errorf("订阅名称过长")
	}
	data := []byte(req.Content)
	if req.URL != "" {
		if req.Content != "" {
			return 0, nil, fmt.Errorf("订阅地址和文本内容请选择一种")
		}
		var err error
		data, err = fetch(ctx, req.URL)
		if err != nil {
			return 0, nil, err
		}
	}
	if sourceID == "" {
		sourceID = model.NewID()
	}
	parsed, err := subscription.Parse(data, sourceID)
	if err != nil {
		return 0, parsed.Warnings, err
	}
	if len(parsed.Nodes) > 2000 {
		return 0, nil, fmt.Errorf("单个订阅最多支持 2000 个节点")
	}
	valid := make([]model.Node, 0, len(parsed.Nodes))
	for _, node := range parsed.Nodes {
		if ctx.Err() != nil {
			return 0, parsed.Warnings, ctx.Err()
		}
		if err := engine.ValidateNode(node); err != nil {
			parsed.Warnings = append(parsed.Warnings, node.Name+": 内核校验失败: "+err.Error())
			continue
		}
		valid = append(valid, node)
	}
	if len(valid) == 0 {
		return 0, parsed.Warnings, fmt.Errorf("所有节点均未通过内核校验")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	next.Nodes = nil
	for _, n := range a.config.Nodes {
		if n.SourceID != sourceID {
			next.Nodes = append(next.Nodes, n)
		}
	}
	next.Nodes = append(next.Nodes, valid...)
	source := model.Source{ID: sourceID, Name: name, URL: req.URL, UpdatedAt: time.Now(), Warnings: parsed.Warnings}
	found := false
	for i, s := range next.Sources {
		if s.ID == sourceID {
			next.Sources[i] = source
			found = true
			break
		}
	}
	if !found {
		next.Sources = append(next.Sources, source)
	}
	if _, ok := next.SelectedNode(); !ok {
		next.SelectedNodeID = valid[0].ID
	}
	if err := a.applyLocked(next); err != nil {
		return 0, parsed.Warnings, err
	}
	return len(valid), parsed.Warnings, nil
}

func (a *App) Refresh(ctx context.Context, id string) (int, []string, error) {
	a.mu.Lock()
	var source model.Source
	for _, s := range a.config.Sources {
		if s.ID == id {
			source = s
			break
		}
	}
	a.mu.Unlock()
	if source.URL == "" {
		return 0, nil, fmt.Errorf("该订阅没有可更新的地址")
	}
	return a.Import(ctx, ImportRequest{Name: source.Name, URL: source.URL}, id)
}

func (a *App) DeleteSource(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	next.Nodes = []model.Node{}
	next.Sources = []model.Source{}
	found := false
	for _, s := range a.config.Sources {
		if s.ID != id {
			next.Sources = append(next.Sources, s)
		} else {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("订阅不存在")
	}
	for _, n := range a.config.Nodes {
		if n.SourceID != id {
			next.Nodes = append(next.Nodes, n)
		}
	}
	if _, ok := next.SelectedNode(); !ok {
		next.SelectedNodeID = ""
		if len(next.Nodes) > 0 {
			next.SelectedNodeID = next.Nodes[0].ID
		}
	}
	if a.Engine.Snapshot().Running && len(next.Nodes) == 0 {
		return fmt.Errorf("请先停止代理，再删除最后一个订阅")
	}
	return a.applyLocked(next)
}

func (a *App) Probe(ctx context.Context, id string) (uint16, error) {
	a.mu.Lock()
	var node model.Node
	for _, n := range a.config.Nodes {
		if n.ID == id {
			node = n
			break
		}
	}
	a.mu.Unlock()
	if node.ID == "" {
		return 0, fmt.Errorf("节点不存在")
	}
	return engine.Probe(ctx, node)
}
