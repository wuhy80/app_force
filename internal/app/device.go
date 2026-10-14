package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"appforce/internal/model"
)

func (a *App) SaveDevice(device model.Device) error {
	u, err := url.Parse(device.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("设备地址应为 https://主机:端口")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("远程设备必须使用 HTTPS；仅回环地址允许 HTTP 隧道")
		}
	}
	if strings.TrimSpace(device.Name) == "" || len(device.Name) > 200 {
		return fmt.Errorf("请填写设备名称")
	}
	device.URL = strings.TrimSuffix(device.URL, "/")
	if device.CertificateSHA256 != "" {
		device.CertificateSHA256 = strings.ToLower(strings.ReplaceAll(device.CertificateSHA256, ":", ""))
		pin, e := hex.DecodeString(device.CertificateSHA256)
		if e != nil || len(pin) != sha256.Size {
			return fmt.Errorf("证书指纹必须为 SHA-256")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	if device.ID == "" {
		device.ID = model.NewID()
	}
	for i, existing := range next.Devices {
		if existing.ID == device.ID {
			if device.Token == "" {
				device.Token = existing.Token
			}
			if len(device.Token) != 64 {
				return fmt.Errorf("管理密钥应为 64 位字符")
			}
			next.Devices[i] = device
			if err := a.store.Save(next); err != nil {
				return err
			}
			a.config = next
			return nil
		}
	}
	if len(device.Token) != 64 {
		return fmt.Errorf("管理密钥应为 64 位字符")
	}
	next.Devices = append(next.Devices, device)
	if err := a.store.Save(next); err != nil {
		return err
	}
	a.config = next
	return nil
}

func (a *App) DeleteDevice(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.config.Clone()
	for i, d := range next.Devices {
		if d.ID == id {
			next.Devices = append(next.Devices[:i], next.Devices[i+1:]...)
			if err := a.store.Save(next); err != nil {
				return err
			}
			a.config = next
			return nil
		}
	}
	return fmt.Errorf("设备不存在")
}

func (a *App) Remote(ctx context.Context, id, method, path string, body io.Reader) (int, []byte, error) {
	a.mu.Lock()
	var device model.Device
	for _, d := range a.config.Devices {
		if d.ID == id {
			device = d
			break
		}
	}
	a.mu.Unlock()
	if device.ID == "" {
		return 0, nil, fmt.Errorf("设备不存在")
	}
	// A remote hop can only access operational routes, never pairing data or another remote hop.
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || strings.Contains(u.Path, "..") {
		return 0, nil, fmt.Errorf("远程请求路径无效")
	}
	allowed := false
	for _, prefix := range []string{"/api/state", "/api/programs", "/api/processes", "/api/engine/", "/api/nodes/", "/api/rules", "/api/sources", "/api/settings", "/api/logs", "/api/traffic"} {
		if u.Path == prefix || strings.HasPrefix(u.Path, strings.TrimSuffix(prefix, "/")+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return 0, nil, fmt.Errorf("远程接口不可访问")
	}
	req, err := http.NewRequestWithContext(ctx, method, device.URL+path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("设备地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+device.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if device.CertificateSHA256 != "" {
		pin, _ := hex.DecodeString(device.CertificateSHA256)
		transport.TLSClientConfig.InsecureSkipVerify = true // Explicit certificate pin replaces CA validation.
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("服务器没有证书")
			}
			cert := state.PeerCertificates[0]
			hash := sha256.Sum256(cert.Raw)
			if subtle.ConstantTimeCompare(hash[:], pin) != 1 {
				return fmt.Errorf("设备证书指纹不匹配")
			}
			if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("设备证书已过期或尚未生效")
			}
			return nil
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("连接设备失败，请检查网络、证书和服务状态")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, data, err
}
