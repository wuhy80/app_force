# Windows WFP TCP 原型

本次新增的是**可选的实验性 TCP 后端源码**，用于验证按连接筛选是否解决未选程序下载降速。它还没有经过 Windows WDK 构建、驱动加载和实机性能验收，不是已经验证可用的正式驱动发布版。原有默认模式仍为 TUN；只有在 Windows 设置中手动选择 WFP，才会尝试加载本后端。

## 数据路径与支持范围

驱动只注册 ALE 连接级 callout，不注册数据包或流内容 callout。未匹配程序返回 `CONTINUE`，其 TCP/UDP 内容不进入 AppForce 的转发队列，也不为展示直连统计而截获数据。每次连接建立仍有一次内核分类成本，不能承诺共享带宽和 CPU 负载下速度绝对不变。

| 项目 | 本原型行为 |
| --- | --- |
| 所选程序的公网 TCP | IPv4/IPv6 重定向至对应的本地 TCP 入口，经 sing-box 的所选节点转发 |
| 所选程序的公网 UDP / QUIC | 明确阻断；支持回落的应用可尝试 TCP。尚未实现 UDP 代理 |
| DNS 端口 53、局域网、回环、链路本地地址 | 保留系统路径，和原有程序路由的本地访问策略一致 |
| 未选程序 | 保留系统路径；不统计其上传下载 |
| 系统 DNS、系统代理、默认路由、网卡 | 不修改；WFP 模式仅允许 `dns_mode=system` |
| TCP 上游节点 | SOCKS、HTTP、Shadowsocks、VMess、VLESS、Trojan；原生 TCP 或 WebSocket 传输 |
| 暂不支持的节点设置 | QUIC 协议节点、多路复用、HTTP/H2/gRPC 等传输、Shadowsocks 插件、TCP Fast Open；启动前报错 |
| 所选节点不可用 | 匹配的代理连接失败，不回落到 direct |
| 动态规则或透明入口失效 | 启动时验证，运行中每秒检查；发现失效后停止代理、关闭现有代理连接并报告错误 |
| 正常停止、进程退出或崩溃 | 关闭独占控制句柄，删除动态接管规则；之后的新连接恢复系统路径 |

**此原型不提供 AppForce 退出后的防直连保护。** BFE 服务重启等使动态规则丢失时，系统路径可能在下次检测之前就已恢复。健康检测用于发现故障并纠正运行状态，不能保证这段时间或代理停止后持续阻断直连。检测报错后需修复原因并手动重新启动，不自动重装规则或切换到 TUN。已有 TCP 连接不会自动迁移；切换模式后应重启待测应用或重新发起请求。绑定特定本地网卡的 socket、其他本地重定向驱动与 VPN 共存尚需专项验证。

Antigravity 规则包括其主可执行文件，以及同一安装目录下明确命名的 `language_server_windows_x64.exe`、`language_server_windows_arm64.exe`、`language_server.exe`。不会自动接管其他 `node.exe`、终端、打包工具或全部子进程。普通程序按完整可执行文件路径匹配；每次最多 64 条启用规则。

## 实现与生命周期

- `drivers/windows/wfp`：WDM 驱动、固定长度的版本化协议、非分页内存中的路径策略、TCP 重定向和所选 UDP 阻断。只允许管理员和 SYSTEM 打开独占设备，IOCTL 校验代理 PID、大小和规则范围。
- `internal/wfp`：通过 `FwpmGetAppIdFromFileName0` 获取内核应用标识，先打开 IPv4/IPv6 监听器，再提交规则并验证状态。启动失败撤销规则并关闭监听器，停止时先删除捕获规则再关闭连接。连接跟踪保留 TCP 半关闭能力，请求方向 EOF 不会提前结束响应方向。
- 健康查询通过安装时的动态会话读取四条 TCP/UDP、IPv4/IPv6 过滤器，检查对应层、callout 与协议条件，再返回当前会话随机值和规则数量；应用验证响应并检测监听器异常。旧会话的延迟检测结果不会停止重新启动后的实例。
- 接受的 TCP socket 必须通过 `SIO_QUERY_WFP_CONNECTION_REDIRECT_CONTEXT` / `...RECORDS` 提供有效上下文。校验会话随机值、规则、进程路径和原目标，拒绝普通 TCP 客户端及过期上下文。
- `internal/engine/wfp.go`：将可信身份交给 sing-box 路由与统计。专用底层 TCP 出站在 `connect` 前通过 `SIO_SET_WFP_CONNECTION_REDIRECT_RECORDS` 设置记录，之后才执行代理协议和 TLS 握手。每连接记录通过 context 传递，不使用全局映射。
- 驱动识别自身重定向并排除 AppForce PID。对已由其他代理重定向的所选公网连接采用阻断，不把另一个代理的地址当作原目的地继续接管；共存行为仍待实测。
- 切到 WFP 后启动失败不会改用 TUN；从运行中的 TUN 切换失败时，也不会重新启动旧 TUN。旧配置保留，界面明确报告代理已停止。

## 自用是否要购买证书

WFP 本身免费。自用仍受 Windows 内核签名检查限制，但**测试阶段可以使用免费生成的本地测试证书**。

测试签名与正常发布签名有区别：测试模式通常要求重启，Secure Boot 可能阻止开启。内存完整性/HVCI 也会检查驱动签名和兼容性；测试模式不能视为任意未签名驱动均可加载。请先使用可还原的 Windows 测试机或虚拟机，保留系统原有设置。AppForce 和脚本不会自动改变这些启动或安全设置。

如果希望在保持普通系统启动设置的情况下直接安装，需要 Windows 接受的正式签名驱动。正式签名涉及微软硬件开发者中心的账户、证书及签名流程，自签测试证书不能代替它。当前仓库不包含正式签名证书或已签名 `.sys`。

## Windows 构建

环境：Windows 10 2004 或更新系统，Visual Studio 2022 C++ 工具、对应架构编译工具，以及相同版本的 Windows SDK / WDK 和 WDK 的 Visual Studio 集成。使用 `ExAllocatePool2`，不支持旧版 Windows。

当前应用与驱动使用协议 **v2**，新增动态规则健康查询。若安装过 v1 原型，需要停止 AppForce、卸载旧驱动，再构建、签名并安装本版本驱动；应用与旧驱动混用会启动失败。

在仓库根目录运行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-wfp-driver.ps1 -Platform x64 -Configuration Debug
# ARM64 测试机使用匹配架构：
powershell -ExecutionPolicy Bypass -File scripts/build-wfp-driver.ps1 -Platform ARM64 -Configuration Debug
```

输出 `bin\wfp\<平台>\Debug\AppForceWfp.sys`。此处仅构建，关闭自动签名；未签名产物不能直接安装。普通 `make windows` / `scripts/build-windows.ps1` 仍只构建应用，**不会自动产生或安装驱动**。

## 本地测试签名与安装

以下命令由测试机管理员手动执行。安装脚本只接受 `Get-AuthenticodeSignature` 验证为有效的嵌入签名；Windows 内核还会在加载时独立验证签名。生产签名若仅在 catalog 中，需要相应正式安装包，不能沿用此原型脚本。

1. 创建本机测试签名证书，导入其公开证书，私钥保存在本机证书存储中：

   ```powershell
   $wfpTestCert = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=AppForce WFP Local Test' -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 -CertStoreLocation Cert:\LocalMachine\My
   Export-Certificate -Cert $wfpTestCert -FilePath .\AppForceWfp-test.cer
   Import-Certificate -FilePath .\AppForceWfp-test.cer -CertStoreLocation Cert:\LocalMachine\Root
   Import-Certificate -FilePath .\AppForceWfp-test.cer -CertStoreLocation Cert:\LocalMachine\TrustedPublisher
   ```

2. 在安装了 SDK 签名工具的开发命令行中签名，`signtool.exe` 需在 PATH 中：

   ```powershell
   signtool.exe sign /v /fd SHA256 /sm /s My /sha1 $wfpTestCert.Thumbprint .\bin\wfp\x64\Debug\AppForceWfp.sys
   signtool.exe verify /v /pa .\bin\wfp\x64\Debug\AppForceWfp.sys
   ```

3. 如测试机尚未开启测试模式，先核对 Secure Boot 和恢复设置，再手动执行 `bcdedit /set testsigning on` 并重启。若 Windows 拒绝，不应假定签名步骤已经足够；按微软文档确认测试环境。不会在脚本中自动关闭 Secure Boot 或内存完整性。
4. 在管理员 PowerShell 中安装并按需启动驱动：

   ```powershell
   powershell -ExecutionPolicy Bypass -File scripts/install-wfp-driver.ps1 -DriverPath .\bin\wfp\x64\Debug\AppForceWfp.sys
   ```

   驱动镜像复制到 `Program Files\AppForce\Drivers`，目录仅允许管理员/SYSTEM 写入，服务为按需启动。每次 Windows 重启后，如服务未运行，先手动 `sc.exe start AppForceWfp`。驱动加载不等于启用捕获，只有 AppForce 成功提交规则后才接管连接。
5. 构建并以管理员身份启动 AppForce，在设置中选择“按程序代理 · WFP（TCP 原型）”，保留系统 DNS，选择兼容 TCP 节点并启用程序规则，然后启动代理。不要把打包器及其通用运行时额外加入规则。

卸载时先停止 AppForce，再以管理员身份运行 `scripts/uninstall-wfp-driver.ps1`。测试结束后，根据此前记录恢复测试模式；若原来关闭，可手动执行 `bcdedit /set testsigning off` 并重启。只移除本次测试证书的相应指纹，不删除其他证书；恢复此前主动修改的 Secure Boot 等设置。

## 必须完成的实机验收

1. **对照下载**：同一打包程序、同一压缩包、同一网络条件，分别在 AppForce 停止、本地代理、TUN、WFP 下重新建立连接，至少重复三次。记录中位吞吐、CPU、耗时和代理流量；排除缓存命中和 CDN 节点差异。
2. **路径证明**：比较 `Get-NetRoute`、`Get-DnsClientServerAddress` 和系统代理配置；WFP 前后应一致。用 Windows WFP/网络跟踪核实未选程序没有连接到透明监听器，其数据不进入用户态转发。
3. **所选程序**：分别验证 IPv4/IPv6 HTTPS、大响应、上传、TCP 半关闭、长连接、重新启动进程，以及程序指定节点和流量归属。切断所选节点后请求应失败。
4. **范围边界**：从 Antigravity 启动打包工具，核实其新建 TCP/UDP 连接不被接管。测试 Antigravity 安装目录的三种语言服务名称、旁边的 `node.exe`、目录同名前缀和另一个安装路径。
5. **UDP 和 DNS**：所选程序公网 UDP/QUIC 被阻断、支持回落的业务改走 TCP；未选程序 UDP 正常，系统 DNS 与局域网访问正常。原型不应被记为通过 UDP 代理验收。
6. **生命周期**：启动/停止、配置更新、缺少驱动、版本不符、端口和 IPv6 不可用、驱动激活失败、任务管理器终止 AppForce、重复启动、休眠恢复。检查动态过滤器清理；验证退出后恢复系统路径符合预期。在测试环境模拟 BFE 会话或过滤器丢失，核实下次健康查询后界面与日志报告代理停止、现有代理连接关闭，且不自动回退 TUN；记录检测之前系统路径已可能恢复的行为。
7. **驱动质量**：先在可恢复测试环境启用针对 `AppForceWfp.sys` 的 Driver Verifier，检查池内存、IRQL、I/O、卸载和并发；随后测 HVCI、VPN、防火墙及其他 WFP 重定向工具。不要把主机端 C 单元测试或 Go 交叉编译当成这些检查已通过。

当前开发机可执行的检查：

```bash
bash scripts/test-wfp-policy.sh
go test -race -tags with_quic,with_utls,with_gvisor ./...
go vet -tags with_quic,with_utls,with_gvisor ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags with_quic,with_utls,with_gvisor ./internal/engine ./internal/wfp
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -tags with_quic,with_utls,with_gvisor ./internal/engine ./internal/wfp
```

C 检查直接编译驱动实际使用的路径与地址范围判断代码，并校验线协议尺寸；Go 测试覆盖边界、上下文验证、双栈监听、TCP 半关闭、健康故障清理、重启隔离和真实 sing-box/SOCKS 转发。**它们没有运行 WFP 内核 API，也没有证明未选程序的实机吞吐已经恢复。** TCP 实机验证完成后，再实现并验收 UDP/QUIC 转发和正式驱动签名发布；健康查询仍需验证 BFE 故障时的实际返回及清理行为。

参考：[微软连接重定向说明](https://learn.microsoft.com/en-us/windows-hardware/drivers/network/using-bind-or-connect-redirection)、[测试签名](https://learn.microsoft.com/en-us/windows-hardware/drivers/install/the-testsigning-boot-configuration-option)、[内核驱动签名要求](https://learn.microsoft.com/en-us/windows-hardware/drivers/install/kernel-mode-code-signing-policy)。其他方案及取舍见 [方案调研](windows-selective-proxy-research.md)。
