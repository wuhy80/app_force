package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"appforce/internal/antigravity"
)

type AccountTarget struct {
	DeviceID string `json:"device_id"`
	antigravity.Target
}

func (a *App) accountClient() *http.Client {
	a.mu.Lock()
	running := a.Engine.Snapshot().Running
	port := a.config.Settings.MixedPort
	a.mu.Unlock()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if running {
		address, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
		transport.Proxy = http.ProxyURL(address)
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}
}

func (a *App) SwitchAccount(ctx context.Context, id string, target AccountTarget) (antigravity.TargetStatus, error) {
	transfer, err := a.Accounts.Transfer(ctx, id, target.Target)
	if err != nil {
		return antigravity.TargetStatus{}, err
	}
	if target.DeviceID == "" {
		return a.Accounts.Apply(ctx, transfer)
	}
	data, _ := json.Marshal(transfer)
	status, body, err := a.Remote(ctx, target.DeviceID, "POST", "/api/antigravity/native/apply", bytes.NewReader(data))
	if err != nil {
		return antigravity.TargetStatus{}, err
	}
	if status != http.StatusOK {
		return antigravity.TargetStatus{}, accountRemoteError(body)
	}
	var result antigravity.TargetStatus
	if json.Unmarshal(body, &result) != nil {
		return result, fmt.Errorf("远端 Agent 返回了无效的切号结果")
	}
	return result, nil
}

func (a *App) CaptureAccount(ctx context.Context, target AccountTarget) (antigravity.AccountView, error) {
	if target.DeviceID == "" {
		credential, err := a.Accounts.Capture(ctx, target.Target)
		if err != nil {
			return antigravity.AccountView{}, err
		}
		account, err := a.Accounts.ImportNativeCredential(ctx, "", credential)
		if err != nil {
			return account, err
		}
		if err = a.Accounts.ObserveCapture(target.Target, account.ID); err != nil {
			return account, err
		}
		return account, nil
	}
	data, _ := json.Marshal(target.Target)
	status, body, err := a.Remote(ctx, target.DeviceID, "POST", "/api/antigravity/native/capture", bytes.NewReader(data))
	if err != nil {
		return antigravity.AccountView{}, err
	}
	if status != http.StatusOK {
		return antigravity.AccountView{}, accountRemoteError(body)
	}
	var credential antigravity.Credential
	if json.Unmarshal(body, &credential) != nil {
		return antigravity.AccountView{}, fmt.Errorf("远端 Agent 返回了无效的账号凭据")
	}
	return a.Accounts.ImportNativeCredential(ctx, "", credential)
}

func accountRemoteError(body []byte) error {
	var reply struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.Error == "" {
		return fmt.Errorf("远端账号操作失败，请升级 Agent 后重试")
	}
	if reply.Error == "接口不存在" {
		return fmt.Errorf("远端 Agent 不支持账号管理，请通过设备管理重新部署或更新 Agent")
	}
	return fmt.Errorf("远端：%s", reply.Error)
}
