# 原生桥接

`CMakeLists.txt` 构建 `libappforce.so`，`types/libappforce/` 提供 ArkTS 声明。模块通过 `dlopen` 加载 HAP 中同架构的 `libappforce_core.so`，解析四个 C ABI 导出；成功加载的 Go 运行时保持到进程结束。

`call`、`start`、`stop` 使用 N-API 异步工作线程。Go 线程的 socket 保护请求通过线程安全函数调度到 ArkTS，等待 `VpnConnection.protect(fd)` 的 Promise 成功后才继续。队列最多 64 项，排队失败、Promise 拒绝、异常、非 Promise 返回或超过 10 秒均阻断连接。

保护请求持有 socket 的副本，延迟的系统操作不会保护到已被复用的原始 FD。Promise 完成后关闭副本；超时后保留到延迟操作完成或相关回调被回收。核心关闭时先阻断后续保护请求并等候已有调用退出，再释放线程安全函数及原生回调上下文。

界面仅调用配置和状态接口，TUN 启停在 VPN 扩展进程中执行。实际 `.so` 放在 `entry/libs/arm64-v8a/`，不在运行时下载核心。未加载核心时返回明确错误。

宿主机 N-API 检查入口及鸿蒙工具链前提见 [核心说明](../../../../core/README.md)，HAP 构建及真机步骤见 [工程说明](../../../../README.md)。
