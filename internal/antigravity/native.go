package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

type systemStore struct{}

// NativeHelperExitCode preserves a missing credential across the user helper
// process boundary, so a first account application can proceed. Other failures
// must still prevent overwriting a credential that could not be read.
func NativeHelperExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, ErrNoCredential) {
		return 20
	}
	return 1
}

type resolvedTarget struct {
	target Target
	home   string
	uid    string
	other  bool
}

func systemPayload(c Credential) []byte {
	data, _ := json.Marshal(struct {
		Token      Token  `json:"token"`
		AuthMethod string `json:"auth_method"`
	}{c.Token, c.AuthMethod})
	return data
}

func resolveTarget(target Target) (resolvedTarget, error) {
	if target.Client == "" {
		target.Client = "system"
	}
	if target.Client != "system" && target.Client != "legacy-ide" && target.Client != "managed-cli" {
		return resolvedTarget{}, fmt.Errorf("请选择 Antigravity 2.x / agy 或旧版 IDE")
	}
	if target.Client == "managed-cli" && runtime.GOOS != "linux" {
		return resolvedTarget{}, fmt.Errorf("无桌面 CLI 账号组件仅适用于 Linux")
	}
	current, err := user.Current()
	if err != nil {
		return resolvedTarget{}, fmt.Errorf("无法确定运行用户")
	}
	if target.User == "" {
		if runtime.GOOS != "windows" && current.Uid == "0" {
			return resolvedTarget{}, fmt.Errorf("Agent 以 root 运行，请填写实际运行 Antigravity / agy 的 Linux 或 Mac 用户名")
		}
		target.User = current.Username
	}
	if len(target.User) > 256 || strings.ContainsAny(target.User, "\x00\r\n") || strings.HasPrefix(target.User, "-") {
		return resolvedTarget{}, fmt.Errorf("用户名无效")
	}
	account := current
	if !strings.EqualFold(target.User, current.Username) {
		if runtime.GOOS == "windows" {
			return resolvedTarget{}, fmt.Errorf("Windows 只能管理当前登录用户的 Antigravity 凭据")
		}
		account, err = lookupOSUser(target.User)
		if err != nil {
			return resolvedTarget{}, fmt.Errorf("目标用户不存在")
		}
	}
	other := account.Uid != current.Uid
	if other && current.Uid != "0" {
		return resolvedTarget{}, fmt.Errorf("管理其他用户需要以 root 运行 Agent")
	}
	if !filepath.IsAbs(account.HomeDir) {
		return resolvedTarget{}, fmt.Errorf("目标用户没有有效的主目录")
	}
	target.User = account.Username
	return resolvedTarget{target, account.HomeDir, account.Uid, other}, nil
}

func (systemStore) Read(ctx context.Context, target Target) (Credential, error) {
	resolved, err := resolveTarget(target)
	if err != nil {
		return Credential{}, err
	}
	if resolved.other {
		return runNativeHelper(ctx, resolved, "read", Credential{})
	}
	if resolved.target.Client == "legacy-ide" {
		return readLegacy(ctx, resolved)
	}
	if resolved.target.Client == "managed-cli" {
		return readManagedCLI(ctx, resolved)
	}
	return readSystemCredential(ctx, resolved)
}

func (systemStore) Write(ctx context.Context, target Target, c Credential) error {
	resolved, err := resolveTarget(target)
	if err != nil {
		return err
	}
	if resolved.other {
		_, err = runNativeHelper(ctx, resolved, "write", c)
		return err
	}
	if resolved.target.Client == "legacy-ide" {
		return writeLegacy(ctx, resolved, c)
	}
	if resolved.target.Client == "managed-cli" {
		return writeManagedCLI(ctx, resolved, c)
	}
	return writeSystemCredential(ctx, resolved, c)
}

func targetCommand(ctx context.Context, target resolvedTarget, command string, args ...string) (*exec.Cmd, error) {
	if !target.other {
		return exec.CommandContext(ctx, command, args...), nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.CommandContext(ctx, "runuser", append([]string{"-u", target.target.User, "--", command}, args...)...)
	case "darwin":
		cmd = exec.CommandContext(ctx, "/bin/launchctl", append([]string{"asuser", target.uid, "/usr/bin/sudo", "-n", "-u", target.target.User, "--", command}, args...)...)
	default:
		return nil, fmt.Errorf("该平台不支持跨用户操作")
	}
	// The user helper opens the user's databases and session keyring without root
	// file access, and receives secrets via stdin rather than command arguments.
	cmd.Env = []string{"PATH=" + filepath.Join(target.home, ".local", "bin") + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + target.home, "USER=" + target.target.User, "LOGNAME=" + target.target.User}
	if runtime.GOOS == "linux" {
		cmd.Env = append(cmd.Env, "XDG_RUNTIME_DIR=/run/user/"+target.uid, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+target.uid+"/bus")
	}
	return cmd, nil
}

func (systemStore) Delete(ctx context.Context, target Target) error {
	resolved, err := resolveTarget(target)
	if err != nil {
		return err
	}
	if resolved.other {
		_, err = runNativeHelper(ctx, resolved, "delete", Credential{})
		return err
	}
	if target.Client == "legacy-ide" {
		return deleteLegacy(ctx, resolved)
	}
	if target.Client == "managed-cli" {
		err = os.Remove(filepath.Join(managedCLIDir(resolved), "credential.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return deleteSystemCredential(ctx, resolved)
}

func runNativeHelper(ctx context.Context, target resolvedTarget, operation string, c Credential) (Credential, error) {
	binary, err := os.Executable()
	if err != nil {
		return Credential{}, fmt.Errorf("无法定位账号组件")
	}
	cmd, err := targetCommand(ctx, target, binary, "--antigravity-native="+operation, "--antigravity-client="+target.target.Client)
	if err != nil {
		return Credential{}, err
	}
	if operation == "write" {
		data, _ := json.Marshal(c)
		cmd.Stdin = strings.NewReader(string(data))
	}
	return runNativeCommand(cmd, operation)
}

func runNativeCommand(cmd *exec.Cmd, operation string) (Credential, error) {
	// stderr contains only our helper's fixed diagnostic, never third-party output.
	var diagnostic strings.Builder
	cmd.Stderr = &diagnostic
	data, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == NativeHelperExitCode(ErrNoCredential) {
			return Credential{}, ErrNoCredential
		}
		message := strings.TrimSpace(diagnostic.String())
		if strings.HasPrefix(message, "AppForce account: ") && len(message) < 600 {
			return Credential{}, fmt.Errorf("%s", strings.TrimPrefix(message, "AppForce account: "))
		}
		return Credential{}, fmt.Errorf("无法在目标用户会话中运行账号组件，请检查权限和用户登录状态")
	}
	if operation != "read" {
		return Credential{}, nil
	}
	return parseCredential(string(data))
}

// NativeHelper is called before opening AppForce data or taking its instance
// lock. The remote root Agent invokes the same binary under the selected user.
func NativeHelper(ctx context.Context, operation, client string, in io.Reader, out io.Writer) error {
	if operation != "read" && operation != "write" && operation != "delete" {
		return fmt.Errorf("账号组件操作无效")
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("无法确定账号组件用户")
	}
	target := Target{User: current.Username, Client: client}
	native := systemStore{}
	if operation == "delete" {
		return native.Delete(ctx, target)
	}
	if operation == "read" {
		c, err := native.Read(ctx, target)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(c)
	}
	data, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil || len(data) > 65536 {
		return fmt.Errorf("凭据输入过大或无效")
	}
	c, err := parseCredential(string(data))
	if err != nil {
		return err
	}
	return native.Write(ctx, target, c)
}
