# AppForce

一个应用，内置 sing-box，按程序路径选择需要代理的应用。Windows 使用原生桌面窗口，Linux/macOS 可使用同一程序提供的网页界面，也可由 Windows 统一管理。

这是首版实现。sing-box 通过 `box.New()` 直接嵌入；界面静态资源通过 `go:embed` 打包。运行时不下载或启动外部 sing-box。Windows 使用 sing-tun 内嵌的 Wintun，无需在可执行文件旁放置 DLL。

## 已实现

- 订阅 URL 下载、手动粘贴、更新和删除。
- Clash YAML、sing-box JSON、base64 URI 列表和逐行分享链接。
- Shadowsocks、VMess、VLESS、Trojan、Hysteria2、SOCKS、HTTP 节点；支持常见 TLS、Reality、WebSocket、gRPC 参数。转换范围见下文。
- 节点选择、通过真实代理出站发起 HTTPS 请求测延迟。
- 三平台直接浏览选择程序文件，无需先启动程序；支持按路径保存规则、运行中程序的辅助定位。
- TUN 透明代理，以及用于无网络权限调试的本地 SOCKS/HTTP 模式。
- 内核启停、真实连接列表、按应用实时速率和持久化历史流量、运行日志。
- Windows 托盘：右键菜单、本机代理启停、恢复窗口、关闭窗口后常驻。
- JSON 原子保存；订阅刷新保留稳定节点 ID；失败不覆盖旧订阅。
- 运行时配置变更失败时恢复旧配置。重新配置会关闭现有连接。
- 远程设备管理：每台机器执行自己的代理，由本机 API 转发管理请求；支持 HTTPS 和证书指纹固定。
- SSH 自动部署：输入 Linux/Mac 主机、用户名和密码，自动识别平台、上传内置 Agent、安装后台服务并加入设备列表。
- 设备 HTTPS 证书生成、Bearer 管理密钥、浏览器来源校验、敏感字段从状态接口移除。
- 第三方应用目录：提供 Cockpit Tools 的介绍、项目主页和最新版本下载入口。
- Antigravity 多账号管理：加密保存账号，本机或远程按用户切换，展示模型剩余额度、积分、重置时间及持久化 token 用量。

## 构建

在项目根目录执行 `make help`（或直接执行 `make`），查看全部命令、产物路径和 Windows/macOS/Linux 编译示例。构建命令会自动安装前端依赖并构建界面。

需要 Go 1.25.5 或更高版本、Node.js 22.12 或更高版本。Go 开启自动工具链下载时会按 `go.mod` 获取对应工具链。

```bash
npm --prefix frontend ci
npm --prefix frontend run build
go run ./build/agents
go build -trimpath -tags with_quic,with_utls,with_gvisor -o bin/appforce ./cmd/appforce
```

`with_quic` 支持 Hysteria2；`with_utls` 支持 TLS 指纹和 Reality 常用配置；`with_gvisor` 支持 TUN 的 gVisor 与混合网络栈。所有正式构建应保留这三个标记。

普通构建在启动时打开默认浏览器。网页资源、后台、代理内核都来自同一个可执行文件。

`make build`、`make desktop`、`make windows` 和 Windows 构建脚本会先构建并内置 Linux/macOS 的 amd64、arm64 Agent，上传时自动选择平台。部署包不需要远端联网下载，也不要求远端安装 Go 或 Node.js。`make agents` 可单独重新生成部署包；生成的压缩包和清单位于 `internal/agentbundle/payloads/`，不提交到 Git。直接使用 `go build` 而未先生成部署包时，仍能连接已有 Agent，但不能自动部署。

### Windows 桌面单应用

在 Windows 项目根目录的 PowerShell 中执行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1
```

产物为 `bin/AppForce.exe`。包含桌面窗口、管理员权限清单、后台、内核和界面资源。双击后由 Windows 请求管理员授权，便于创建 TUN。

启动后在通知区域显示 AppForce 图标。单击或双击恢复主窗口；右键菜单显示本机代理状态、当前节点和启用规则数，并提供“显示主窗口”“启动本机代理”“停止本机代理”“退出 AppForce”。启停处理中会禁用代理操作；失败时恢复窗口并显示错误。

关闭窗口会隐藏到托盘，本机代理继续运行。通过托盘“退出 AppForce”停止本机代理并退出。托盘始终操作本机，界面切换到远程设备后也一样。图标被 Windows 收入隐藏区域时，可从通知区域的展开按钮找到它。Explorer 重启后自动恢复图标；托盘初始化或恢复失败时显示错误并恢复窗口，关闭窗口将正常退出。

在本机“代理设置”中可开启“随 Windows 启动”，默认关闭，切换后立即保存。开启后，通过当前用户的 Windows 任务计划在每次登录时以管理员权限启动 AppForce；关闭后移除对应任务。该开关只启动应用，代理仍由“启动代理”控制。启动任务保留当前配置目录、监听地址和 HTTPS 参数；移动 EXE 后请重新关闭、开启此选项以更新路径。远程设备、Linux/macOS 不显示此开关。

桌面窗口依赖 Windows WebView2 Runtime。Windows 11 通常已安装；缺少该系统运行时的 Windows 10 需要先安装。应用本身为单个 EXE。

从 macOS/Linux 交叉编译 Windows amd64：

```bash
make windows
```

### Windows 发布

推送与应用版本一致的 `vX.Y.Z` 标签后，GitHub Actions 自动构建 x64 / ARM64 Windows 包，运行宿主机测试、静态检查和 Windows 编译检查，生成 SHA-256、对应源码及依赖许可，再发布到 GitHub Release。流程使用仓库自带的 `GITHUB_TOKEN`；已发布版本的资产不会覆盖。失败后可在 Actions 的 “Publish Windows release” 中选择已有标签重新运行。

若构建打包已通过、文件已上传草稿而发布中断，在 `main` 更新发布工作流可复用保留的构建产物完成发布。恢复任务核对版本标签、源码提交、原构建检查结果及本地和远端 SHA-256，仅发布对应草稿；构建产物保留 14 天。

本地已构建两个架构后，可执行 `python3 scripts/package-windows.py --version v0.1.1 --output output/release` 整理发布文件（Python >= 3.9）。输出目录必须不存在，`assets/` 包含两份 Windows ZIP、源码包和校验文件。打包脚本会核对 EXE 架构、清单、构建提交及内置资源，并收集对应 Go / 前端依赖源码和许可证。Windows 真机运行验证仍需单独完成。

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

1. 在“订阅与节点”导入订阅或分享链接，选择默认节点。
2. 在“程序代理”点击“添加程序”，再点“浏览选择程序”：Windows 选择 `.exe`，macOS 可直接选择 `.app`，Linux 选择可执行文件。无需先启动程序，也可直接填写绝对路径。
3. 点击“启动代理”。默认透明代理模式，匹配程序使用单独指定的节点，未指定时使用默认节点。
4. 在程序中建立新连接，从“实时连接”验证实际转发方式。

“实时连接”默认只显示通过代理转发的连接。打开“显示直连连接”可同时查看直连流量，开关状态会在当前浏览器或桌面窗口中保存。列表最多显示 200 条，优先保留代理连接，同类连接按建立时间从新到旧排列。

在“程序代理”中，可为每个程序单独选择节点；“跟随默认节点”使用“订阅与节点”当前选中的默认节点。新增程序默认跟随，开关规则会保留已选节点，重启后继续生效。切换默认节点不会改变程序单独指定的节点。指定节点被订阅更新或删除移除时不会自动回退，请重新选择或设为跟随默认节点；运行中的代理会拒绝导致启用规则节点失效的订阅变更。程序节点选择用于透明代理模式，本地 SOCKS/HTTP 入站仍使用默认节点。规则按路径匹配，不依赖 PID，程序或电脑重启后仍生效。macOS 的 `.app` 规则覆盖应用包内的主程序与辅助程序；Windows 选择 `Antigravity.exe` 或 `Antigravity IDE.exe` 时，自动包含该安装目录及其子目录内的 `language_server_windows_x64.exe`、`language_server_windows_arm64.exe` 和 `language_server.exe`，由同一个规则开关控制。已有 Antigravity 主程序规则升级后同样生效，无需重新添加；辅助程序可在应用启动后出现，无需提前运行或扫描文件。其他多进程应用需分别添加真正联网的辅助可执行文件。程序移动或重新安装到其他路径后，需要重新选择。

本机桌面版使用系统文件选择框；网页版和远程管理界面浏览目标设备上的程序目录。找不到程序时可填写目录后点“前往”，或使用“从运行中的程序快速定位（可选）”；同一路径的多个进程合并为一个程序。

节点连接失败时，匹配程序的请求失败，不自动回退直连。进程未能识别时默认直连，因此首版不提供严格的应用泄漏隔离保证。

局域网、回环、管理服务和代理应用自身直连。该规则优先于程序规则；需要代理到私网目标的场景尚未提供独立策略。

### 本地代理模式

在“代理设置”选择“本地代理 · SOCKS / HTTP”。此模式无需创建 TUN，不启用程序规则，需要应用主动使用 `127.0.0.1:17890`。

```bash
curl --proxy socks5h://127.0.0.1:17890 https://example.com
```

代理端口可以在界面中修改，仅监听回环地址。

### DNS 行为

默认选择“保留系统 DNS”，不为 TUN 网卡配置 DNS，也不替换其他程序的 DNS 查询目的地址。可为所选程序启用直连或通过节点的加密 DNS；自定义服务器必须支持 HTTPS 的 `/dns-query`，走代理时使用该程序单独指定的节点，未指定时使用默认节点；默认地址为 `8.8.8.8`，在国内网络使用该地址时建议选择“所选程序 · 加密 DNS 走代理”。

自定义 DNS 只接管能识别为已启用程序发出的 53 端口查询。Windows DNS Client 等共享系统服务代发的查询无法归属原程序，保持原目的地址直连；应用自带 DoH 按该应用的普通连接规则处理。节点服务器域名使用独立的系统 DNS 解析，不依赖代理节点。升级前保存的直连或代理 DNS 设置仍保留，但其作用范围收窄到所选程序。

TUN 模式仍会调整系统路由：未选程序的部分流量可能先进入 TUN，再由内核直连，因此仍可能影响 VPN、网卡绑定、已有连接及部分网络协议。“严格路由”默认关闭，遇到其他程序无法联网时应保持关闭。需要完全保留其他程序的网络路径时，使用“本地代理 · SOCKS / HTTP”，并在 Antigravity 等目标程序中单独配置 `127.0.0.1:17890`。Windows 实际 DNS、IPv6、VPN 共存和换网恢复仍需目标机器验证。

在“代理设置”的透明代理模式下，可选择“TUN 网络栈”：默认 gVisor 在应用内处理 TCP/UDP；Mixed 使用系统 TCP 栈与 gVisor UDP 栈；System 使用系统网络栈。三平台默认均为 gVisor，升级前未保存该选项的配置也默认使用 gVisor。选择后点击“保存设置”，选项按设备持久保存；运行中的代理会重启并关闭现有连接，应用失败时恢复旧配置。本地代理模式保留该选项，但不使用 TUN 网络栈。启动后可在“运行日志”查看实际网络栈。

gVisor 可绕开 Windows 系统栈将 TCP 改写到本机监听端口的路径；使用 Mixed 或 System 时需允许 AppForce 通过 Windows 防火墙。钉钉等未选程序仍按原规则直连；若仅在开启 TUN 时无法加载图片，可选用 gVisor，保存后完全退出、重新打开该程序。此调整仍需 Windows 实测；若问题持续，可暂时使用本地代理模式，并收集图片加载时的连接与日志。

连接/流量来自内核真实追踪。程序代理页底部的累计字节为本次内核运行的代理与直连总量，重启内核后归零；活动列表最多显示 200 条连接，每 3 秒更新；“实时连接”显示内核实际使用的节点名称、协议与服务器地址，直连连接不显示代理节点。

“流量统计”提供各应用实时上传/下载速率、活跃连接、今天或近 7/30/90 天的用量及历史累计。“今天”按小时显示，从当天 08:00 开始；若有更早的流量，则从最早流量所在小时开始，连续显示到当前小时（08:00 前无流量时从当前小时开始），空闲小时显示为 0。近 7/30/90 天仍按天显示。点击应用名称查看该应用的趋势，支持按名称或路径搜索。默认只统计代理流量，可打开“包含内核处理的直连流量”；未经过 AppForce 内核的系统流量不在统计范围内。本地代理模式无法识别具体程序时，归入“本地代理（未识别应用）”。

流量直接从 TCP、UDP 和 TUN 流的计数器记录，短连接关闭后仍保留，不受连接列表条数限制。应用以路径为稳定标识，进程重启或删除后重新添加同路径规则不清空历史。Antigravity 的语言服务按主程序规则合并，macOS 应用包内的辅助程序归入所属应用；移除程序规则也会保留已有历史。

历史按记录设备的本地日期和小时汇总，保存在配置目录的 `traffic.db`（Windows 为 `%APPDATA%\AppForce\traffic.db`），每 5 秒自动保存，正常停止或切换配置时立即保存。保存失败会在统计页提示并保留内存计数以便重试，代理转发继续运行。旧版每日累计继续保留，但无法还原具体小时，统计页会提示这部分用量；小时记录从升级后开始。进程被强制终止或断电时，最后一次保存后的少量数据可能丢失。每台远程设备独立保存历史，切换设备可查看对应统计。

## 从 Windows 管理 Linux / Mac

推荐在 Windows 的“设备管理 → 添加设备 → SSH 自动部署”中输入设备名称、IP/主机名、SSH 端口（默认 22）、用户名和密码，点击“部署并启动”。高级设置可填写不同的 sudo 密码和 Agent 管理端口（默认 17891）。程序自动完成平台识别、SHA-256 上传校验、管理员提权、证书生成、后台服务安装、HTTPS 连接验证和设备添加，界面显示各阶段进度。成功后自动切换到新设备，再导入节点、添加远端应用并启动代理；新安装不会在未配置节点时启用全局代理。

远端须已开启 SSH 并允许密码登录。Linux 需要正在运行的 systemd、`/dev/net/tun` 和 root 或 sudo 账号；macOS 需要启用“系统设置 → 通用 → 共享 → 远程登录”，并使用允许远程登录的管理员账号。SSH 检查到的主机指纹用于后续部署连接的验证，指纹变化会中止部署。密码只用于本次 SSH/sudo 操作，不写入配置、设备列表或部署进度；安装后管理使用 Agent 密钥和 HTTPS 证书指纹。

自动部署的管理服务监听 HTTPS，防火墙或云安全组需允许管理端口从 Windows 所在网络访问。SSH 登录成功而管理端口不通时，页面会提示 Agent 已安装但无法连接，并显示管理地址。程序不会自动修改远端防火墙。

Linux 安装到 `/opt/appforce/bin/appforce`，配置保存在 `/var/lib/appforce`，后台服务为 `appforce-agent.service`；Mac 安装到 `/usr/local/lib/appforce/appforce`，配置保存在 `/Library/Application Support/AppForce`，后台服务为 `com.appforce.agent`。均配置为开机运行。重复部署保留配置、管理密钥和历史流量，并更新已有的同地址设备；安装或本机启动验证失败时恢复旧二进制和服务文件。已有手动运行的 Linux `appforce.service` 会阻止自动安装，避免两套服务冲突，可选择“连接已有 Agent”。

“移除设备”只删除 Windows 上的管理连接，远端 Agent 继续运行。部署任务进度仅保存在当前管理程序内存中，关闭整个程序会中断尚未完成的部署；已成功启动的远端后台服务不受影响。

也可选择“连接已有 Agent”，通过以下两种方式管理已安装的设备。

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

## Antigravity 账号与用量

侧栏打开“Antigravity”。此页面专门管理 Antigravity / agy 的账号和用量。账号列表保存在当前管理端，应用目标跟随左上角选择的设备。账号加密保存在配置目录的 `antigravity/accounts.enc`；备份时同时保留 `accounts.key`，缺少密钥时不会生成新密钥覆盖原账号。

推荐点击“添加账号”→“使用 Google 授权登录”，在自动打开的浏览器中选择 Google 账号并同意授权；返回 AppForce 后会自动保存账号、选择新账号并查询额度。无需复制 Token，也无需创建自己的 OAuth 客户端。授权窗口支持重新打开网页、复制链接、取消和失败重试；关闭添加窗口后可从页面查看仍在等待的授权进度。切换设备或离开页面会取消仍在等待浏览器的授权；已经开始保存的授权会继续完成。账号添加与应用到设备是两个独立操作，授权完成后按需点击“应用到”。

授权前需启动管理端的代理。授权网页、Token 交换、身份验证、自动续期和额度查询使用已启用的 Antigravity 程序规则指定的节点；没有单独指定时使用默认节点。代理未启动时会提示先启动，不会自动直连。多个 Antigravity 安装配置了不同节点时需先统一节点。授权会使用本机 Edge / Chrome 打开独立的临时无痕窗口，只为该窗口设置代理；本机 OAuth 回调保持直连，现有浏览器和系统代理设置不变。未安装支持的浏览器时会提示安装，也可复制链接到已使用相同代理的浏览器完成授权。授权成功后显示“添加完成”，点击“完成”关闭窗口。

“备用导入”保留从 Antigravity / agy 读取当前登录、导入单个账号凭据 JSON 的方式。自定义 OAuth 客户端放在页面下方的高级设置中。自动续期保留各账号原登录对应的客户端配置，不用其他账号的新配置覆盖它。

`internal/antigravity/oauth_defaults.go` 提供公开的 Antigravity 桌面 OAuth 客户端配置，供默认浏览器授权、原生账号导入和续期使用。要保留上述直接授权体验，需保留这组配置或提供等效的安装配置；仅删除该文件会导致编译失败，移除配置则需要用户自行设置 OAuth 客户端。此文件不存储用户密码或个人账号 Token。其公开来源和许可见第三方声明。

Windows 选择“Antigravity 2.x / agy”会写入当前 Windows 用户的凭据管理器。macOS 使用该用户的登录钥匙串，Linux 使用该用户的 Secret Service 密钥环。旧版 IDE 选择“旧版 Antigravity IDE”，操作其已有 `state.vscdb`，切号前需要关闭 IDE。所有切换均读取原凭据并验证新凭据；失败时尝试恢复，只有验证和保存成功后才显示已应用。已运行的桌面应用或 CLI 会话可能保留旧账号，切换后重启客户端或创建新会话。

从 Windows 管理 Linux 时，先在设备管理中部署/更新 Agent，再切到该设备，填写实际运行 `agy` 的 Linux 用户名。root Agent 通过当前二进制中的用户组件执行操作，不把凭据写进 root 的密钥环。系统密钥环模式需要 `libsecret-tools`、目标用户的 D-Bus 会话及已解锁的 Secret Service。

纯 SSH、无桌面的 Linux 可选择“无桌面 Linux · appforce-agy”。目标机器需要已有 `agy`，以及 `dbus-run-session`、`secret-tool`、`gnome-keyring-daemon`。Debian/Ubuntu 可安装依赖：

```bash
sudo apt-get install dbus libsecret-tools gnome-keyring
```

点击账号的“应用到”后，会在指定用户下部署 `~/.local/bin/appforce-agy`。它在独立 D-Bus 和临时密钥环中加载账号，避免依赖官方 CLI 尚有兼容问题的文件凭据回退。随后用以下入口启动（参数原样传给 `agy`）：

```bash
~/.local/bin/appforce-agy
~/.local/bin/appforce-agy --print "你的任务"
```

该入口的凭据文件在目标用户的 `~/.config/AppForce/antigravity-cli/`，仅供该用户读取。直接启动 `agy` 仍使用普通系统登录。部署时校验独立密钥环的实际读写；目标系统没有依赖或密钥环无法运行时，会明确报错。真实 `agy` 版本的登录、续期和会话行为仍需要目标机器验证。

用量展示包括模型组/模型剩余百分比、重置时间、积分，以及 token 消耗。额度查询通过当前管理端的代理发出；远端应用账号时也需要远端能连接 Google。数据来自账号服务，不将剩余额度百分比转换为 token。

Token 采集读取所选用户的以下会话日志，每个会话优先使用 `transcript.jsonl`，不存在时使用 `transcript_full.jsonl`：

- `~/.gemini/antigravity-cli/brain/*/.system_generated/logs/`
- `~/.gemini/antigravity/brain/*/.system_generated/logs/`

页面每 15 秒读取，已经通过本工具管理的目标在后台每 30 秒采集。仅保存响应 ID、时间、模型、账号归属和 token 计数，不保存提示词和回复正文。按设备/用户、账号和模型查看今天、近 7/30/90 天及累计用量；今天按小时展示，从 08:00 或更早的用量记录开始。输入、输出、缓存读取、思考计数分开展示，报告总数遵循客户端字段，不再次叠加已包含的缓存或思考。重复记录、进行中的流式记录和会话累计结果不会重复计入。

缺少 token 或时间的日志不推算消耗，页面会提示无法识别。没有日志账号时按 AppForce 记录的切号时段归属，开始管理前的记录为“未归属”；客户端内自行切号后需重新导入确认。未退出的旧会话仍可能使用原账号，这类日志的账号归属不能仅凭系统凭据完全确认。历史覆盖已采集的记录，不代表官方完整账单。客户端日志/服务接口变更后可能需要更新适配。

也可选择实际账号后导入 JSON 数组或 JSONL 的逐次用量记录。标准格式如下，`id` 必须是稳定、唯一的响应 ID；省略 `total_tokens` 时按输入和输出求和，已有官方总数时应保留它：

```json
{"id":"response-001","time":"2026-10-14T10:00:00+08:00","model":"gemini-model","input_tokens":1000,"output_tokens":200,"cached_tokens":800,"thinking_tokens":100,"total_tokens":1200}
```

账号存储、适配器、管理界面和统计为本项目实现，没有合并 Cockpit Tools 源码。公开 OAuth 配置的来源和 MIT 许可见 [第三方声明](docs/third-party-notices.md)。

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

测试包括：嵌入内核真实 HTTP → SOCKS 转发与统计、虚拟 TUN 下未选程序的 IPv4/IPv6 大文件 HTTPS 下载与直连统计、重启、进程路由配置、订阅格式与边界、密钥鉴权、来源检查、远程 HTTPS 指纹、持久化、订阅更新失败保留旧数据，以及托盘操作串行、错误重试、退出等待和状态同步。测试不创建真实 TUN，也不修改宿主系统路由。

当前已在 macOS 开发环境通过本地转发和浏览器流程验证，并完成 Windows/Linux 编译检查。Windows 托盘鼠标/键盘交互、关闭窗口后恢复和 Explorer 重启恢复，以及 Windows/Linux/macOS 的管理员 TUN、UDP/IPv6、DNS、VPN 共存与异常退出恢复仍需目标机器实测，不能以交叉编译替代运行验证。

## 项目结构

```text
cmd/appforce/          统一入口、桌面窗口、网页模式
internal/engine/       嵌入式 sing-box、配置生成、连接和统计
internal/subscription/ 多格式订阅转换
internal/process/      三平台进程发现
internal/program/      三平台程序目录浏览
internal/app/          配置事务、订阅、规则、远程管理
internal/api/          带鉴权的管理 API
internal/store/        原子 JSON 保存与密钥
internal/antigravity/  账号加密存储、OAuth、跨用户凭据适配与 token 采集
internal/security/     HTTPS 设备证书
internal/tray/         本机托盘控制、Windows 原生通知图标和消息循环
internal/web/          嵌入的界面构建产物
frontend/              Vue 3 + TypeScript
harmonyos/             原生鸿蒙手机版、VPN 扩展与 Go 移动内核
build/windows/         Windows 图标、权限和 DPI 清单生成
deploy/                常驻服务模板
```

原生鸿蒙 5 / 6 / NEXT 手机版在同一仓库的 `harmonyos/` 目录维护。已实现 ArkUI 界面、VPN 扩展、N-API 桥接和 Go 移动内核代码，并验证宿主机转发与桥接接口；鸿蒙 Go 工具链、DevEco 构建及真机验证尚未完成。构建前提和操作步骤见 [鸿蒙工程说明](harmonyos/README.md)，接口限制见 [鸿蒙移植评估](docs/harmonyos-port.md)。

项目按 GPL-3.0-or-later 分发。sing-box 另有名称与关联声明限制，第三方许可说明见 [NOTICE.md](NOTICE.md)。
