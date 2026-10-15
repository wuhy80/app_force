package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	mu               sync.Mutex
	vault            *vault
	state            vaultState
	native           NativeStore
	client           func() *http.Client
	usage            *usageStore
	oauthMu          sync.Mutex
	oauthSessions    map[string]*oauthPending
	scanMu           sync.Mutex
	collectorContext context.Context
	collectorCancel  context.CancelFunc
	collectorDone    chan struct{}
	closeOnce        sync.Once
}

func Open(dir string, client func() *http.Client) (*Manager, error) {
	v, state, err := openVault(dir)
	if err != nil {
		return nil, err
	}
	u, err := openUsage(dir)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = func() *http.Client { return &http.Client{Timeout: 25 * time.Second} }
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{vault: v, state: state, native: systemStore{}, client: client, usage: u, oauthSessions: map[string]*oauthPending{}, collectorContext: ctx, collectorCancel: cancel, collectorDone: make(chan struct{})}
	go m.collectLoop()
	return m, nil
}

func accountID(email string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(digest[:16])
}

func (m *Manager) List() []AccountView {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]AccountView, 0, len(m.state.Accounts))
	for _, a := range m.state.Accounts {
		items = append(items, AccountView{ID: a.ID, Email: a.Email, Name: a.Name, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
			Expiry: a.Credential.Token.Expiry, Refreshable: a.Credential.Token.RefreshToken != "" && a.OAuth.ClientID != "",
			Quota: a.Quota, QuotaError: a.QuotaError})
	}
	return items
}

func (m *Manager) Config() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"client_id": m.state.OAuth.ClientID, "has_client_secret": m.state.OAuth.ClientSecret != "", "cloud": m.state.OAuth.Cloud}
}

func (m *Manager) SetConfig(config OAuthConfig) error {
	config.ClientID = strings.TrimSpace(config.ClientID)
	if config.ClientID == "" {
		config = defaultOAuthConfig()
	}
	if len(config.ClientID) > 512 || len(config.ClientSecret) > 4096 || (config.ClientID != "" && !strings.HasSuffix(config.ClientID, ".apps.googleusercontent.com")) {
		return fmt.Errorf("请填写 Google OAuth 桌面应用的 client_id")
	}
	if config.Cloud == "" {
		config.Cloud = "daily"
	}
	if config.Cloud != "daily" && config.Cloud != "production" {
		return fmt.Errorf("云服务应为 daily 或 production")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(m.state)
	if config.ClientID == next.OAuth.ClientID && config.ClientSecret == "" {
		config.ClientSecret = next.OAuth.ClientSecret
	}
	next.OAuth = config
	// A configuration change only updates accounts that have no refresh client.
	for i := range next.Accounts {
		if next.Accounts[i].OAuth.ClientID == "" || next.Accounts[i].OAuth.ClientID == config.ClientID {
			next.Accounts[i].OAuth = config
		}
	}
	if err := m.vault.save(next); err != nil {
		return err
	}
	m.state = next
	return nil
}

func parseCredential(content string) (Credential, error) {
	var credential Credential
	if len(content) > 65536 {
		return credential, fmt.Errorf("账号凭据过大")
	}
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "go-keyring-base64:") {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(content, "go-keyring-base64:"))
		if err != nil {
			return credential, fmt.Errorf("账号凭据编码无效")
		}
		content = string(data)
	}
	if json.Unmarshal([]byte(content), &credential) != nil {
		return credential, fmt.Errorf("请导入有效的账号凭据 JSON")
	}
	// Accept the token object itself and conventional OAuth export fields.
	if credential.Token.AccessToken == "" {
		_ = json.Unmarshal([]byte(content), &credential.Token)
	}
	var export struct {
		Token struct {
			ExpiryTimestamp int64 `json:"expiry_timestamp"`
		} `json:"token"`
	}
	_ = json.Unmarshal([]byte(content), &export)
	if credential.Token.Expiry.IsZero() && export.Token.ExpiryTimestamp > 0 {
		credential.Token.Expiry = time.Unix(export.Token.ExpiryTimestamp, 0)
	}
	if credential.AuthMethod == "" {
		credential.AuthMethod = "consumer"
	}
	if credential.Token.TokenType == "" {
		credential.Token.TokenType = "Bearer"
	}
	if err := validateCredential(credential); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func validateCredential(c Credential) error {
	if c.AuthMethod != "consumer" {
		return fmt.Errorf("当前支持 Google 个人账号，暂不支持企业 WIF/GCP 凭据")
	}
	if c.Token.AccessToken == "" || len(c.Token.AccessToken) > 16384 || len(c.Token.RefreshToken) > 16384 || strings.ContainsAny(c.Token.AccessToken, "\r\n") || strings.ContainsAny(c.Token.RefreshToken, "\r\n") {
		return fmt.Errorf("账号凭据缺少有效 access_token")
	}
	if c.Token.TokenType != "" && !strings.EqualFold(c.Token.TokenType, "Bearer") {
		return fmt.Errorf("账号凭据必须使用 Bearer token")
	}
	return nil
}

func (m *Manager) Import(ctx context.Context, request ImportRequest) (AccountView, error) {
	c, err := parseCredential(request.Credential)
	if err != nil {
		return AccountView{}, err
	}
	return m.ImportCredential(ctx, request.Name, c)
}

func (m *Manager) ImportCredential(ctx context.Context, name string, c Credential) (AccountView, error) {
	return m.importCredential(ctx, name, c, nil)
}

func (m *Manager) ImportNativeCredential(ctx context.Context, name string, c Credential) (AccountView, error) {
	config := defaultOAuthConfig()
	m.mu.Lock()
	config.Cloud = m.state.OAuth.Cloud
	m.mu.Unlock()
	return m.importCredential(ctx, name, c, &config)
}

func (m *Manager) importCredential(ctx context.Context, name string, c Credential, config *OAuthConfig) (AccountView, error) {
	if err := validateCredential(c); err != nil {
		return AccountView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(strings.TrimSpace(name)) > 200 {
		return AccountView{}, fmt.Errorf("账号名称过长")
	}
	a := Account{Credential: c, OAuth: m.state.OAuth}
	if config != nil {
		a.OAuth = *config
	}
	if err := m.fresh(ctx, &a); err != nil {
		return AccountView{}, err
	}
	email, err := identify(ctx, m.client(), a.Credential.Token.AccessToken)
	if err != nil {
		return AccountView{}, err
	}
	a.ID = accountID(email)
	a.Email = email
	a.Name = strings.TrimSpace(name)
	if a.Name == "" {
		a.Name = email
	}
	now := time.Now()
	a.CreatedAt = now
	a.UpdatedAt = now
	next := cloneState(m.state)
	found := false
	for i, previous := range next.Accounts {
		if previous.ID == a.ID {
			a.CreatedAt = previous.CreatedAt
			a.Quota = previous.Quota
			next.Accounts[i] = a
			found = true
			break
		}
	}
	if !found {
		if len(next.Accounts) >= 200 {
			return AccountView{}, fmt.Errorf("最多保存 200 个账号")
		}
		next.Accounts = append(next.Accounts, a)
	}
	if err = m.vault.save(next); err != nil {
		return AccountView{}, err
	}
	m.state = next
	return AccountView{ID: a.ID, Email: a.Email, Name: a.Name, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Expiry: a.Credential.Token.Expiry, Refreshable: a.OAuth.ClientID != "" && a.Credential.Token.RefreshToken != "", Quota: a.Quota}, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(m.state)
	for i, a := range next.Accounts {
		if a.ID == id {
			next.Accounts = append(next.Accounts[:i], next.Accounts[i+1:]...)
			if err := m.vault.save(next); err != nil {
				return err
			}
			m.state = next
			return nil
		}
	}
	return fmt.Errorf("账号不存在")
}

func (m *Manager) RefreshQuota(ctx context.Context, id string) (*Quota, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(m.state)
	for i := range next.Accounts {
		a := &next.Accounts[i]
		if a.ID != id {
			continue
		}
		err := m.fresh(ctx, a)
		var quota *Quota
		if err == nil {
			quota, err = fetchQuota(ctx, m.client(), *a)
		}
		if err != nil {
			a.QuotaError = err.Error()
		} else {
			a.Quota = quota
			a.QuotaError = ""
		}
		a.UpdatedAt = time.Now()
		if saveErr := m.vault.save(next); saveErr != nil {
			return nil, saveErr
		}
		m.state = next
		return quota, err
	}
	return nil, fmt.Errorf("账号不存在")
}

func (m *Manager) Transfer(ctx context.Context, id string, target Target) (Transfer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := cloneState(m.state)
	for i := range next.Accounts {
		a := &next.Accounts[i]
		if a.ID != id {
			continue
		}
		if err := m.fresh(ctx, a); err != nil {
			return Transfer{}, err
		}
		if err := m.vault.save(next); err != nil {
			return Transfer{}, err
		}
		m.state = next
		return Transfer{a.Email, a.Name, a.Credential, a.OAuth, target}, nil
	}
	return Transfer{}, fmt.Errorf("账号不存在")
}

func (m *Manager) Capture(ctx context.Context, target Target) (Credential, error) {
	return m.native.Read(ctx, target)
}

func (m *Manager) Apply(ctx context.Context, transfer Transfer) (TargetStatus, error) {
	if err := validateCredential(transfer.Credential); err != nil {
		return TargetStatus{}, err
	}
	if len(transfer.Email) > 320 || len(transfer.Name) > 200 {
		return TargetStatus{}, fmt.Errorf("账号信息过长")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	email, err := identify(ctx, m.client(), transfer.Credential.Token.AccessToken)
	if err != nil {
		return TargetStatus{}, err
	}
	if !strings.EqualFold(email, transfer.Email) {
		return TargetStatus{}, fmt.Errorf("账号身份与凭据不匹配")
	}
	transfer.Credential.Email = email
	resolved, err := resolveTarget(transfer.Target)
	if err != nil {
		return TargetStatus{}, err
	}
	transfer.Target = resolved.target
	previous, readErr := m.native.Read(ctx, transfer.Target)
	if readErr != nil && !errors.Is(readErr, ErrNoCredential) {
		return TargetStatus{}, fmt.Errorf("读取原凭据失败，未切换账号: %w", readErr)
	}
	if err = m.native.Write(ctx, transfer.Target, transfer.Credential); err != nil {
		if restoreErr := m.restore(transfer.Target, previous, readErr); restoreErr != nil {
			return TargetStatus{}, fmt.Errorf("写入账号凭据失败，无法确认原凭据恢复，请检查目标客户端登录状态: %w", err)
		}
		return TargetStatus{}, err
	}
	verify, err := m.native.Read(ctx, transfer.Target)
	if err != nil || verify.Token.AccessToken != transfer.Credential.Token.AccessToken || verify.Token.RefreshToken != transfer.Credential.Token.RefreshToken {
		if restoreErr := m.restore(transfer.Target, previous, readErr); restoreErr != nil {
			return TargetStatus{}, fmt.Errorf("账号凭据校验失败，恢复原凭据也失败，请重新登录目标客户端")
		}
		return TargetStatus{}, fmt.Errorf("账号凭据写入后校验失败，已恢复原凭据，未标记为已切换")
	}
	next := cloneState(m.state)
	now := time.Now()
	id := accountID(email)
	account := Account{ID: id, Email: email, Name: transfer.Name, Credential: transfer.Credential, OAuth: transfer.OAuth, CreatedAt: now, UpdatedAt: now}
	found := false
	for i, a := range next.Accounts {
		if a.ID == id {
			account.CreatedAt = a.CreatedAt
			account.Quota = a.Quota
			next.Accounts[i] = account
			found = true
			break
		}
	}
	if !found {
		next.Accounts = append(next.Accounts, account)
		if len(next.Accounts) > 200 {
			_ = m.restore(transfer.Target, previous, readErr)
			return TargetStatus{}, fmt.Errorf("远端最多保存 200 个账号")
		}
	}
	next.Activations = append(next.Activations, Activation{transfer.Target, id, now})
	if err = m.vault.save(next); err != nil {
		if restoreErr := m.restore(transfer.Target, previous, readErr); restoreErr != nil {
			return TargetStatus{}, fmt.Errorf("保存切号结果失败，恢复原凭据也失败，请重新登录目标客户端")
		}
		return TargetStatus{}, fmt.Errorf("保存切号结果失败，已恢复原凭据")
	}
	m.state = next
	message := "凭据已写入并校验。请重启 Antigravity 或新建 agy 会话；已运行的任务不会自动换号。"
	if transfer.Target.Client == "managed-cli" {
		message = "无桌面 CLI 账号组件已部署。请使用 ~/.local/bin/appforce-agy 启动 CLI；已运行的会话继续使用原账号。"
	}
	return TargetStatus{Target: transfer.Target, AccountID: id, Email: email, Available: true, NeedsRestart: true, Message: message}, nil
}

func (m *Manager) restore(target Target, previous Credential, readErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if readErr == nil {
		return m.native.Write(ctx, target, previous)
	}
	return m.native.Delete(ctx, target)
}

func (m *Manager) Status(ctx context.Context, target Target) TargetStatus {
	resolved, err := resolveTarget(target)
	if err != nil {
		return TargetStatus{Target: target, Message: err.Error()}
	}
	target = resolved.target
	c, err := m.native.Read(ctx, target)
	if err != nil {
		return TargetStatus{Target: target, Message: err.Error()}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.state.Accounts {
		if a.Credential.Token.AccessToken == c.Token.AccessToken || (c.Token.RefreshToken != "" && a.Credential.Token.RefreshToken == c.Token.RefreshToken) {
			return TargetStatus{Target: target, Available: true, AccountID: a.ID, Email: a.Email, Message: "已读取目标用户的账号凭据"}
		}
	}
	return TargetStatus{Target: target, Available: true, Message: "目标用户已有登录凭据，请导入当前账号以识别身份"}
}
