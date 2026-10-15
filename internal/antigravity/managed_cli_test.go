package antigravity

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedCLIWrapperHasValidShellAndPreservesArgumentBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell is only used on Linux")
	}
	root := t.TempDir()
	path := filepath.Join(root, "appforce-agy")
	if err := os.WriteFile(path, []byte(managedCLIScript), 0700); err != nil {
		t.Fatal(err)
	}
	if data, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("invalid shell: %s", data)
	}
	// Check the nested shell without invoking a user's real keyring or agy.
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	mock := `#!/bin/sh
shift
shift
shift
shift
printf '%s' "$1" > "$APPFORCE_TEST_SCRIPT"
shift
shift
printf '%s\n' "$@" > "$APPFORCE_TEST_ARGS"
`
	if err := os.WriteFile(filepath.Join(bin, "dbus-run-session"), []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested.sh")
	args := filepath.Join(root, "args.txt")
	cmd := exec.Command("sh", path, "--print", "argument with spaces", "literal;$value")
	cmd.Env = append(os.Environ(), "HOME="+root, "APPFORCE_TEST_SCRIPT="+nested, "APPFORCE_TEST_ARGS="+args)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper did not forward shell: %s", data)
	}
	if data, err := exec.Command("sh", "-n", nested).CombinedOutput(); err != nil {
		t.Fatalf("invalid nested shell: %s", data)
	}
	data, err := os.ReadFile(args)
	if err != nil || string(data) != "--print\nargument with spaces\nliteral;$value\n" {
		t.Fatalf("arguments changed: %s %v", data, err)
	}
}
