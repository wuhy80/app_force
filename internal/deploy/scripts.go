package deploy

import (
	"fmt"
)

func paths(goos string) (binary, data, service string) {
	if goos == "darwin" {
		return "/usr/local/lib/appforce/appforce", "/Library/Application Support/AppForce", "/Library/LaunchDaemons/com.appforce.agent.plist"
	}
	return "/opt/appforce/bin/appforce", "/var/lib/appforce", "/etc/systemd/system/appforce-agent.service"
}

func preflight(goos string, port int) string {
	base := "set -eu\n[ \"$(id -u)\" = 0 ] || { echo '需要 root 或 sudo 管理员权限'; exit 1; }\n"
	if goos == "linux" {
		return base + `command -v systemctl >/dev/null || { echo 'Linux 需要 systemd 服务管理器'; exit 1; }
command -v sha256sum >/dev/null || { echo '缺少 sha256sum'; exit 1; }
[ -d /run/systemd/system ] || { echo 'systemd 未运行，暂不支持此容器或发行版'; exit 1; }
[ -c /dev/net/tun ] || { echo '缺少 /dev/net/tun，无法运行透明代理'; exit 1; }
if systemctl is-active --quiet appforce.service; then echo '已存在手动运行的 appforce.service，请先停止旧服务或使用连接已有 Agent'; exit 1; fi
`
	}
	return base + `command -v launchctl >/dev/null || { echo '缺少 launchctl'; exit 1; }
command -v shasum >/dev/null || { echo '缺少 shasum'; exit 1; }
`
}

func installation(goos, temp, hash string, port int) string {
	binary, data, service := paths(goos)
	digest := "sha256sum " + quote(temp+"/appforce")
	stop, start := "systemctl stop appforce-agent.service", "systemctl daemon-reload; systemctl enable appforce-agent.service; systemctl start appforce-agent.service"
	if goos == "darwin" {
		digest = "shasum -a 256 " + quote(temp+"/appforce")
		stop = "launchctl bootout system/com.appforce.agent"
		start = "launchctl enable system/com.appforce.agent; launchctl bootstrap system " + quote(service)
	}
	// Roll back the prior binary and service if installation/start fails. Configuration is retained.
	script := "set -eu\numask 077\n"
	script += "[ \"$(" + digest + " | awk '{print $1}')\" = " + quote(hash) + " ] || { echo '远端 Agent 校验失败'; exit 1; }\n"
	script += "mkdir -p " + quote(dir(binary)) + " " + quote(data) + "\nchmod 700 " + quote(data) + "\n"
	script += "backup=" + quote(temp+"/previous") + "\nmkdir -p \"$backup\"\n"
	script += "[ ! -f " + quote(binary) + " ] || cp " + quote(binary) + " \"$backup/appforce\"\n"
	script += "[ ! -f " + quote(service) + " ] || cp " + quote(service) + " \"$backup/service\"\n"
	script += "rollback() {\n  " + stop + " >/dev/null 2>&1 || true\n"
	script += "  if [ -f \"$backup/appforce\" ]; then cp \"$backup/appforce\" " + quote(binary) + "; else rm -f " + quote(binary) + "; fi\n"
	script += "  if [ -f \"$backup/service\" ]; then cp \"$backup/service\" " + quote(service) + "; " + start + " >/dev/null 2>&1 || true; else rm -f " + quote(service) + "; fi\n}\n"
	script += "trap 'rollback' EXIT\ntrap 'exit 1' HUP INT TERM\n" + stop + " >/dev/null 2>&1 || true\n"
	script += "cp " + quote(temp+"/appforce") + " " + quote(binary+".new") + "\nchmod 755 " + quote(binary+".new") + "\nchown root " + quote(binary+".new") + "\nmv -f " + quote(binary+".new") + " " + quote(binary) + "\n"
	if goos == "darwin" {
		// Ad-hoc sign uploaded Apple Silicon binaries before launchd starts them.
		script += "if command -v codesign >/dev/null; then codesign --force --sign - " + quote(binary) + "; fi\n"
	}
	script += quote(binary) + " --data " + quote(data) + " --pairing-info >/dev/null\n"
	script += "cat > " + quote(service) + " <<'APPFORCE_SERVICE'\n" + serviceContents(goos, binary, data, port) + "\nAPPFORCE_SERVICE\n"
	script += "chmod 644 " + quote(service) + "\nchown root " + quote(service) + "\n" + start + "\n"
	if goos == "linux" {
		script += "systemctl is-active --quiet appforce-agent.service\n"
	}
	script += quote(binary) + " --data " + quote(data) + " --verify-agent --listen " + quote(fmt.Sprintf("127.0.0.1:%d", port)) + " >/dev/null\n"
	script += "trap - EXIT\necho 'Agent 后台服务已启动'\n"
	return script
}

func serviceContents(goos, binary, data string, port int) string {
	if goos == "darwin" {
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.appforce.agent</string>
<key>ProgramArguments</key><array>
<string>%s</string><string>--headless</string><string>--autostart</string>
<string>--data</string><string>%s</string>
<string>--listen</string><string>0.0.0.0:%d</string>
<string>--tls-cert</string><string>%s/server.crt</string>
<string>--tls-key</string><string>%s/server.key</string>
</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>5</integer>
<key>StandardOutPath</key><string>/var/log/appforce.log</string>
<key>StandardErrorPath</key><string>/var/log/appforce.log</string>
</dict></plist>`, binary, data, port, data, data)
	}
	return fmt.Sprintf(`[Unit]
Description=AppForce remotely deployed agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
UMask=0077
ExecStart=%s --headless --autostart --data %s --listen 0.0.0.0:%d --tls-cert %s/server.crt --tls-key %s/server.key
Restart=on-failure
RestartSec=3
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target`, binary, data, port, data, data)
}

func credentials(goos string) string {
	binary, data, _ := paths(goos)
	return quote(binary) + " --data " + quote(data) + " --pairing-info"
}

func dir(value string) string {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == '/' {
			return value[:i]
		}
	}
	return "."
}
