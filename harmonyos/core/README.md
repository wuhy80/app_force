# Go 移动内核

`mobile/` 在根目录 `appforce` Go 模块中复用 `internal/model`、`internal/subscription` 和日志模型。移动配置单独生成：系统管理 TUN 与路由，内核处理包转发；进入 VPN 的应用共用所选节点，非公共地址直连，53 端口接管到 DNS。

## 鸿蒙构建前提

当前依赖要求 Go >= 1.25.5。官方普通 Go 没有 `openharmony/arm64` 目标，**安装 DevEco 和原生 SDK 后仍不能直接完成核心构建**。仓库没有随附鸿蒙 Go 运行时补丁或预编译内核；下面的脚本是满足条件后的构建入口，不代表工具链移植已经完成。

选定工具链必须支持鸿蒙 ARM64 的 CGo / `-buildmode=c-shared`，并将当前依赖所用的 Linux 构建约束、syscall / x/sys、musl、TLS、线程、文件锁及 sing-tun FD 读写适配到鸿蒙。核心自身接受 `linux`、`darwin` 和 `openharmony` 构建标签；依赖是否兼容需要用选定工具链实际编译验证。不能通过降低根目录 `go.mod` 的版本绕过依赖要求，也不能把 Linux ARM64 共享库当作已验证的鸿蒙库。

以下为 Bash / macOS / Linux 构建入口。Windows 上应使用所选工具链支持的构建环境，随后将鸿蒙 `.so` 复制到 DevEco 工程：

```bash
export OHOS_GO=/absolute/path/to/harmony-compatible-go/bin/go
export OHOS_NATIVE_HOME=/absolute/path/to/HarmonyOS/sdk/native
bash harmonyos/core/scripts/build-core.sh --check
bash harmonyos/core/scripts/build-core.sh
```

脚本校验本地 Go 版本、`openharmony/arm64` 目标和 SDK `llvm/bin/clang`、`sysroot`，使用 `with_quic,with_utls,with_gvisor` 构建。`GOTOOLCHAIN=local` 避免自动切换回普通 Go。输出到 `core/build/ohos/`，再复制 `libappforce_core.so` 到 `entry/libs/arm64-v8a/`。

实验性移植工具链可显式设置 `OHOS_GOOS`；这只改变构建目标，预检通过或编译成功均不证明鸿蒙 ABI 与运行时兼容。真机还必须验证动态加载、TLS、线程回调、TUN 读写及 socket 保护。

## 接口与所有权

C ABI 在 `cshared/main.go` 导出：

| 函数 | 职责 |
| --- | --- |
| `AppForceCall(request)` | JSON 配置与状态操作，返回分配的 JSON 字符串 |
| `AppForceStart(directory, fd, protect, context)` | 加载配置、复制系统 TUN FD、启动内核；保护回调返回 0 才允许连接 |
| `AppForceStop()` | 等待保护回调退出、停止核心、保存最终状态、释放运行锁 |
| `AppForceFree(pointer)` | 释放上述返回字符串，调用方必须执行一次 |

ArkTS 扩展拥有系统原始 TUN FD；Go 拥有 `dup` 后的副本。扩展先停止 Go 核心，再销毁系统 VPN 并关闭原始 FD。FD 只在创建它的 VPN 进程内交接，界面不持有该 FD。

socket 保护通过平台接口安装。`route.auto_detect_interface` 必须启用，平台初始化时必须填充出口列表；否则 sing-box 不会保护连接或会报告无可用出口。平台提供单个逻辑出口，使保护后的 socket 由系统选择物理网络，避免自行配置 OS 网卡、路由或 netlink。

配置位于 `${filesDir}/appforce/config.json`，包含订阅凭证和节点参数，以 0600 权限原子写入。公开状态不返回节点凭证或完整订阅 URL；在线更新通过独立本地方法获取 URL。`config.lock` 串行化配置访问；`vpn.lock` 在整个运行期间持有，阻止双重启动和运行中的配置修改。

VPN 每秒原子更新 `runtime.json`；界面每两秒读取。`vpn.lock` 判定核心是否仍然存活，心跳缺失或过期时报告待确认。`stop.json` 是带会话 ID 和时间的停止请求，防止旧请求停止新会话。状态和日志保留最近一次连接数据，未实现累计历史统计。

## 宿主机验证

从仓库根目录运行：

```bash
go test -tags with_quic,with_utls,with_gvisor ./harmonyos/core/mobile ./harmonyos/core/cshared
go test -race -tags with_quic,with_utls,with_gvisor ./harmonyos/core/mobile
go vet -tags with_quic,with_utls,with_gvisor ./harmonyos/core/mobile ./harmonyos/core/cshared
bash harmonyos/core/scripts/test-native.sh
```

原生测试需 C/C++ 编译器、Node 和 Node N-API 头文件，可用 `NODE_INCLUDE_DIR` 指定头文件目录。脚本构建本机 Go 共享库、用 Node 的 N-API 编译同一桥接源文件，验证实际 JSON ABI；另用测试夹具验证保护 Promise 的成功、拒绝、异常、超时和 FD 生命周期。测试夹具仅存于临时构建目录，不进入 HAP。

Go 集成测试使用内存 TUN 和本地 TLS / HTTP CONNECT 服务，验证真实 sing-box 入站与转发，不修改宿主机网卡和路由。上述检查不替代 HarmonyOS SDK 编译及真机验收。
