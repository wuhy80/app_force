// Package antigravity implements account and usage management independently of
// third-party account managers. Credentials never appear in public views.
package antigravity

import (
	"context"
	"errors"
	"time"
)

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

type Credential struct {
	Token      Token  `json:"token"`
	AuthMethod string `json:"auth_method"`
	Email      string `json:"account_email,omitempty"` // helper metadata, omitted from system keyring
}

// Target identifies an OS user, not a process. System credentials are shared by
// Antigravity 2.x and agy within that user's session.
type Target struct {
	User   string `json:"user"`
	Client string `json:"client"` // system or legacy-ide
}

type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	Cloud        string `json:"cloud"` // daily (consumer) or production (GCP)
}

type ImportRequest struct {
	Name       string `json:"name"`
	Credential string `json:"credential"`
}

type Account struct {
	ID         string      `json:"id"`
	Email      string      `json:"email"`
	Name       string      `json:"name"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Quota      *Quota      `json:"quota,omitempty"`
	QuotaError string      `json:"quota_error,omitempty"`
	Credential Credential  `json:"credential"`
	OAuth      OAuthConfig `json:"oauth"`
}

type AccountView struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Expiry      time.Time `json:"expiry"`
	Refreshable bool      `json:"refreshable"`
	Quota       *Quota    `json:"quota,omitempty"`
	QuotaError  string    `json:"quota_error,omitempty"`
}

type QuotaBucket struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Window    string   `json:"window,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"` // percent; absent is unknown
	ResetTime string   `json:"reset_time,omitempty"`
}

type Credit struct {
	Type   string `json:"type"`
	Amount string `json:"amount"`
}

type Quota struct {
	FetchedAt time.Time     `json:"fetched_at"`
	Tier      string        `json:"tier,omitempty"`
	Project   string        `json:"project,omitempty"`
	Buckets   []QuotaBucket `json:"buckets"`
	Credits   []Credit      `json:"credits"`
}

type Activation struct {
	Target    Target    `json:"target"`
	AccountID string    `json:"account_id"`
	Time      time.Time `json:"time"`
}

type TargetStatus struct {
	Target       Target `json:"target"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	Available    bool   `json:"available"`
	Message      string `json:"message"`
	NeedsRestart bool   `json:"needs_restart"`
}

type Transfer struct {
	Email      string      `json:"email"`
	Name       string      `json:"name"`
	Credential Credential  `json:"credential"`
	OAuth      OAuthConfig `json:"oauth"`
	Target     Target      `json:"target"`
}

type NativeStore interface {
	Read(ctx context.Context, target Target) (Credential, error)
	Write(ctx context.Context, target Target, value Credential) error
	Delete(ctx context.Context, target Target) error
}

var ErrNoCredential = errors.New("尚未保存 Antigravity 登录凭据")
