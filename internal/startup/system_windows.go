//go:build windows

package startup

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const supported = true

func newSystemBackend(args []string) (backend, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	current, err := user.Current()
	if err != nil {
		return nil, err
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	dataDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dataDir = filepath.Join(dataDir, "AppForce")
	for i, arg := range args {
		if arg == "--data" && i+1 < len(args) {
			dataDir = args[i+1]
		}
	}
	return &scheduledTask{
		name: taskName(current.Uid, filepath.Clean(dataDir)), userID: current.Uid,
		executable: executable, directory: filepath.Dir(executable), args: append([]string(nil), args...),
		run: func(ctx context.Context, encoded string) ([]byte, error) {
			powershell := filepath.Join(systemDir, "WindowsPowerShell", "v1.0", "powershell.exe")
			cmd := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
			return cmd.CombinedOutput()
		},
	}, nil
}
