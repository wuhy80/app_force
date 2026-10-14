# VPN 扩展

`AppForceVpnAbility.ets` 已在 `module.json5` 中注册为 `vpn` 扩展。界面调用系统 VPN 启停接口，由系统处理授权；扩展从应用沙箱读取配置，在自身进程创建 TUN 并调用本进程 N-API 核心。

配置包含 IPv4 / IPv6 地址、默认路由、虚拟 DNS、MTU 1400 和非空 `trustedApplications` 白名单。原始 TUN FD 由扩展持有，核心复制后使用。启动和系统销毁可能重叠，扩展等待正在进行的启动任务结束，再依次停止核心、销毁 VPN 和关闭原始 FD。

跨进程配置与运行状态通过应用私有目录中的文件锁、原子 JSON 和停止请求同步，界面不跨进程传递 FD。关闭界面不会停止 VPN；系统停止通过扩展销毁生命周期进行清理。

目前声明 INTERNET 和 GET_NETWORK_INFO，未申请应用枚举等系统特权。公开 API 的版本与 VPN 扩展行为仍需在目标 HarmonyOS SDK 和真机上验证；已完成的宿主机测试范围与真机验收顺序见 [工程说明](../../../../../README.md)。
