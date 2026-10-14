package startup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

type scheduledTask struct {
	name, userID, executable, directory string
	args                                []string
	run                                 func(context.Context, string) ([]byte, error)
}

func taskName(userID, dataDir string) string {
	// Separate users and configuration directories must not overwrite each other.
	digest := sha256.Sum256([]byte(userID + "\x00" + strings.ToLower(dataDir)))
	return fmt.Sprintf("AppForce-Startup-%x", digest[:12])
}

func psString(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func encodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func (t *scheduledTask) command(script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// No shell receives executable paths or flags. PowerShell literals are escaped,
	// and its encoded input preserves Chinese and other Unicode paths.
	out, err := t.run(ctx, encodedCommand("$ErrorActionPreference = 'Stop'; [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); "+script))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Windows 开机自动启动设置超时: %w", ctx.Err())
		}
		return nil, fmt.Errorf("Windows 任务计划操作失败（请以管理员身份运行 AppForce）: %w; %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (t *scheduledTask) lookupScript() string {
	// Enumerating avoids a localized 'not found' error for a missing task or an
	// empty root folder on a fresh Windows installation.
	return `$task = Get-ScheduledTask | Where-Object { $_.TaskPath -eq '\' -and $_.TaskName -eq ` + psString(t.name) + " }; "
}

func (t *scheduledTask) Enabled() (bool, error) {
	out, err := t.command(t.lookupScript() + "if ($task -and $task.Settings.Enabled) { 'true' } else { 'false' }")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("无法读取 Windows 开机自动启动状态")
	}
}

func (t *scheduledTask) SetEnabled(enabled bool) error {
	var script string
	if enabled {
		script = `Register-ScheduledTask -TaskPath '\' -TaskName ` + psString(t.name) + " -Xml " + psString(t.xml()) + " -Force | Out-Null"
	} else {
		// Removing an absent task is successful, regardless of the system language.
		script = t.lookupScript() + "if ($task) { Unregister-ScheduledTask -InputObject $task -Confirm:$false }"
	}
	_, err := t.command(script)
	return err
}

func xmlText(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

// quoteArgument follows the Windows CommandLineToArgvW backslash/quote rules.
// Always quoting also protects whitespace and empty arguments.
func quoteArgument(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	slashes := 0
	for _, c := range value {
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			out.WriteString(strings.Repeat("\\", 2*slashes+1))
		} else {
			out.WriteString(strings.Repeat("\\", slashes))
		}
		slashes = 0
		out.WriteRune(c)
	}
	out.WriteString(strings.Repeat("\\", 2*slashes))
	out.WriteByte('"')
	return out.String()
}

func (t *scheduledTask) xml() string {
	args := make([]string, len(t.args))
	for i, arg := range t.args {
		args[i] = quoteArgument(arg)
	}
	// InteractiveToken keeps the window and tray in the logged-in user's session.
	// HighestAvailable is required by AppForce's administrator manifest and TUN.
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>AppForce 随 Windows 登录自动启动</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="User"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions Context="User"><Exec><Command>%s</Command><Arguments>%s</Arguments><WorkingDirectory>%s</WorkingDirectory></Exec></Actions>
</Task>`, xmlText(t.userID), xmlText(t.userID), xmlText(t.executable), xmlText(strings.Join(args, " ")), xmlText(t.directory))
}
