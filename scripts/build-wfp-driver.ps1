param(
    [ValidateSet('x64', 'ARM64')][string]$Platform = 'x64',
    [ValidateSet('Debug', 'Release')][string]$Configuration = 'Debug'
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
if (!(Test-Path $vswhere)) { throw '请安装 Visual Studio 2022 C++ 工具和配套 Windows SDK / WDK（含 VS 扩展）。' }
$msbuild = & $vswhere -latest -products '*' -requires Microsoft.Component.MSBuild -find 'MSBuild\**\Bin\MSBuild.exe' | Select-Object -First 1
if (!$msbuild) { throw '未找到 MSBuild。' }
$includeRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\Include'
$kit = Get-ChildItem -Path $includeRoot -Directory | Where-Object { Test-Path (Join-Path $_.FullName 'km\fwpsk.h') } | Sort-Object { [version]$_.Name } -Descending | Select-Object -First 1
if (!$kit) { throw '未找到 WDK 的 km\fwpsk.h。请安装 Windows SDK 和相同版本的 WDK。' }
& $msbuild (Join-Path $repo 'drivers\windows\wfp\AppForceWfp.vcxproj') /m /t:Build "/p:Platform=$Platform" "/p:Configuration=$Configuration" "/p:WindowsTargetPlatformVersion=$($kit.Name)" /p:SignMode=Off
if ($LASTEXITCODE -ne 0) { throw "驱动构建失败（$LASTEXITCODE）。" }
Write-Host "输出：bin\wfp\$Platform\$Configuration\AppForceWfp.sys（未签名，不能直接加载）。"
Write-Host '测试签名、安装和验收步骤见 docs/windows-wfp.md。此脚本不修改测试模式或 Secure Boot。'
