$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

function Invoke-Checked([scriptblock]$Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "构建失败，退出码 $LASTEXITCODE" }
}

Invoke-Checked { npm --prefix frontend ci }
Invoke-Checked { npm --prefix frontend run build }
Invoke-Checked { go run ./build/agents }
Invoke-Checked { go run ./build/windows }
$env:CGO_ENABLED = "0"
Invoke-Checked { go build -trimpath -tags "desktop,production,with_quic,with_utls,with_gvisor" -ldflags "-H windowsgui" -o bin/AppForce.exe ./cmd/appforce }
Write-Host "已生成 bin/AppForce.exe；内核、界面、Wintun 与 Linux/macOS 远程 Agent 均已嵌入。"
