$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (!(New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw '请在管理员 PowerShell 中运行。' }
$service = Get-Service -Name AppForceWfp -ErrorAction SilentlyContinue
if ($service) {
    if ($service.Status -ne 'Stopped') {
        & sc.exe stop AppForceWfp
        if ($LASTEXITCODE -ne 0) { throw '驱动仍在使用。先停止 AppForce 的代理，再重试卸载。' }
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
    }
    & sc.exe delete AppForceWfp
    if ($LASTEXITCODE -ne 0) { throw '删除驱动服务失败。' }
}
$image = Join-Path $env:ProgramFiles 'AppForce\Drivers\AppForceWfp.sys'
if (Test-Path $image) { Remove-Item -LiteralPath $image }
Write-Host '驱动服务和镜像已移除。测试证书与测试模式请按文档自行恢复。'
