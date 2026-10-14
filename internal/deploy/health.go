package deploy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"appforce/internal/model"
)

func managementURL(host string, port int) string {
	return "https://" + net.JoinHostPort(host, fmt.Sprint(port))
}

func VerifyAgent(ctx context.Context, address, token, fingerprint string) error {
	return waitReady(ctx, model.Device{URL: address, Token: token, CertificateSHA256: fingerprint})
}

func waitReady(ctx context.Context, device model.Device) error {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	pin, _ := hex.DecodeString(device.CertificateSHA256)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return fmt.Errorf("证书缺失")
		}
		cert := state.PeerCertificates[0]
		hash := sha256.Sum256(cert.Raw)
		if subtle.ConstantTimeCompare(hash[:], pin) != 1 || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return fmt.Errorf("Agent 证书验证失败")
		}
		return nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", device.URL+"/api/state", nil)
		req.Header.Set("Authorization", "Bearer "+device.Token)
		resp, err := client.Do(req)
		if err == nil {
			var state struct {
				Version string `json:"version"`
				OS      string `json:"os"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&state)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 && err == nil && state.Version == model.Version && (state.OS == "linux" || state.OS == "darwin") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Agent 已安装，但无法连接管理地址 %s；请检查服务是否启动，以及防火墙是否允许管理端口，再重新部署重试", device.URL)
		case <-time.After(time.Second):
		}
	}
}
