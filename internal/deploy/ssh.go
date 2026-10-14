package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Request struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	SudoPassword    string `json:"sudo_password,omitempty"`
	ManagementPort  int    `json:"management_port"`
	HostFingerprint string `json:"host_fingerprint,omitempty"`
}

func (r *Request) Validate() error {
	r.Name, r.Host, r.Username = strings.TrimSpace(r.Name), strings.TrimSpace(r.Host), strings.TrimSpace(r.Username)
	r.Host = strings.TrimPrefix(strings.TrimSuffix(r.Host, "]"), "[")
	if r.Port == 0 {
		r.Port = 22
	}
	if r.ManagementPort == 0 {
		r.ManagementPort = 17891
	}
	if r.Name == "" || len(r.Name) > 200 {
		return fmt.Errorf("请填写设备名称（最多 200 字节）")
	}
	if r.Host == "" || len(r.Host) > 253 || strings.ContainsAny(r.Host, "/\\ \t\r\n\x00@?#") {
		return fmt.Errorf("请填写设备 IP 或主机名，不包含协议或端口")
	}
	if strings.Contains(r.Host, ":") && net.ParseIP(r.Host) == nil {
		return fmt.Errorf("SSH 端口请填写在端口栏")
	}
	if r.Port < 1 || r.Port > 65535 || r.ManagementPort < 1024 || r.ManagementPort > 65535 {
		return fmt.Errorf("SSH 端口应在 1 到 65535 之间，管理端口应在 1024 到 65535 之间")
	}
	if r.Username == "" || len(r.Username) > 200 || strings.ContainsAny(r.Username, "\r\n\x00") {
		return fmt.Errorf("请填写 SSH 用户名")
	}
	if r.Password == "" || len(r.Password) > 4096 || len(r.SudoPassword) > 4096 || strings.ContainsAny(r.Password+r.SudoPassword, "\r\n\x00") {
		return fmt.Errorf("请填写密码；密码不能包含换行符")
	}
	return nil
}

type connection struct {
	client      *ssh.Client
	raw         net.Conn
	fingerprint string
}

func connect(ctx context.Context, r Request, requirePin bool) (*connection, error) {
	if requirePin && !strings.HasPrefix(r.HostFingerprint, "SHA256:") {
		return nil, fmt.Errorf("请先连接并检查设备")
	}
	address := net.JoinHostPort(r.Host, fmt.Sprint(r.Port))
	raw, err := (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("无法连接 SSH，请检查地址、端口和远端是否已开启 SSH")
	}
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(time.Now().Add(20*time.Second)) {
		_ = raw.SetDeadline(deadline)
	}
	fingerprint := ""
	hostChanged := false
	config := &ssh.ClientConfig{User: r.Username, Auth: []ssh.AuthMethod{
		ssh.Password(r.Password), ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			if len(questions) != 1 || !strings.Contains(strings.ToLower(questions[0]), "password") {
				return nil, fmt.Errorf("当前只支持密码登录")
			}
			return []string{r.Password}, nil
		}),
	}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		if r.HostFingerprint != "" && r.HostFingerprint != fingerprint {
			hostChanged = true
			return fmt.Errorf("SSH 主机指纹已变化")
		}
		return nil
	}}
	sshConn, channels, requests, err := ssh.NewClientConn(raw, address, config)
	if err != nil {
		_ = raw.Close()
		if hostChanged {
			return nil, fmt.Errorf("SSH 主机指纹与检查结果不一致，请重新检查设备")
		}
		return nil, fmt.Errorf("SSH 登录失败，请检查用户名、密码及服务器是否允许密码登录")
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, channels, requests)
	return &connection{client: client, raw: raw, fingerprint: fingerprint}, nil
}

func (c *connection) Close() { _ = c.client.Close() }

type limitedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := 64*1024 - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(len(p), remaining)])
	}
	return n, nil
}

func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func (c *connection) run(ctx context.Context, command string, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("部署超时或已取消")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.raw.SetDeadline(deadline)
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = c.raw.Close() })
	defer stopCancellation()
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("SSH 会话建立失败")
	}
	defer session.Close()
	var output, diagnostics limitedBuffer
	session.Stdout, session.Stderr, session.Stdin = &output, &diagnostics, input
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-ctx.Done():
		_ = c.raw.SetDeadline(time.Now())
		_ = session.Close()
		<-done
		return "", fmt.Errorf("部署超时或已取消")
	case err := <-done:
		if err != nil {
			return output.String() + "\n" + diagnostics.String(), err
		}
		return output.String(), err
	}
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (c *connection) privileged(ctx context.Context, r Request, root bool, script string) (string, error) {
	if root {
		return c.run(ctx, "/bin/sh -c "+quote(script), nil)
	}
	password := r.SudoPassword
	if password == "" {
		password = r.Password
	}
	return c.run(ctx, "sudo -S -p '' /bin/sh -c "+quote(script), strings.NewReader(password+"\n"))
}

func safeError(stage, output string, err error, r Request) error {
	for _, secret := range []string{r.Password, r.SudoPassword} {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "[已隐藏]")
		}
	}
	output = strings.TrimSpace(output)
	if len(output) > 2000 {
		output = output[len(output)-2000:]
	}
	if output == "" {
		output = "请检查远端权限、服务状态及网络连接"
	}
	return fmt.Errorf("%s失败：%s (%v)", stage, output, err)
}
