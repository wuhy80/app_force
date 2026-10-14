# AppForce 鸿蒙手机版

原生 HarmonyOS 5 / 6 / NEXT 手机端位于当前仓库的 `harmonyos/`，独立作为 DevEco Studio 工程打开，和桌面版共享 Go 模块中的订阅解析与节点模型。

## 实现范围与验证状态

已实现 ArkUI 界面、VPN 扩展生命周期、N-API 桥接和 Go 移动内核代码：

- 连接启停、真实后台状态、最近一次连接的上传/下载速率与总量。
- HTTPS 订阅导入和更新，粘贴节点链接 / Base64 / Clash / sing-box 配置，节点搜索与选择。
- 按准确的应用包名添加、启用、停用和移除代理应用，最多 64 个；选中应用共用一个节点。
- IPv4 / IPv6 TUN、DNS 请求接管、通过所选节点使用 HTTPS DNS，局域网及非公共地址直连。
- 活动连接与最近运行日志，应用沙箱内原子配置保存、跨进程文件锁及停止请求。

本机通过了宿主机 Go 测试、虚拟 TUN 的 IPv4 / IPv6 HTTPS 转发测试、保护失败阻断测试，以及 C++ / N-API 与真实 Go 共享库的接口测试。**尚未执行 HarmonyOS SDK 的 ArkTS / CMake 编译、HAP 打包、签名或真机验证，当前没有可安装的鸿蒙包。**

**构建前仍需解决 Go 工具链前提**：仓库尚未提供经过真机验证、兼容当前依赖的鸿蒙 Go 工具链。普通 Go 和 DevEco Studio 不能直接把现有 Go 核心变成可用的鸿蒙共享库。需要支持鸿蒙 ARM64 CGo / c-shared、满足 Go >= 1.25.5，并兼容 sing-box 1.14.3 及 sing-tun 的工具链；参见 [核心构建说明](core/README.md)。

工程配置以 Stage 模型、DevEco 5.x、HarmonyOS `5.0.0(12)`、手机、`arm64-v8a` 为起点；目标 SDK 的 VPN API 可用性、应用名单语义和 FD 读写仍以 SDK 编译及真机验证为准。

## 目录

```text
harmonyos/
├── AppScope/                     应用标识与资源
├── entry/src/main/ets/
│   ├── entryability/             UI 生命周期
│   ├── pages/Index.ets           连接、节点、应用、活动四个页面
│   ├── service/                  订阅下载、内核调用、VPN 控制
│   ├── model/                    JSON 接口模型
│   └── vpn/AppForceVpnAbility.ets 系统 VPN 与 TUN 生命周期
├── entry/src/main/cpp/            C++ N-API 桥接及 ArkTS 类型声明
├── entry/libs/arm64-v8a/          构建生成的鸿蒙 Go 共享库（忽略提交）
├── core/mobile/                  配置、平台适配与内核运行
├── core/cshared/                 Go C ABI 导出
├── core/scripts/                 核心构建与宿主机桥接测试脚本
├── core/tests/                   宿主机原生接口测试夹具
└── build-profile.json5           SDK 与产品配置
```

根目录的 `make build`、`make desktop`、`make windows` 是桌面构建入口。鸿蒙界面使用 ArkUI，无需构建桌面 Vue 前端。

## 在 DevEco 电脑上构建

1. 先准备符合 [核心构建说明](core/README.md) 的鸿蒙 Go 工具链和 HarmonyOS 原生 SDK，运行预检及核心构建，把 **鸿蒙 ARM64** 的 `libappforce_core.so` 放入 `entry/libs/arm64-v8a/`。`--host` 产物仅用于本机测试，不能复制进去。
2. 在 DevEco Studio 中打开本目录，同步工程。根据实际 SDK 调整 `build-profile.json5`；升级工程模型时同时核对 `oh-package.json5` 与 `hvigor/hvigor-config.json5`。
3. 在 Project Structure / Signing Configs 中配置开发签名并关联到 `default` 产品。SDK 路径、证书、私钥和签名配置留在本机。
4. 构建 `entry` HAP。检查安装包的 ARM64 库目录同时包含 `libappforce.so` 和 `libappforce_core.so`，再连接手机运行。
5. 导入测试节点，添加一个已安装应用的准确包名并启用，开启代理并接受系统 VPN 授权。

未打包核心库时，应用会显示内核加载错误并禁用操作，不会展示虚假的已连接状态。

## 首次真机验收

按顺序确认以下行为，尤其关注系统 VPN 标识和实际流量是否一致：

1. 所选应用通过测试节点访问 HTTPS；未选应用继续使用自身网络。空应用范围和 AppForce 自身包名不能开启代理。
2. 应用 DNS 使用节点；代理节点连接及引导 DNS 不进入自己的 VPN。核对 IPv4 / IPv6、UDP / QUIC 和不同节点协议。
3. 切换节点、更新订阅和修改应用范围需要先停止代理。无效订阅不能覆盖原有节点。
4. 停止后系统 VPN 和内核都退出，应用网络恢复；授权拒绝、创建失败和核心加载失败后没有残留 TUN。
5. 关闭界面后 VPN 继续运行；重新打开可读取状态。锁屏、系统停止 VPN、异常杀进程后状态与网络恢复正确。
6. 测试 Wi-Fi / 蜂窝切换及断网重连。当前适配让系统为保护后的 socket 选择物理出口，尚未接入网络变化事件来主动重置已有连接；如旧连接未恢复，应停止后重新连接并记录机型与 SDK。

首版不枚举所有安装应用，不提供逐应用不同节点、逐应用历史统计或节点测速。逐连接应用归属查询和安装应用查询在不同 SDK 上受版本、可见性和权限限制，需另行验证后接入。

接口限制与原始评估见 [鸿蒙移植评估](../docs/harmonyos-port.md)。
