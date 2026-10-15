package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func requestJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body io.Reader, contentType string, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("账号服务请求无效")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "AppForce/Antigravity")
	// OAuth credentials must never follow a redirect to another host.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer copyClient.CloseIdleConnections()
	response, err := copyClient.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 Google 账号服务，请检查代理和网络")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case 401:
			return fmt.Errorf("登录凭据已失效，请重新登录或导入账号")
		case 403:
			return fmt.Errorf("账号无权读取额度，请确认登录类型、地区和服务权限（HTTP 403）")
		case 429:
			return fmt.Errorf("账号服务请求过于频繁，请稍后刷新（HTTP 429）")
		default:
			return fmt.Errorf("账号服务返回 HTTP %d", response.StatusCode)
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, result) != nil {
		return fmt.Errorf("账号服务返回了无法识别的数据")
	}
	return nil
}

func identify(ctx context.Context, client *http.Client, token string) (string, error) {
	var user struct {
		Email    string `json:"email"`
		Verified *bool  `json:"verified_email"`
	}
	err := requestJSON(ctx, client, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", token, nil, "", &user)
	if err != nil {
		return "", err
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" || len(email) > 320 || !strings.Contains(email, "@") || (user.Verified != nil && !*user.Verified) {
		return "", fmt.Errorf("无法验证 Google 账号邮箱")
	}
	return email, nil
}

func (m *Manager) fresh(ctx context.Context, a *Account) error {
	if a.Credential.Token.Expiry.IsZero() || a.Credential.Token.Expiry.After(time.Now().Add(time.Minute)) {
		return nil
	}
	if a.Credential.Token.RefreshToken == "" || a.OAuth.ClientID == "" {
		return fmt.Errorf("账号凭据已过期，请重新导入；自动续期需要配置原登录使用的 OAuth 客户端")
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {a.Credential.Token.RefreshToken}, "client_id": {a.OAuth.ClientID}}
	if a.OAuth.ClientSecret != "" {
		values.Set("client_secret", a.OAuth.ClientSecret)
	}
	token, err := exchange(ctx, m.client(), values)
	if err != nil {
		return err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = a.Credential.Token.RefreshToken
	}
	a.Credential.Token = token
	return nil
}

func exchange(ctx context.Context, client *http.Client, values url.Values) (Token, error) {
	var reply struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
	}
	if err := requestJSON(ctx, client, http.MethodPost, "https://oauth2.googleapis.com/token", "", strings.NewReader(values.Encode()), "application/x-www-form-urlencoded", &reply); err != nil {
		return Token{}, err
	}
	if reply.Access == "" || reply.Expires <= 0 || reply.Expires > 86400*365 {
		return Token{}, fmt.Errorf("OAuth 服务没有返回有效 token")
	}
	if reply.Type == "" {
		reply.Type = "Bearer"
	}
	return Token{reply.Access, reply.Refresh, reply.Type, time.Now().Add(time.Duration(reply.Expires) * time.Second)}, nil
}

func cloudBase(config OAuthConfig) string {
	if config.Cloud == "production" {
		return "https://cloudcode-pa.googleapis.com/"
	}
	return "https://daily-cloudcode-pa.googleapis.com/"
}

func cloudPost(ctx context.Context, client *http.Client, config OAuthConfig, token, path string, payload any, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return requestJSON(ctx, client, http.MethodPost, cloudBase(config)+"v1internal:"+path, token, bytes.NewReader(data), "application/json", result)
}

func fetchQuota(ctx context.Context, client *http.Client, account Account) (*Quota, error) {
	quota := &Quota{FetchedAt: time.Now(), Buckets: []QuotaBucket{}, Credits: []Credit{}}
	var eligibility map[string]any
	loadErr := cloudPost(ctx, client, account.OAuth, account.Credential.Token.AccessToken, "loadCodeAssist", map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY", "ideName": "antigravity", "pluginType": "GEMINI"}}, &eligibility)
	if loadErr == nil {
		quota.Project = stringValue(eligibility["cloudaicompanionProject"])
		if project, ok := eligibility["cloudaicompanionProject"].(map[string]any); ok {
			quota.Project = stringValue(project["id"])
		}
		for _, key := range []string{"paidTier", "currentTier"} {
			if tier, ok := eligibility[key].(map[string]any); ok {
				if quota.Tier == "" {
					quota.Tier = stringValue(tier["id"])
				}
				if credits, ok := tier["availableCredits"].([]any); ok {
					for _, item := range credits {
						if c, ok := item.(map[string]any); ok {
							amount := stringValue(c["creditAmount"])
							if amount != "" {
								quota.Credits = append(quota.Credits, Credit{stringValue(c["creditType"]), amount})
							}
						}
					}
				}
			}
		}
	}
	payload := map[string]any{}
	if quota.Project != "" {
		payload["project"] = quota.Project
	}
	var summary map[string]any
	summaryErr := cloudPost(ctx, client, account.OAuth, account.Credential.Token.AccessToken, "retrieveUserQuotaSummary", payload, &summary)
	if summaryErr == nil {
		quota.Buckets = parseSummary(summary)
	}
	if len(quota.Buckets) == 0 {
		var models map[string]any
		modelsErr := cloudPost(ctx, client, account.OAuth, account.Credential.Token.AccessToken, "fetchAvailableModels", payload, &models)
		if modelsErr == nil {
			quota.Buckets = parseModels(models)
		}
		if len(quota.Buckets) == 0 && modelsErr != nil && summaryErr != nil {
			return nil, summaryErr
		}
	}
	if len(quota.Buckets) == 0 && len(quota.Credits) == 0 {
		return nil, fmt.Errorf("服务未返回可识别的额度，保留上次结果；请检查账号权限或客户端版本")
	}
	return quota, nil
}

func stringValue(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case json.Number:
		return string(v)
	}
	return ""
}

func field(object map[string]any, names ...string) any {
	for _, key := range names {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func fraction(object map[string]any) *float64 {
	v, ok := field(object, "remainingFraction", "remaining_fraction").(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return nil
	}
	percent := v * 100
	return &percent
}

func parseSummary(summary map[string]any) []QuotaBucket {
	if inner, ok := summary["quotaSummary"].(map[string]any); ok {
		summary = inner
	}
	groups, _ := summary["groups"].([]any)
	result := []QuotaBucket{}
	for groupIndex, value := range groups {
		group, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(field(group, "displayName", "display_name", "name", "groupName", "group_name"))
		if name == "" {
			name = fmt.Sprintf("模型组 %d", groupIndex+1)
		}
		buckets, _ := group["buckets"].([]any)
		for index, value := range buckets {
			bucket, ok := value.(map[string]any)
			if !ok {
				continue
			}
			id := stringValue(field(bucket, "id", "bucketId", "bucket_id"))
			if id == "" {
				id = fmt.Sprintf("group-%d-%d", groupIndex, index)
			}
			result = append(result, QuotaBucket{ID: id, Name: name, Window: stringValue(bucket["window"]), Remaining: fraction(bucket), ResetTime: stringValue(field(bucket, "resetTime", "reset_time"))})
		}
	}
	return result
}

func parseModels(response map[string]any) []QuotaBucket {
	result := []QuotaBucket{}
	models, _ := response["models"].(map[string]any)
	for id, value := range models {
		model, ok := value.(map[string]any)
		if !ok {
			continue
		}
		info, ok := model["quotaInfo"].(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(model["displayName"])
		if name == "" {
			name = id
		}
		result = append(result, QuotaBucket{ID: id, Name: name, Remaining: fraction(info), ResetTime: stringValue(info["resetTime"])})
	}
	if buckets, ok := response["quotaBuckets"].([]any); ok {
		for _, value := range buckets {
			b, ok := value.(map[string]any)
			if !ok {
				continue
			}
			result = append(result, QuotaBucket{ID: stringValue(b["id"]), Name: stringValue(b["displayName"]), Remaining: fraction(b), ResetTime: stringValue(b["resetTime"])})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
