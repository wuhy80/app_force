//go:build darwin

package antigravity

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
)

func readSystemCredential(ctx context.Context, target resolvedTarget) (Credential, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w")
	data, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 44 {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, fmt.Errorf("无法读取 Antigravity 钥匙串，请确认目标用户已登录并解锁登录钥匙串")
	}
	return parseCredential(string(data))
}

func deleteSystemCredential(ctx context.Context, target resolvedTarget) error {
	if err := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", "gemini", "-a", "antigravity").Run(); err != nil {
		return fmt.Errorf("移除新写入的钥匙串凭据失败")
	}
	return nil
}

func writeSystemCredential(ctx context.Context, target resolvedTarget, c Credential) error {
	data := systemPayload(c)
	// security's interactive mode accepts the secret on stdin; it is not visible
	// in process arguments. Base64 has no quoting or command delimiter characters.
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s gemini -a antigravity -w go-keyring-base64:" + base64.StdEncoding.EncodeToString(data) + "\n")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入 Antigravity 钥匙串失败，请解锁目标用户的登录钥匙串")
	}
	return nil
}
