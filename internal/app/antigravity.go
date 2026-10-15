package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"appforce/internal/antigravity"
	"appforce/internal/authbrowser"
	"appforce/internal/model"
)

type AccountTarget struct {
	DeviceID string `json:"device_id"`
	antigravity.Target
}

func (a *App) accountClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial, err := a.accountDialer()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if err != nil {
			return nil, &antigravity.NetworkError{Message: err.Error()}
		}
		return dial(ctx, network, address)
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}
}

func accountNode(c model.Config) (model.Node, error) {
	id := c.SelectedNodeID
	found := false
	for _, rule := range c.Rules {
		if !rule.Enabled || !model.IsAntigravityProgram(rule.Path) {
			continue
		}
		next := rule.NodeID
		if next == "" {
			next = c.SelectedNodeID
		}
		if found && next != id {
			return model.Node{}, fmt.Errorf("多个 Antigravity 程序规则使用了不同节点，请先将它们设置为同一代理节点")
		}
		id, found = next, true
	}
	node, ok := c.Node(id)
	if !ok {
		return model.Node{}, fmt.Errorf("Antigravity 的代理节点不存在，请先选择有效节点")
	}
	return node, nil
}

func (a *App) accountDialer() (func(context.Context, string, string) (net.Conn, error), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.Engine.Snapshot().Running {
		return nil, fmt.Errorf("代理未启动，请先启动 Antigravity 的代理后再添加授权、导入或刷新账号")
	}
	node, err := accountNode(a.config)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return a.Engine.DialNodeContext(ctx, node.ID, network, address)
	}, nil
}

func (a *App) StartAccountOAuth() (antigravity.OAuthSession, error) {
	if _, err := a.accountDialer(); err != nil {
		return antigravity.OAuthSession{}, err
	}
	return a.Accounts.StartOAuth()
}

func (a *App) OpenAccountOAuthBrowser(id string) error {
	dial, err := a.accountDialer()
	if err != nil {
		return err
	}
	return a.Accounts.OpenOAuthBrowser(id, func(address string) (io.Closer, error) {
		return authbrowser.Open(address, dial)
	})
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
