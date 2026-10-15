package antigravity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// A fresh D-Bus session with a private Secret Service avoids relying on agy's
// problematic headless file fallback. Only this wrapper uses the managed login;
// the user's ordinary desktop keyring remains independent.
const managedCLIScript = `#!/bin/sh
set -eu
export PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
export AGY_CLI_DISABLE_AUTO_UPDATE=true
exec dbus-run-session -- sh -eu -c '
  APPFORCE_KEYRING_DIR=$(mktemp -d "${TMPDIR:-/tmp}/appforce-agy.XXXXXX")
  trap '\''rm -rf "$APPFORCE_KEYRING_DIR"'\'' EXIT
  export XDG_DATA_HOME="$APPFORCE_KEYRING_DIR"
  mkdir -p "$XDG_DATA_HOME/keyrings"
  chmod 700 "$XDG_DATA_HOME" "$XDG_DATA_HOME/keyrings"
  gnome-keyring-daemon --unlock --components=secrets < "$HOME/.config/AppForce/antigravity-cli/keyring-password" >/dev/null
  secret-tool store --label=gemini service gemini username antigravity < "$HOME/.config/AppForce/antigravity-cli/credential.json"
  agy "$@"
' appforce-agy "$@"
`

func managedCLIDir(target resolvedTarget) string {
	return filepath.Join(target.home, ".config", "AppForce", "antigravity-cli")
}

func readManagedCLI(ctx context.Context, target resolvedTarget) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	data, err := os.ReadFile(filepath.Join(managedCLIDir(target), "credential.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, fmt.Errorf("%w，请先应用一个已保存账号部署无桌面 CLI 组件", ErrNoCredential)
		}
		return Credential{}, fmt.Errorf("尚未部署无桌面 CLI 账号组件，请先应用一个已保存账号")
	}
	return parseCredential(string(data))
}

func writeManagedCLI(ctx context.Context, target resolvedTarget, c Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, command := range []string{"dbus-run-session", "gnome-keyring-daemon", "secret-tool"} {
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("无桌面 CLI 需要 %s；Debian/Ubuntu 可安装 dbus libsecret-tools gnome-keyring 后重试", command)
		}
	}
	dir := managedCLIDir(target)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("无法创建用户 CLI 账号目录")
	}
	password := filepath.Join(dir, "keyring-password")
	if _, err := os.Stat(password); os.IsNotExist(err) {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		f, err := os.OpenFile(password, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("无法创建 CLI 密钥环密码")
		}
		_, err = f.Write([]byte(hex.EncodeToString(key)))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return fmt.Errorf("无法读取 CLI 密钥环密码")
	}
	binDir := filepath.Join(target.home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return err
	}
	if err := writePrivateFile(filepath.Join(binDir, "appforce-agy"), []byte(managedCLIScript), 0700); err != nil {
		return err
	}
	// Validate Secret Service readback in an isolated bus before publishing a
	// credential file. The probe never launches a model request or alters agy.
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(probe.Name())
	data := systemPayload(c)
	_, err = probe.Write(data)
	closeErr := probe.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	probeScript := `set -eu
APPFORCE_KEYRING_DIR=$(mktemp -d "${TMPDIR:-/tmp}/appforce-agy.XXXXXX")
trap 'rm -rf "$APPFORCE_KEYRING_DIR"' EXIT
export XDG_DATA_HOME="$APPFORCE_KEYRING_DIR"
mkdir -m 700 "$XDG_DATA_HOME/keyrings"
gnome-keyring-daemon --unlock --components=secrets < "$1" >/dev/null
secret-tool store --label=gemini service gemini username antigravity < "$2"
secret-tool lookup service gemini username antigravity
`
	cmd := exec.CommandContext(ctx, "dbus-run-session", "--", "sh", "-c", probeScript, "appforce-keyring-probe", password, probe.Name())
	readback, err := cmd.Output()
	if err != nil || !bytes.Equal(bytes.TrimSpace(readback), data) {
		return fmt.Errorf("无桌面 CLI 密钥环读写校验失败，请检查 D-Bus 和 gnome-keyring 安装")
	}
	return writePrivateFile(filepath.Join(dir, "credential.json"), systemPayload(c), 0600)
}

func writePrivateFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".account-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
