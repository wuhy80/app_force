# AppForce

一个应用，内置 sing-box，按程序路径选择需要代理的应用。Windows 使用原生桌面窗口，Linux/macOS 可使用同一程序提供的网页界面，也可由 Windows 统一管理。

这是首版实现。sing-box 通过 `box.New()` 直接嵌入；界面静态资源通过 `go:embed` 打包。运行时不下载或启动外部 sing-box。Windows 使用 sing-tun 内嵌的 Wintun，无需在可执行文件旁放置 DLL。

## 已实现

- 订阅 URL 下载、手动粘贴、更新和删除。
- Clash YAML、sing-box JSON、base64 URI 列表和逐行分享链接。
- Shadowsocks、VMess、VLESS、Trojan、Hysteria2、SOCKS、HTTP 节点；支持常见 TLS、Reality、WebSocket、gRPC 参数。转换范围见下文。
- 节点选择、通过真实代理出站发起 HTTPS 请求测延迟。
- 三平台运行中程序发现、搜索、按可执行文件绝对路径保存规则。
- TUN 透明代理，以及用于无网络权限调试的本地 SOCKS/HTTP 模式。
- 内核启停、真实连接列表、内核流量统计、运行日志。
- Windows 托盘：右键菜单、本机代理启停、恢复窗口、关闭窗口后常驻。
- JSON 原子保存；订阅刷新保留稳定节点 ID；失败不覆盖旧订阅。
- 运行时配置变更失败时恢复旧配置。重新配置会关闭现有连接。
- 远程设备管理：每台机器执行自己的代理，由本机 API 转发管理请求；支持 HTTPS 和证书指纹固定。
- 设备 HTTPS 证书生成、Bearer 管理密钥、浏览器来源校验、敏感字段从状态接口移除。

## 构建

需要 Go 1.25.5 或更高版本、Node.js 22.12 或更高版本。Go 开启自动工具链下载时会按 `go.mod` 获取对应工具链。

```bash
npm --prefix frontend ci
npm --prefix frontend run build
go build -trimpath -tags with_quic,with_utls -o bin/appforce ./cmd/appforce
```

`with_quic` 支持 Hysteria2；`with_utls` 支持 TLS 指纹和 Reality 常用配置。所有正式构建应保留这两个标记。

普通构建在启动时打开默认浏览器。网页资源、后台、代理内核都来自同一个可执行文件。

### Windows 桌面单应用

在 Windows 项目根目录的 PowerShell 中执行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1
```

产物为 `bin/AppForce.exe`。包含桌面窗口、管理员权限清单、后台、内核和界面资源。双击后由 Windows 请求管理员授权，便于创建 TUN。

启动后在通知区域显示 AppForce 图标。单击或双击恢复主窗口；右键菜单显示本机代理状态、当前节点和启用规则数，并提供“显示主窗口”“启动本机代理”“停止本机代理”“退出 AppForce”。启停处理中会禁用代理操作；失败时恢复窗口并显示错误。

关闭窗口会隐藏到托盘，本机代理继续运行。通过托盘“退出 AppForce”停止本机代理并退出。托盘始终操作本机，界面切换到远程设备后也一样。图标被 Windows 收入隐藏区域时，可从通知区域的展开按钮找到它。Explorer 重启后自动恢复图标；托盘初始化或恢复失败时显示错误并恢复窗口，关闭窗口将正常退出。

桌面窗口依赖 Windows WebView2 Runtime。Windows 11 通常已安装；缺少该系统运行时的 Windows 10 需要先安装。应用本身为单个 EXE。

从 macOS/Linux 交叉编译 Windows amd64：

```bash
make windows
```

### Linux / macOS

```bash
make build
./bin/appforce
```

默认管理地址为 `http://127.0.0.1:17891`。自动打开的浏览器通过 URL fragment 接收密钥，随后移除 fragment；密钥不会出现在 HTTP URL 或后台日志中。

TUN 需要 root 或足够的网络权限。建议用后台方式启动，再用普通用户浏览器管理：

```bash
# Linux
sudo ./bin/appforce --headless --data /var/lib/appforce

# macOS
sudo ./bin/appforce --headless --data '/Library/Application Support/AppForce'
```

手动访问管理地址，输入上述配置目录中 `token` 文件的内容。也可执行 `appforce --data <相同目录> --show-token` 查看密钥。

macOS 可选原生窗口构建：

```bash
make desktop
```

Linux 原生窗口需要 GTK/WebKit 开发依赖；首版推荐普通构建和网页管理。

## 使用

1. 在“订阅与节点”导入订阅或分享链接，选择节点。
2. 在“程序代理”添加运行中的程序，或填写可执行文件绝对路径。
3. 点击“启动代理”。默认透明代理模式，仅匹配程序使用当前节点。
4. 在程序中建立新连接，从“实时连接”验证实际转发方式。

所有所选程序共用当前节点。规则按路径匹配，不依赖 PID，程序重启后仍生效。需要分别添加浏览器、Electron 等应用中真正联网的辅助可执行文件；首版不自动扩展程序树。

节点连接失败时，匹配程序的请求失败，不自动回退直连。进程未能识别时默认直连，因此首版不提供严格的应用泄漏隔离保证。

局域网、回环、管理服务和代理应用自身直连。该规则优先于程序规则；需要代理到私网目标的场景尚未提供独立策略。

### 本地代理模式

在“代理设置”选择“本地代理 · SOCKS / HTTP”。此模式无需创建 TUN，不启用程序规则，需要应用主动使用 `127.0.0.1:17890`。

```bash
curl --proxy socks5h://127.0.0.1:17890 https://example.com
```

代理端口可以在界面中修改，仅监听回环地址。

### DNS 行为

透明代理会调整系统路由，并接管进入 TUN 的常规 DNS 查询。默认使用直连加密 DNS；可切换为通过节点查询。DNS 服务器必须支持 HTTPS 的 `/dns-query`，默认 `1.1.1.1`。

DNS 设置可能影响未选程序。节点服务器域名使用独立直连 UDP DNS 引导解析，避免依赖系统 DNS 而形成循环。应用自带 DoH、系统 DNS 代发、已有连接、IPv6、VPN 共存和换网恢复均需要在实际目标系统上验证。

连接/流量来自内核真实追踪。累计字节包含内核处理的代理和直连流量；活动列表最多显示 200 条连接，每 3 秒更新。重启内核后统计归零。

## 从 Windows 管理 Linux / Mac

远端运行相同的 AppForce。配置在远端本地保存；关闭 Windows 管理窗口不会停止远端服务。

方式一：SSH 隧道。远端保持默认回环监听：

```bash
ssh -N -L 17892:127.0.0.1:17891 user@remote-host
```

在“设备管理”添加 `http://127.0.0.1:17892`，并填写远端管理密钥。仅回环地址允许明文 HTTP。

方式二：远端 HTTPS：

```bash
# 在远端使用同一个配置目录生成一次证书，命令输出证书 SHA-256 指纹
sudo appforce --data /var/lib/appforce --init-tls

sudo appforce --headless --data /var/lib/appforce \
  --listen 0.0.0.0:17891 \
  --tls-cert /var/lib/appforce/server.crt \
  --tls-key /var/lib/appforce/server.key
```

在 Windows 添加 `https://远端地址:17891`、远端管理密钥和证书指纹。自签名证书采用完整证书 SHA-256 固定，并检查有效期。使用受信任 CA 签发的证书时可不填写指纹。

每台设备订阅、节点和程序规则独立。当前没有中心同步，也不会让远端流量经过 Windows。切换左上角设备后，订阅导入、程序选择、启停和日志操作作用于该设备。

## 常驻运行

- Linux：提供 [deploy/appforce.service](deploy/appforce.service)。将可执行文件放到 `/usr/local/bin/appforce` 后，安装到 `/etc/systemd/system/`。模板使用回环管理，可通过 SSH 隧道管理。
- macOS：提供 [deploy/com.appforce.agent.plist](deploy/com.appforce.agent.plist)，可安装到 `/Library/LaunchDaemons/`。模板使用 `/Library/Application Support/AppForce` 保存配置。
- Windows：桌面窗口关闭后在托盘中常驻；选择托盘“退出 AppForce”停止本机代理。`--headless` 不创建窗口或托盘，可用任务计划程序启动 `--headless --autostart`。尚未提供原生 SCM 服务或自动安装器。

`--autostart` 尝试启动已保存配置；失败时保留管理接口，可从界面修正。没有节点或程序时不会自动启用全局代理。

## 订阅转换范围

导入的节点经过 sing-box 构造校验。Clash 插件、规则组、远程 provider、SSR、XHTTP、复杂 HTTP/H2 配置及 Hysteria2 URI 端口跳跃暂不转换。不支持的字段/节点给出导入提示，支持的节点仍可导入。

sing-box JSON 仅导入独立代理 outbound，忽略 direct、selector、urltest 等非节点条目；本地入站、路由和 DNS 由 AppForce 管理。不接受引用其他 outbound、网卡、命名空间或本地证书文件的节点。

订阅内容上限 8 MiB，单次最多 2000 个节点。订阅 URL 和节点认证信息保存在配置目录，状态接口不返回这些信息。

## 验证

```bash
make test
make check
```

测试包括：嵌入内核真实 HTTP → SOCKS 转发与统计、重启、进程路由配置、订阅格式与边界、密钥鉴权、来源检查、远程 HTTPS 指纹、持久化、订阅更新失败保留旧数据，以及托盘操作串行、错误重试、退出等待和状态同步。测试不创建真实 TUN，也不修改宿主系统路由。

当前已在 macOS 开发环境通过本地转发和浏览器流程验证，并完成 Windows/Linux 编译检查。Windows 托盘鼠标/键盘交互、关闭窗口后恢复和 Explorer 重启恢复，以及 Windows/Linux/macOS 的管理员 TUN、UDP/IPv6、DNS、VPN 共存与异常退出恢复仍需目标机器实测，不能以交叉编译替代运行验证。

## 项目结构

```text
cmd/appforce/          统一入口、桌面窗口、网页模式
internal/engine/       嵌入式 sing-box、配置生成、连接和统计
internal/subscription/ 多格式订阅转换
internal/process/      三平台进程发现
internal/app/          配置事务、订阅、规则、远程管理
internal/api/          带鉴权的管理 API
internal/store/        原子 JSON 保存与密钥
internal/security/     HTTPS 设备证书
internal/tray/         本机托盘控制、Windows 原生通知图标和消息循环
internal/web/          嵌入的界面构建产物
frontend/              Vue 3 + TypeScript
build/windows/         Windows 图标、权限和 DPI 清单生成
deploy/                常驻服务模板
```

项目按 GPL-3.0-or-later 分发。sing-box 另有名称与关联声明限制，第三方许可说明见 [NOTICE.md](NOTICE.md)。
