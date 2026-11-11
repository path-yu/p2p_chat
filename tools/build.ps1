# 编译 Lumen 的两个产物：
#
#   lumen.exe   窗口版。带 -H=windowsgui，双击只出窗口，不弹黑框。
#   lumend.exe  终端/服务器版。纯 Go，不依赖图形库。
#
#   .\tools\build.ps1            只编 Windows 的两个
#   .\tools\build.ps1 -Linux     顺带交叉编译 Linux（amd64 + arm64）
param([switch]$Linux)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    Write-Host '编译 lumen.exe（窗口程序，无控制台）…'
    go build -trimpath -ldflags '-H=windowsgui -s -w' -o lumen.exe .
    if ($LASTEXITCODE -ne 0) { throw 'lumen.exe 编译失败' }

    Write-Host '编译 lumend.exe（终端/服务器节点）…'
    go build -trimpath -ldflags '-s -w' -o lumend.exe ./cmd/lumend
    if ($LASTEXITCODE -ne 0) { throw 'lumend.exe 编译失败' }

    if ($Linux) {
        Write-Host '交叉编译 Linux amd64 / arm64…'
        $env:CGO_ENABLED = '0'
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        go build -trimpath -ldflags '-s -w' -o lumen-linux ./cmd/lumend
        $env:GOARCH = 'arm64'
        go build -trimpath -ldflags '-s -w' -o lumen-linux-arm64 ./cmd/lumend
        Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    }

    Get-ChildItem lumen.exe, lumend.exe, lumen-linux, lumen-linux-arm64 -ErrorAction SilentlyContinue |
        ForEach-Object { '{0,8:N1} MB  {1}' -f ($_.Length / 1MB), $_.Name }
} finally {
    Pop-Location
}
