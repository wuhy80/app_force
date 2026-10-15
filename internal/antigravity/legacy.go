package antigravity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/shirou/gopsutil/v3/process"
	"google.golang.org/protobuf/encoding/protowire"
)

const oauthStateKey = "antigravityUnifiedStateSync.oauthToken"
const statusStateKey = "antigravityUnifiedStateSync.userStatus"

func legacyPath(target resolvedTarget) (string, error) {
	var bases []string
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(target.home, "AppData", "Roaming")
		}
		bases = []string{base}
	case "darwin":
		bases = []string{filepath.Join(target.home, "Library", "Application Support")}
	default:
		bases = []string{filepath.Join(target.home, ".config")}
	}
	paths := []string{}
	for _, base := range bases {
		for _, name := range []string{"Antigravity", "Antigravity IDE"} {
			path := filepath.Join(base, name, "User", "globalStorage", "state.vscdb")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("没有找到旧版 Antigravity IDE 的 state.vscdb，请先启动并登录 IDE")
	}
	if len(paths) > 1 {
		return "", fmt.Errorf("检测到多个 IDE 用户目录，无法安全确定旧版账号目标")
	}
	return paths[0], nil
}

func legacyDB(ctx context.Context, target resolvedTarget) (*sql.DB, error) {
	path, err := legacyPath(target)
	if err != nil {
		return nil, err
	}
	address := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(1500)"}}
	address.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", address.String())
	if err != nil {
		return nil, fmt.Errorf("无法打开 IDE 账号数据库")
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("无法读取 IDE 账号数据库")
	}
	return db, nil
}

type protoField struct {
	number  protowire.Number
	kind    protowire.Type
	data    []byte
	integer uint64
	raw     []byte
}

func protoFields(data []byte) ([]protoField, error) {
	fields := []protoField{}
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("IDE 凭据 protobuf 损坏")
		}
		length := protowire.ConsumeFieldValue(number, kind, data[n:])
		if length < 0 {
			return nil, fmt.Errorf("IDE 凭据 protobuf 损坏")
		}
		f := protoField{number: number, kind: kind, raw: data[:n+length]}
		if kind == protowire.BytesType {
			f.data, _ = protowire.ConsumeBytes(data[n:])
		}
		if kind == protowire.VarintType {
			f.integer, _ = protowire.ConsumeVarint(data[n:])
		}
		fields = append(fields, f)
		data = data[n+length:]
	}
	return fields, nil
}

func protoBytes(number protowire.Number, data []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), data)
}
func protoInteger(number protowire.Number, value uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, number, protowire.VarintType), value)
}
func protoValue(fields []protoField, number protowire.Number) []byte {
	for _, f := range fields {
		if f.number == number {
			return f.data
		}
	}
	return nil
}

func topicPayload(encoded, key string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("IDE 凭据编码无效")
	}
	fields, err := protoFields(data)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f.number != 1 || f.kind != protowire.BytesType {
			continue
		}
		entry, err := protoFields(f.data)
		if err != nil {
			return nil, err
		}
		if string(protoValue(entry, 1)) != key {
			continue
		}
		row, err := protoFields(protoValue(entry, 2))
		if err != nil {
			return nil, err
		}
		payload, err := base64.StdEncoding.DecodeString(string(protoValue(row, 1)))
		if err != nil {
			return nil, fmt.Errorf("IDE OAuth 数据无效")
		}
		return payload, nil
	}
	return nil, fmt.Errorf("IDE 中没有已登录的 OAuth 账号")
}

func replaceTopic(encoded, key string, payload []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("IDE 现有凭据格式无效，未覆盖")
	}
	fields, err := protoFields(data)
	if err != nil {
		return "", err
	}
	updated := []byte{}
	for _, f := range fields {
		if f.number == 1 && f.kind == protowire.BytesType {
			entry, err := protoFields(f.data)
			if err != nil {
				return "", err
			}
			if string(protoValue(entry, 1)) == key {
				continue
			}
		}
		updated = append(updated, f.raw...)
	}
	row := protoBytes(1, []byte(base64.StdEncoding.EncodeToString(payload)))
	entry := append(protoBytes(1, []byte(key)), protoBytes(2, row)...)
	updated = append(updated, protoBytes(1, entry)...)
	return base64.StdEncoding.EncodeToString(updated), nil
}

func readLegacy(ctx context.Context, target resolvedTarget) (Credential, error) {
	db, err := legacyDB(ctx, target)
	if err != nil {
		return Credential{}, err
	}
	defer db.Close()
	var encoded string
	if err = db.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", oauthStateKey).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, ErrNoCredential
		}
		return Credential{}, fmt.Errorf("旧版 IDE 尚未登录或不支持此凭据格式")
	}
	payload, err := topicPayload(encoded, "oauthTokenInfoSentinelKey")
	if err != nil {
		return Credential{}, err
	}
	fields, err := protoFields(payload)
	if err != nil {
		return Credential{}, err
	}
	for _, f := range fields {
		if f.number == 6 && f.integer != 0 {
			return Credential{}, fmt.Errorf("暂不支持旧版 IDE 的企业 GCP 登录")
		}
	}
	c := Credential{AuthMethod: "consumer", Token: Token{AccessToken: string(protoValue(fields, 1)), TokenType: string(protoValue(fields, 2)), RefreshToken: string(protoValue(fields, 3))}}
	stamp, err := protoFields(protoValue(fields, 4))
	if err != nil {
		return Credential{}, err
	}
	for _, f := range stamp {
		if f.number == 1 {
			c.Token.Expiry = time.Unix(int64(f.integer), 0)
		}
	}
	if db.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", statusStateKey).Scan(&encoded) == nil {
		if userData, err := topicPayload(encoded, "userStatusSentinelKey"); err == nil {
			if fields, err := protoFields(userData); err == nil {
				c.Email = string(protoValue(fields, 3))
			}
		}
	}
	return c, validateCredential(c)
}

func deleteLegacy(ctx context.Context, target resolvedTarget) error {
	db, err := legacyDB(ctx, target)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "DELETE FROM ItemTable WHERE key IN (?, ?)", oauthStateKey, statusStateKey)
	return err
}

func writeLegacy(ctx context.Context, target resolvedTarget, c Credential) error {
	if c.Token.Expiry.IsZero() {
		return fmt.Errorf("旧版 IDE 凭据需要提供有效的 expiry 到期时间")
	}
	// A running IDE can overwrite injected SQLite state on exit. Do not kill
	// editors or silently lose work; require the user to close it first.
	processes, _ := process.ProcessesWithContext(ctx)
	for _, p := range processes {
		name, _ := p.NameWithContext(ctx)
		name = strings.ToLower(name)
		if strings.Contains(name, "antigravity") {
			owner, _ := p.UsernameWithContext(ctx)
			if owner == "" || strings.EqualFold(owner, target.target.User) {
				return fmt.Errorf("请先关闭该用户的旧版 Antigravity IDE，再切换账号")
			}
		}
	}
	db, err := legacyDB(ctx, target)
	if err != nil {
		return err
	}
	defer db.Close()
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("无法开始 IDE 凭据事务")
	}
	defer transaction.Rollback()
	var old string
	err = transaction.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", oauthStateKey).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("无法读取现有 IDE 凭据")
	}
	payload := append(protoBytes(1, []byte(c.Token.AccessToken)), protoBytes(2, []byte(c.Token.TokenType))...)
	payload = append(payload, protoBytes(3, []byte(c.Token.RefreshToken))...)
	stamp := append(protoInteger(1, uint64(c.Token.Expiry.Unix())), protoInteger(2, 0)...)
	payload = append(payload, protoBytes(4, stamp)...)
	encoded, err := replaceTopic(old, "oauthTokenInfoSentinelKey", payload)
	if err != nil {
		return err
	}
	values := map[string]string{oauthStateKey: encoded, "antigravityOnboarding": "true"}
	if c.Email != "" {
		oldStatus := ""
		err = transaction.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", statusStateKey).Scan(&oldStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("无法读取 IDE 用户状态")
		}
		userPayload := append(protoBytes(3, []byte(c.Email)), protoBytes(7, []byte(c.Email))...)
		status, err := replaceTopic(oldStatus, "userStatusSentinelKey", userPayload)
		if err != nil {
			return err
		}
		values[statusStateKey] = status
	}
	for key, value := range values {
		if _, err = transaction.ExecContext(ctx, "INSERT OR REPLACE INTO ItemTable (key,value) VALUES (?,?)", key, value); err != nil {
			return fmt.Errorf("写入 IDE 凭据失败，已取消事务")
		}
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("提交 IDE 凭据失败")
	}
	return nil
}
