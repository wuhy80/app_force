//go:build darwin

package antigravity

import (
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"
)

func lookupOSUser(name string) (*user.User, error) {
	if account, err := user.Lookup(name); err == nil {
		return account, nil
	}
	// Cross-built macOS Agents have CGO disabled. Their os/user package only
	// sees /etc/passwd, while normal Mac users live in Directory Service.
	if name == "" || strings.ContainsAny(name, "/:\x00\r\n") {
		return nil, fmt.Errorf("用户名无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UniqueID", "NFSHomeDirectory").Output()
	if err != nil {
		return nil, fmt.Errorf("无法读取 Mac 用户信息")
	}
	return parseDirectoryUser(name, string(data))
}

func parseDirectoryUser(name, data string) (*user.User, error) {
	account := &user.User{Username: name}
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "UniqueID:") {
			account.Uid = strings.TrimSpace(strings.TrimPrefix(line, "UniqueID:"))
		}
		if strings.HasPrefix(line, "NFSHomeDirectory:") {
			account.HomeDir = strings.TrimSpace(strings.TrimPrefix(line, "NFSHomeDirectory:"))
		}
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 || !strings.HasPrefix(account.HomeDir, "/") {
		return nil, fmt.Errorf("Mac 用户缺少有效的 UID 或主目录")
	}
	return account, nil
}
