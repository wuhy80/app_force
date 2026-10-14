package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"appforce/internal/agentbundle"
	"appforce/internal/model"
)

type Check struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Hostname        string `json:"hostname"`
	Root            bool   `json:"root"`
	HostFingerprint string `json:"host_fingerprint"`
	Bundled         bool   `json:"bundled"`
}

func inspect(ctx context.Context, c *connection) (Check, error) {
	output, err := c.run(ctx, "uname -s; uname -m; hostname; id -u", nil)
	if err != nil {
		return Check{}, fmt.Errorf("无法读取设备信息")
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		return Check{}, fmt.Errorf("远端 shell 输出异常，无法识别设备")
	}
	check := Check{Hostname: strings.TrimSpace(lines[2]), Root: strings.TrimSpace(lines[3]) == "0", HostFingerprint: c.fingerprint}
	switch strings.TrimSpace(lines[0]) {
	case "Linux":
		check.OS = "linux"
	case "Darwin":
		check.OS = "darwin"
	default:
		return Check{}, fmt.Errorf("只支持部署 Linux 和 macOS Agent")
	}
	switch strings.TrimSpace(lines[1]) {
	case "x86_64", "amd64":
		check.Arch = "amd64"
	case "aarch64", "arm64":
		check.Arch = "arm64"
	default:
		return Check{}, fmt.Errorf("不支持的 CPU 架构：%s", strings.TrimSpace(lines[1]))
	}
	for _, platform := range agentbundle.Platforms() {
		if platform.OS == check.OS && platform.Arch == check.Arch {
			check.Bundled = true
		}
	}
	return check, nil
}

func Inspect(ctx context.Context, r Request) (Check, error) {
	if err := r.Validate(); err != nil {
		return Check{}, err
	}
	c, err := connect(ctx, r, false)
	if err != nil {
		return Check{}, err
	}
	defer c.Close()
	return inspect(ctx, c)
}

type Job struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Status    string    `json:"status"` // running, succeeded, failed
	Stage     string    `json:"stage"`
	Progress  int       `json:"progress"`
	Error     string    `json:"error"`
	DeviceID  string    `json:"device_id"`
	StartedAt time.Time `json:"started_at"`
}

type Manager struct {
	mu         sync.Mutex
	jobs       map[string]Job
	active     map[string]bool
	save       func(model.Device) (string, error)
	openBundle func(string, string) (io.ReadCloser, agentbundle.Platform, error)
	ready      func(context.Context, model.Device) error
	cancels    map[string]context.CancelFunc
	closing    bool
	wg         sync.WaitGroup
}

func NewManager(save func(model.Device) (string, error)) *Manager {
	return &Manager{jobs: make(map[string]Job), active: make(map[string]bool), save: save, openBundle: agentbundle.Open, ready: waitReady, cancels: make(map[string]context.CancelFunc)}
}

func (m *Manager) Start(r Request) (Job, error) {
	if err := r.Validate(); err != nil {
		return Job{}, err
	}
	if !strings.HasPrefix(r.HostFingerprint, "SHA256:") {
		return Job{}, fmt.Errorf("请先连接并检查设备")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Job{}, fmt.Errorf("管理程序正在退出")
	}
	key := r.Host // Don't deploy two jobs to the same host through different SSH ports.
	if m.active[key] {
		return Job{}, fmt.Errorf("该设备正在部署中")
	}
	if len(m.active) >= 4 {
		return Job{}, fmt.Errorf("最多同时部署 4 台设备")
	}
	if len(m.jobs) >= 32 {
		for id, job := range m.jobs {
			if job.Status != "running" {
				delete(m.jobs, id)
				break
			}
		}
	}
	job := Job{ID: model.NewID(), Name: r.Name, Host: r.Host, Status: "running", Stage: "连接 SSH", Progress: 5, StartedAt: time.Now()}
	m.jobs[job.ID], m.active[key] = job, true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.cancels[job.ID] = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		deviceID, err := m.install(ctx, job.ID, r)
		m.mu.Lock()
		defer m.mu.Unlock()
		finished := m.jobs[job.ID]
		if err != nil {
			finished.Status, finished.Error = "failed", err.Error()
		} else {
			finished.Status, finished.Stage, finished.Progress, finished.DeviceID = "succeeded", "部署完成，Agent 已启动", 100, deviceID
		}
		m.jobs[job.ID] = finished
		delete(m.active, key)
		delete(m.cancels, job.ID)
	}()
	return job, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true
	for _, cancel := range m.cancels {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) Job(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("部署任务不存在")
	}
	return job, nil
}

func (m *Manager) update(id, stage string, progress int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	job.Stage, job.Progress = stage, progress
	m.jobs[id] = job
}

func (m *Manager) install(ctx context.Context, id string, r Request) (string, error) {
	c, err := connect(ctx, r, true)
	if err != nil {
		return "", err
	}
	defer c.Close()
	check, err := inspect(ctx, c)
	if err != nil {
		return "", err
	}
	m.update(id, "检查系统和管理员权限", 12)
	reader, platform, err := m.openBundle(check.OS, check.Arch)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	output, err := c.privileged(ctx, r, check.Root, preflight(check.OS, r.ManagementPort))
	if err != nil {
		return "", safeError("权限或系统检查", output, err, r)
	}
	output, err = c.run(ctx, "umask 077; mktemp -d /tmp/appforce-deploy.XXXXXXXX", nil)
	if err != nil {
		return "", fmt.Errorf("创建远端上传目录失败")
	}
	temp := strings.TrimSpace(output)
	if !strings.HasPrefix(temp, "/tmp/appforce-deploy.") || strings.ContainsAny(temp, "\r\n\x00") {
		return "", fmt.Errorf("远端临时目录无效")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.run(cleanupCtx, "rm -rf -- "+quote(temp), nil)
	}()
	m.update(id, "上传 "+check.OS+" / "+check.Arch+" Agent", 25)
	hash := sha256.New()
	upload := io.TeeReader(io.LimitReader(reader, platform.Size+1), hash)
	output, err = c.run(ctx, "umask 077; cat > "+quote(temp+"/appforce"), upload)
	if err != nil {
		return "", safeError("上传 Agent", output, err, r)
	}
	if hex.EncodeToString(hash.Sum(nil)) != platform.SHA256 {
		return "", fmt.Errorf("内置 Agent 校验失败，未安装")
	}
	m.update(id, "安装后台服务并启动 Agent", 60)
	output, err = c.privileged(ctx, r, check.Root, installation(check.OS, temp, platform.SHA256, r.ManagementPort))
	if err != nil {
		return "", safeError("安装后台服务", output, err, r)
	}
	m.update(id, "读取管理凭据并验证连接", 85)
	output, err = c.privileged(ctx, r, check.Root, credentials(check.OS))
	if err != nil {
		return "", fmt.Errorf("Agent 已安装，但读取管理凭据失败；请重新部署以重试")
	}
	var credentials struct {
		Token       string `json:"token"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &credentials); err != nil || len(credentials.Token) != 64 || len(credentials.Fingerprint) != 64 {
		return "", fmt.Errorf("Agent 已安装，但管理凭据格式异常")
	}
	device := model.Device{Name: r.Name, URL: managementURL(r.Host, r.ManagementPort), Token: credentials.Token, CertificateSHA256: credentials.Fingerprint}
	if err := m.ready(ctx, device); err != nil {
		return "", err
	}
	return m.save(device)
}
