param([Parameter(Mandatory=$true)][string]$DriverPath)
$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (!(New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw '请在管理员 PowerShell 中运行。' }
$source = (Resolve-Path $DriverPath).Path
if ([IO.Path]::GetFileName($source) -ne 'AppForceWfp.sys') { throw '请选择 AppForceWfp.sys。' }
$signature = Get-AuthenticodeSignature -FilePath $source
if ($signature.Status -ne 'Valid') { throw "驱动签名无效：$($signature.Status)。请先按文档完成测试签名或正式签名。" }
$existing = Get-Service -Name AppForceWfp -ErrorAction SilentlyContinue
if ($existing) { throw '驱动服务已经存在。先停止 AppForce 并卸载旧驱动，再安装更新。' }
# A standard user must never be able to replace a loadable kernel image.
$installDir = Join-Path $env:ProgramFiles 'AppForce\Drivers'
New-Item -ItemType Directory -Path $installDir -Force | Out-Null
& icacls.exe $installDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
if ($LASTEXITCODE -ne 0) { throw '设置驱动目录权限失败。' }
$target = Join-Path $installDir 'AppForceWfp.sys'
Copy-Item -LiteralPath $source -Destination $target -Force
& sc.exe create AppForceWfp type= kernel start= demand binPath= "`"$target`"" DisplayName= 'AppForce WFP TCP prototype'
if ($LASTEXITCODE -ne 0) { throw '创建驱动服务失败。' }
& sc.exe start AppForceWfp
if ($LASTEXITCODE -ne 0) { throw '驱动未能加载。检查签名、测试模式、Secure Boot 和系统事件日志；不会自动放宽系统设置。服务可用卸载脚本移除。' }
Write-Host '驱动已加载。以管理员身份启动 AppForce，在设置中手动选择 WFP TCP 原型。'
