//go:build linux

package antigravity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func secretCommand(ctx context.Context, target resolvedTarget, args ...string) (*exec.Cmd, error) {
	bus := "/run/user/" + target.uid + "/bus"
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" {
		if _, err := os.Stat(bus); err != nil {
			return nil, fmt.Errorf("该用户没有可用的系统密钥环。纯 SSH 环境请先配置用户 D-Bus / Secret Service；不会使用无法确认生效的凭据文件回退")
		}
		address = "unix:path=" + bus
	}
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return nil, fmt.Errorf("请在 Linux 安装 libsecret-tools，并为目标用户启动和解锁 Secret Service 密钥环")
	}
	cmd := exec.CommandContext(ctx, "secret-tool", args...)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+address, "XDG_RUNTIME_DIR=/run/user/"+target.uid)
	return cmd, nil
}

func readSystemCredential(ctx context.Context, target resolvedTarget) (Credential, error) {
	cmd, err := secretCommand(ctx, target, "lookup", "service", "gemini", "username", "antigravity")
	if err != nil {
		return Credential{}, err
	}
	data, err := cmd.Output()
	if err == nil && len(data) == 0 {
		return Credential{}, ErrNoCredential
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && len(exit.Stderr) == 0 {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, fmt.Errorf("无法读取该用户的 Antigravity 凭据，请确认已登录并解锁系统密钥环")
	}
	return parseCredential(string(data))
}

func deleteSystemCredential(ctx context.Context, target resolvedTarget) error {
	cmd, err := secretCommand(ctx, target, "clear", "service", "gemini", "username", "antigravity")
	if err != nil {
		return err
	}
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("移除新写入的密钥环凭据失败")
	}
	return nil
}

func writeSystemCredential(ctx context.Context, target resolvedTarget, c Credential) error {
	cmd, err := secretCommand(ctx, target, "store", "--label=gemini", "service", "gemini", "username", "antigravity")
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(string(systemPayload(c)))
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("写入目标用户密钥环失败，请确认 Secret Service 已启动且密钥环已解锁")
	}
	return nil
}
