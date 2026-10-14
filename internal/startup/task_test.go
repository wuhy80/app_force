package startup

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeCommand(t *testing.T, encoded string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data)%2 != 0 {
		t.Fatalf("invalid UTF-16 command: %v", err)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return string(utf16.Decode(units))
}

func TestLogonTaskPersistsAndRunsInInteractiveAdministratorSession(t *testing.T) {
	const exe = `C:\应用 & 工具\O'Brien\AppForce.exe`
	const dir = `C:\应用 & 工具\O'Brien`
	const data = `C:\用户\配置 & 数据\`
	const userID = "S-1-5-21-123-456-789-1001"
	var persisted string
	task := &scheduledTask{
		name: taskName(userID, data), userID: userID, executable: exe, directory: dir,
		args: []string{"--data", data, "--listen", "127.0.0.1:17892", "--tls-cert", `C:\证书\server.crt`, "--tls-key", `C:\证书\server.key`},
		run: func(ctx context.Context, encoded string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("system commands must have a timeout")
			}
			script := decodeCommand(t, encoded)
			switch {
			case strings.Contains(script, "Register-ScheduledTask"):
				match := regexp.MustCompile(`(?s) -Xml '((?:[^']|'')*)' -Force`).FindStringSubmatch(script)
				if len(match) != 2 {
					t.Fatal("registration did not pass an escaped XML literal")
				}
				persisted = strings.ReplaceAll(match[1], "''", "'")
			case strings.Contains(script, "Unregister-ScheduledTask"):
				persisted = ""
			default:
				if persisted == "" {
					return []byte("false\r\n"), nil
				}
				return []byte("true\r\n"), nil
			}
			return nil, nil
		},
	}
	if enabled, err := task.Enabled(); err != nil || enabled || persisted != "" {
		t.Fatal("reading the default choice created or enabled a task")
	}
	if err := task.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	var document struct {
		XMLName xml.Name
		Trigger struct {
			Enabled bool
			UserID  string `xml:"UserId"`
		} `xml:"Triggers>LogonTrigger"`
		Principal struct {
			UserID    string `xml:"UserId"`
			LogonType string
			RunLevel  string
		} `xml:"Principals>Principal"`
		Settings struct {
			Enabled                    bool
			MultipleInstancesPolicy    string
			DisallowStartIfOnBatteries bool
			StopIfGoingOnBatteries     bool
			StartWhenAvailable         bool
			ExecutionTimeLimit         string
		}
		Exec struct{ Command, Arguments, WorkingDirectory string } `xml:"Actions>Exec"`
	}
	if err := xml.Unmarshal([]byte(persisted), &document); err != nil {
		t.Fatal(err)
	}
	if document.XMLName.Space != "http://schemas.microsoft.com/windows/2004/02/mit/task" || !document.Trigger.Enabled || document.Trigger.UserID != userID || document.Principal.UserID != userID || document.Principal.LogonType != "InteractiveToken" || document.Principal.RunLevel != "HighestAvailable" {
		t.Fatalf("task cannot run elevated at the current user's interactive logon: %+v", document)
	}
	if !document.Settings.Enabled || document.Settings.MultipleInstancesPolicy != "IgnoreNew" || document.Settings.DisallowStartIfOnBatteries || document.Settings.StopIfGoingOnBatteries || !document.Settings.StartWhenAvailable || document.Settings.ExecutionTimeLimit != "PT0S" {
		t.Fatalf("task can be duplicated or stopped by power/time limits: %+v", document.Settings)
	}
	if document.Exec.Command != exe || document.Exec.WorkingDirectory != dir {
		t.Fatalf("Unicode, apostrophes, or XML characters corrupted the executable path: %+v", document.Exec)
	}
	wantArgs := `"--data" "C:\用户\配置 & 数据\\" "--listen" "127.0.0.1:17892" "--tls-cert" "C:\证书\server.crt" "--tls-key" "C:\证书\server.key"`
	if document.Exec.Arguments != wantArgs || strings.Contains(document.Exec.Arguments, "--autostart") {
		t.Fatalf("launch flags or trailing backslash corrupted: %q", document.Exec.Arguments)
	}
	// A fresh reader sees the OS-persisted choice, independent of in-memory status.
	reader := *task
	if enabled, err := reader.Enabled(); err != nil || !enabled {
		t.Fatal("a fresh reader did not retain the enabled choice")
	}
	for i := 0; i < 2; i++ {
		if err := task.SetEnabled(false); err != nil {
			t.Fatal(err)
		}
		if enabled, err := reader.Enabled(); err != nil || enabled {
			t.Fatal("disabling or repeatedly disabling did not remove the task")
		}
	}
}

func TestTaskNamesSeparateUsersAndConfigurationDirectories(t *testing.T) {
	base := taskName("user-a", `C:\用户\AppForce`)
	if base != taskName("user-a", `c:\用户\appforce`) {
		t.Fatal("Windows path casing created duplicate startup tasks")
	}
	if base == taskName("user-b", `C:\用户\AppForce`) || base == taskName("user-a", `C:\用户\Other`) {
		t.Fatal("startup tasks can overwrite another user or configuration")
	}
}

func TestTaskQueryAndRegistrationFailuresAreReported(t *testing.T) {
	task := &scheduledTask{run: func(context.Context, string) ([]byte, error) {
		return []byte("access denied"), errors.New("exit status 1")
	}}
	if enabled, err := task.Enabled(); err == nil || enabled {
		t.Fatal("a query error was presented as successful")
	}
	if err := task.SetEnabled(true); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatal("registration failure did not include the system error")
	}
	task.run = func(context.Context, string) ([]byte, error) { return []byte("unexpected"), nil }
	if _, err := task.Enabled(); err == nil {
		t.Fatal("unrecognized system status accepted")
	}
}

func TestWindowsArgumentQuoting(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", `""`},
		{`plain`, `"plain"`},
		{`C:\Program Files\AppForce\`, `"C:\Program Files\AppForce\\"`},
		{`a\"b`, `"a\\\"b"`},
	} {
		if got := quoteArgument(tc.input); got != tc.want {
			t.Errorf("quote %q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}
