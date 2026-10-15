//go:build !windows && !darwin && !linux

package antigravity

import (
	"context"
	"fmt"
)

func readSystemCredential(context.Context, resolvedTarget) (Credential, error) {
	return Credential{}, fmt.Errorf("该系统尚不支持 Antigravity 账号管理")
}
func writeSystemCredential(context.Context, resolvedTarget, Credential) error {
	return fmt.Errorf("该系统尚不支持 Antigravity 账号管理")
}

func deleteSystemCredential(context.Context, resolvedTarget) error {
	return fmt.Errorf("该系统尚不支持 Antigravity 账号管理")
}
