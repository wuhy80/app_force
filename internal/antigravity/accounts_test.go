package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status := 200
		body := "{}"
		switch {
		case r.URL.Host == "oauth2.googleapis.com" && r.URL.Path == "/token":
			body = `{"access_token":"new-access-secret","refresh_token":"new-refresh-secret","token_type":"Bearer","expires_in":3600}`
		case r.URL.Path == "/oauth2/v2/userinfo":
			if r.Header.Get("Authorization") == "Bearer invalid-secret" {
				status = 401
				body = `{"error":"sensitive-response-must-not-leak"}`
			} else {
				body = `{"email":"person@example.com","verified_email":true}`
			}
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			body = `{"cloudaicompanionProject":"project-one","paidTier":{"id":"pro-tier","availableCredits":[{"creditType":"AI_CREDIT","creditAmount":"100"}]}}`
		case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			body = `{"groups":[{"displayName":"Gemini","buckets":[{"id":"weekly","window":"WEEKLY","remainingFraction":0,"resetTime":"2026-10-15T08:00:00Z"},{"id":"unknown","window":"DAILY"}]}]}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

func newManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m, err := Open(dir, providerClient)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func testCredential() Credential {
	return Credential{Token: Token{AccessToken: "access-secret", RefreshToken: "refresh-secret", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).Truncate(time.Second)}, AuthMethod: "consumer"}
}

func TestEncryptedAccountsPersistAndNeverExposeSecrets(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, dir)
	if err := m.SetConfig(OAuthConfig{ClientID: "own.apps.googleusercontent.com", ClientSecret: "client-secret", Cloud: "daily"}); err != nil {
		t.Fatal(err)
	}
	a, err := m.ImportCredential(context.Background(), "Work", testCredential())
	if err != nil {
		t.Fatal(err)
	}
	quota, err := m.RefreshQuota(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quota.Buckets[0].Remaining == nil || *quota.Buckets[0].Remaining != 0 || quota.Buckets[1].Remaining != nil {
		t.Fatal("zero and unknown quota conflated")
	}
	public, _ := json.Marshal(m.List())
	config, _ := json.Marshal(m.Config())
	sealed, err := os.ReadFile(filepath.Join(dir, "accounts.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "client-secret"} {
		for _, value := range [][]byte{public, config, sealed} {
			if bytes.Contains(value, []byte(secret)) {
				t.Fatal("credential leaked from public view or vault")
			}
		}
	}
	reopened := newManager(t, dir)
	if len(reopened.List()) != 1 || reopened.List()[0].ID != a.ID || reopened.List()[0].Quota == nil {
		t.Fatal("accounts/quota did not persist")
	}
	if _, err = m.ImportCredential(context.Background(), "", Credential{Token: Token{AccessToken: "invalid-secret", TokenType: "Bearer"}, AuthMethod: "consumer"}); err == nil || strings.Contains(err.Error(), "sensitive-response") {
		t.Fatal("invalid login accepted or server body leaked")
	}
	if len(m.List()) != 1 {
		t.Fatal("failed import changed accounts")
	}
	if err = m.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Fatal("account was not removed")
	}
}

func TestMissingOrDamagedVaultKeyDoesNotOverwriteAccounts(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, dir)
	if _, err := m.ImportCredential(context.Background(), "", testCredential()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "accounts.enc"))
	if err := os.Remove(filepath.Join(dir, "accounts.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, providerClient); err == nil {
		t.Fatal("missing key silently replaced")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "accounts.enc"))
	if !bytes.Equal(before, after) {
		t.Fatal("vault overwritten")
	}
}

type memoryNative struct {
	value       Credential
	missing     bool
	badReadback bool
	readCount   int
	writes      int
	deletes     int
	readError   error
}

func (n *memoryNative) Read(context.Context, Target) (Credential, error) {
	n.readCount++
	if n.readError != nil {
		return Credential{}, n.readError
	}
	if n.missing {
		return Credential{}, ErrNoCredential
	}
	if n.badReadback && n.readCount == 2 {
		c := n.value
		c.Token.AccessToken = "wrong"
		return c, nil
	}
	return n.value, nil
}
func (n *memoryNative) Write(_ context.Context, _ Target, c Credential) error {
	n.value = c
	n.missing = false
	n.writes++
	return nil
}
func (n *memoryNative) Delete(context.Context, Target) error {
	n.missing = true
	n.deletes++
	return nil
}

func testTarget(t *testing.T) Target {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return Target{User: u.Username, Client: "system"}
}

func TestSwitchVerificationAndRollback(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			m := newManager(t, t.TempDir())
			old := testCredential()
			old.Token.AccessToken = "original"
			native := &memoryNative{value: old, missing: missing, badReadback: true}
			m.native = native
			transfer := Transfer{Email: "person@example.com", Name: "work", Credential: testCredential(), Target: testTarget(t)}
			if _, err := m.Apply(context.Background(), transfer); err == nil {
				t.Fatal("bad readback accepted")
			}
			if len(m.List()) != 0 || len(m.state.Activations) != 0 {
				t.Fatal("failed switch recorded as active")
			}
			if missing {
				if !native.missing || native.deletes != 1 {
					t.Fatal("new credential not removed")
				}
			} else if native.value.Token.AccessToken != "original" {
				t.Fatal("original credential not restored")
			}
		})
	}
	m := newManager(t, t.TempDir())
	native := &memoryNative{missing: true}
	m.native = native
	transfer := Transfer{Email: "person@example.com", Credential: testCredential(), Target: testTarget(t)}
	status, err := m.Apply(context.Background(), transfer)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.AccountID != accountID(transfer.Email) || len(m.state.Activations) != 1 {
		t.Fatal("verified switch not persisted")
	}
	if result := m.Status(context.Background(), transfer.Target); result.AccountID != status.AccountID {
		t.Fatal("native status did not reflect switched login")
	}
	native.readError = fmt.Errorf("locked keyring")
	writes := native.writes
	if _, err = m.Apply(context.Background(), transfer); err == nil || native.writes != writes {
		t.Fatal("unreadable old credential overwritten")
	}
}

func TestRefreshUsesAccountClientAndPreservesOtherAccountConfig(t *testing.T) {
	m := newManager(t, t.TempDir())
	original := OAuthConfig{ClientID: "original.apps.googleusercontent.com", ClientSecret: "original-secret", Cloud: "daily"}
	if err := m.SetConfig(original); err != nil {
		t.Fatal(err)
	}
	c := testCredential()
	c.Token.Expiry = time.Now().Add(-time.Hour)
	view, err := m.ImportCredential(context.Background(), "", c)
	if err != nil {
		t.Fatal(err)
	}
	if view.Expiry.Before(time.Now()) || !view.Refreshable {
		t.Fatal("expired credential was not renewed")
	}
	if err = m.SetConfig(OAuthConfig{ClientID: "different.apps.googleusercontent.com", Cloud: "production"}); err != nil {
		t.Fatal(err)
	}
	transfer, err := m.Transfer(context.Background(), view.ID, testTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	if transfer.OAuth != original || transfer.Credential.Token.AccessToken != "new-access-secret" {
		t.Fatal("refresh changed original OAuth identity")
	}
}

func TestOAuthStatePKCEAndCallback(t *testing.T) {
	m := newManager(t, t.TempDir())
	if err := m.SetConfig(OAuthConfig{ClientID: "own.apps.googleusercontent.com", ClientSecret: "oauth-secret"}); err != nil {
		t.Fatal(err)
	}
	session, err := m.StartOAuth()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(session.URL)
	query := authURL.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatal("OAuth missing state/PKCE")
	}
	callback := query.Get("redirect_uri")
	resp, err := http.Get(callback + "?state=wrong&code=one")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 || len(m.List()) != 0 {
		t.Fatal("invalid state accepted")
	}
	resp, err = http.Get(callback + "?" + url.Values{"state": {query.Get("state")}, "code": {"one"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(m.List()) != 1 {
		t.Fatal("valid OAuth callback not saved")
	}
	view, err := m.OAuthStatus(session.ID)
	if err != nil || view.Status != "succeeded" || view.URL != "" {
		t.Fatal("OAuth polling exposes URL or incorrect result")
	}
}

func TestDefaultOAuthLoginNeedsNoManualTokenAndCanBeCancelled(t *testing.T) {
	m := newManager(t, t.TempDir())
	session, err := m.StartOAuth()
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(session.URL)
	if err != nil || authURL.Host != "accounts.google.com" || authURL.Query().Get("client_id") == "" || authURL.Query().Get("client_secret") != "" {
		t.Fatal("default browser login is unavailable or exposes client secret")
	}
	if cancelled, err := m.CancelOAuth(session.ID); err != nil || cancelled.Status != "cancelled" || cancelled.URL != "" {
		t.Fatal("login was not cancelled")
	}
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(authURL.Query().Get("redirect_uri")); err == nil {
		response.Body.Close()
		t.Fatal("cancelled callback still listening")
	}
	if len(m.List()) != 0 {
		t.Fatal("cancelling browser login changed saved accounts")
	}
	session, err = m.StartOAuth()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ = url.Parse(session.URL)
	query := authURL.Query()
	response, err := client.Get(query.Get("redirect_uri") + "?" + url.Values{"state": {query.Get("state")}, "error": {"access_denied"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	view, err := m.OAuthStatus(session.ID)
	if err != nil || view.Status != "failed" || len(m.List()) != 0 {
		t.Fatal("denied browser authorization was not reported without adding an account")
	}
}

func TestOAuthCannotCancelOrReplaceAnAccountBeingSaved(t *testing.T) {
	m := newManager(t, t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	base := providerClient().Transport
	m.client = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "oauth2.googleapis.com" {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return base.RoundTrip(r)
		})}
	}
	session, err := m.StartOAuth()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(session.URL)
	query := authURL.Query()
	finished := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(query.Get("redirect_uri") + "?" + url.Values{"state": {query.Get("state")}, "code": {"fixture-code"}}.Encode())
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("callback status %d", response.StatusCode)
			}
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback never began token exchange")
	}
	if _, err := m.CancelOAuth(session.ID); err == nil {
		t.Error("in-flight account save could be cancelled")
	}
	if _, err := m.StartOAuth(); err == nil {
		t.Error("in-flight account save could be replaced")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	view, err := m.CancelOAuth(session.ID)
	if err != nil || view.Status != "succeeded" || len(m.List()) != 1 {
		t.Fatal("completed account was cancelled or lost")
	}
}
