# AppForce 原生鸿蒙手机版移植评估

评估日期：2026-10-14。目标为用户确认的 HarmonyOS 5 / 6 / NEXT 手机。

本文保留初始接口与移植评估。后续已在 `harmonyos/` 中实现界面、VPN 扩展、原生桥接和移动核心代码；当前验证范围、尚未解决的鸿蒙 Go 工具链前提及构建步骤以 [鸿蒙工程说明](../harmonyos/README.md) 为准。

## 结论

有可行的移植路线，但当前项目还不能构建成可在原生鸿蒙手机安装、运行的应用。需要新增鸿蒙应用工程、VPN 平台适配和原生内核桥接，先完成真机转发验证，再开发完整功能。

建议采用 ArkTS / ArkUI 手机界面、VpnExtensionAbility 和 N-API / C ABI 桥接的 sing-box 共享库。现有 Go 订阅解析、节点模型和部分业务逻辑有复用价值；复用的前提是内核及依赖通过鸿蒙工具链和运行验证。

本次完成的是代码与接口评估，没有生成鸿蒙应用或安装包，也没有完成真机 VPN 验证。

## 当前项目与迁移范围

| 内容 | 当前实现 | 鸿蒙处理方式 |
| --- | --- | --- |
| 界面 | Vue 3 / TypeScript；桌面窗口使用 Wails | 建议新增 ArkUI 手机界面；ArkWeb 可作为复用 Vue 的备选，但仍需原生 VPN 和内核桥接 |
| 代理内核 | Go 内嵌 sing-box 1.14.3，调用 `box.New()` | 编译为进程内原生共享库，适配鸿蒙 ABI、运行时和平台接口 |
| TUN | sing-tun 创建桌面系统网卡、配置路由 | 由鸿蒙 VPN 扩展创建虚拟网卡，将返回的文件描述符交给内核 |
| 应用规则 | 可执行文件绝对路径与 `process_path` | 新增按应用 `bundleName` 标识的规则类型 |
| 应用发现 | gopsutil 扫描进程、浏览程序目录 | 使用目标 SDK 允许的应用查询或选择方式，首个验证可用已知包名 |
| 订阅与节点 | Clash YAML、sing-box JSON、URI 解析与校验 | 优先复用解析和模型，网络访问与节点校验需结合移动内核适配 |
| 节点测速 | 通过实际代理出站发起 HTTPS 请求 | 复用逻辑，确保测速连接使用正确的物理网络出口 |
| 连接与流量 | 内核追踪与按程序路径归类 | 可优先做总体统计；逐应用统计需接通连接归属查询 |
| 配置与历史 | 原子 JSON、bbolt、桌面配置目录 | 使用鸿蒙应用沙箱目录，验证文件锁、数据库和扩展访问方式 |
| 常驻与生命周期 | 桌面托盘、系统服务、信号处理 | 改为 VPN 扩展生命周期；验证界面退出、锁屏、系统停止与换网 |

关键代码位置：

- `internal/engine/config.go`：当前开启 `find_process`，按程序路径生成路由，默认出口为 `direct`，并启用桌面 TUN 的自动路由。
- `internal/engine/engine.go`：内核启停、连接状态、日志与流量追踪。
- `internal/model/model.go`：程序规则以绝对路径为标识和校验对象。
- `internal/process/process.go`、`internal/program/program.go`：桌面进程发现与文件浏览。
- `internal/subscription/parser.go`：主要订阅解析逻辑。
- `internal/traffic/`：流量采样、归类与持久化。
- `cmd/appforce/desktop.go`、`frontend/src/api.ts`：现有 Wails 窗口和前后端桥接。

## VPN 接入路线

公开的 OpenHarmony VPN 扩展接口提供了以下基础能力。具体 HarmonyOS 版本的可用性，以目标 DevEco SDK 和手机实测为准。

1. 界面请求启动 `VpnExtensionAbility`，由系统处理 VPN 用户授权。
2. 扩展通过 `VpnConnection.create(config)` 创建虚拟网卡，获取文件描述符。
3. 通过 `trustedApplications` 配置进入 VPN 的应用包名和对应网络路由。
4. 在持有该文件描述符的扩展进程中加载原生内核，将描述符交给 sing-box 平台适配层。
5. 代理出站 socket 通过 `protect()` 绕过 VPN，避免节点连接再次进入 TUN；较新 SDK 的进程保护接口可作为后续选项。
6. 内核状态、配置操作和日志通过桥接及必要的 IPC 提供给界面；系统销毁扩展时关闭内核并保存数据。

必须改写移动平台配置：由系统管理网卡和路由，不能沿用桌面自动创建网卡、自动路由和进程扫描。若首版使用应用白名单、所有选中应用共享一个节点，进入 TUN 的目标流量应路由到该节点；直接沿用当前默认 `direct` 的配置会导致流量直连。

界面与 VPN 扩展可能处于不同进程，不能只把文件描述符的整数值传到另一进程就当作可用描述符。内核与 TUN 描述符的进程归属、关闭责任和 IPC 方式需要在原型中验证。

## 两个需要区分的功能层次

**指定应用进入代理、共用一个节点**：VPN 的应用包名名单为此提供了接口基础，适合作为首版目标。不需要依靠桌面的可执行文件路径匹配。

**不同应用分别使用不同节点、逐应用统计**：还需要把每条 TCP / UDP 连接映射到应用身份，再交给内核路由和统计。应用白名单本身不提供逐连接身份。

本次查阅的 OpenHarmony SDK 声明中，`connection.getConnectOwnerUid()` 和同步版本标记为 API 23 起支持，仅限 VPN 应用并要求 `GET_NETWORK_INFO`。这说明存在公开的连接归属查询路线，但不能据此认定所有 HarmonyOS 5 / 6 手机均可使用；还需核对实际 HarmonyOS SDK、UID 与包名映射方式，并验证 UDP、短连接和查询失败时的行为。

应用选择也需单独验证：所查 SDK 中 `bundleManager.getAllBundleInfo()` 标记为系统 API，不能按普通应用可任意枚举所有安装应用来设计。应核对目标 HarmonyOS SDK 的公开查询、可见性规则或系统选择能力；如果受限，先支持填写或导入已知包名。

## 内核编译的实际难点

本机 `go tool dist list` 未列出 `ohos` / `harmonyos` 目标。当前 sing-box 1.14.3 的移动库构建入口提供 Android / Apple 构建分支，项目自身也没有鸿蒙构建工程。

sing-box 存在可复用的平台接口，包括 `OpenInterface`、出站 socket 控制和连接归属查询；sing-tun 支持接收现有 TUN 文件描述符。这些是适配入口，不等于当前依赖已经支持鸿蒙运行。

需要用目标 SDK 的原生工具链验证 Go 共享库的 ABI、CGo、TLS、线程与回调，以及 sing-tun 的文件描述符读写路径。不能把 `GOOS=linux GOARCH=arm64` 编译成功当作鸿蒙适配完成。工具链、补丁和内核版本应固定并纳入可重复构建，避免依赖来源不明的预编译库。

## 建议的首版范围与开发顺序

1. **平台原型**：签名安装最小 HAP，在真机启动 VPN，创建网卡，加载内核，完成一个指定应用通过一个真实节点的 HTTPS 转发。验证代理 socket 绕过 VPN、停止后恢复网络、未选应用按预期联网。
2. **可用首版**：订阅导入与更新、节点选择与测速、按包名指定代理应用、手动启停、连接状态、总体流量和日志。选中应用共享当前默认节点，配置在手机本地保存。
3. **完整适配**：在连接归属能力验证通过后增加逐应用节点和统计；继续验证 IPv6、UDP / QUIC、DNS、Wi-Fi 与蜂窝切换、锁屏运行和异常退出恢复。

首个阶段通过的条件是手机真实流量经过节点，而不是仅页面可操作、系统显示 VPN 标识或工程能够编译。原型测试失败时应先解决平台数据转发问题，再扩展界面。

后续实现建议放入独立 `harmonyos/` 工程和移动内核入口，通过平台适配复用 Go 业务；保留桌面程序的构建入口和路径规则。移动规则使用明确的类型及 `bundleName` 字段，不把包名写入当前要求绝对路径的 `path` 字段。

## 开发与验证条件

- 与目标手机版本匹配的 DevEco Studio / HarmonyOS SDK 及原生工具链。
- 用于开发签名和真机安装的证书、Profile 与设备配置。
- 可供调试的原生鸿蒙手机，以及测试节点和订阅。
- 手机完整系统版本和对应 API 版本，用于确定最低兼容范围。

本次在常见应用安装路径未检测到 DevEco Studio，尚未验证开发工具链、签名材料或连接的鸿蒙设备。本次未修改现有桌面代码。

## 接口参考

- [华为 HarmonyOS VPN 扩展接口文档入口](https://developer.huawei.com/consumer/cn/doc/harmonyos-references/js-apis-net-vpnextension)。本次直接访问返回文档中心页面壳，正文接口核对使用下面的公开 OpenHarmony 资料。
- [OpenHarmony VPN 扩展接口](https://github.com/openharmony/docs/blob/master/en/application-dev/reference/apis-network-kit/js-apis-net-vpnExtension.md)：虚拟网卡文件描述符、应用名单、socket 保护与生命周期。
- [OpenHarmony 网络连接接口声明](https://github.com/openharmony/interface_sdk-js/blob/master/api/@ohos.net.connection.d.ts)：连接归属 UID 查询的版本和权限要求。
- [OpenHarmony 包管理接口声明](https://github.com/openharmony/interface_sdk-js/blob/master/api/@ohos.bundle.bundleManager.d.ts)：安装应用枚举的系统 API 限制。

以上 OpenHarmony `master` 资料可能包含较新版本的接口，不代表目标手机已具备全部能力；实施时需锁定目标 HarmonyOS SDK 版本重新核对。
