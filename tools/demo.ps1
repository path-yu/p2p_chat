# 在一台电脑上开两个 Lumen，用 UDP 打洞把它们连起来。
#
#   .\tools\demo.ps1            两个终端窗口（lumend），可以直接在其中打字互聊
#   .\tools\demo.ps1 -Gui       左边开窗口，右边开终端，能看着气泡冒出来
#   .\tools\demo.ps1 -WithDHT   顺便加入公网会合网络（慢 20~40 秒，用于看 DHT 发现数）
#   .\tools\demo.ps1 -DryRun    只打印要跑的命令，不启动
#
# 端口：甲 TCP 47821 / UDP 47823，乙 TCP 47831 / UDP 47833；
# 乙用 -punch 主动打甲的 UDP 端口，甲收到陌生打洞包会自动回打，于是双向打通。
param(
    [switch]$Gui,
    [switch]$WithDHT,
    [switch]$DryRun,
    [int]$BasePort = 47821
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$window = Join-Path $root 'lumen.exe'
$terminal = Join-Path $root 'lumend.exe'

if ((-not (Test-Path $window)) -or (-not (Test-Path $terminal))) {
    Write-Host '先编译…'
    & (Join-Path $PSScriptRoot 'build.ps1')
}

$aTcp = $BasePort
$aUdp = $BasePort + 2
$aDisc = $BasePort + 20
$bTcp = $BasePort + 10
$bUdp = $BasePort + 12
$bDisc = $BasePort + 22

$dirA = Join-Path $root 'demo\a'
$dirB = Join-Path $root 'demo\b'
New-Item -ItemType Directory -Force -Path $dirA, $dirB | Out-Null

$aArgs = @('-port', $aTcp, '-udp-port', $aUdp, '-discovery-port', $aDisc,
    '-dir', $dirA, '-network', 'demo-local', '-name', '甲')
$bArgs = @('-port', $bTcp, '-udp-port', $bUdp, '-discovery-port', $bDisc,
    '-dir', $dirB, '-network', 'demo-local', '-name', '乙',
    '-punch', "127.0.0.1:$aUdp")
if (-not $WithDHT) {
    $aArgs += '-no-dht'
    $bArgs += '-no-dht'
}

function Quote([string]$s) {
    if ($s -match '\s') { return '"' + $s + '"' }
    return $s
}

$line = ($aArgs | ForEach-Object { Quote $_ }) -join ' '
$cmdA = "title 甲 & ""$terminal"" $line"

$line = ($bArgs | ForEach-Object { Quote $_ }) -join ' '
$cmdB = "title 乙 & ""$terminal"" $line"

if ($DryRun) {
    Write-Host '甲要执行的命令：' -ForegroundColor Yellow
    Write-Host "  $cmdA"
    Write-Host '乙要执行的命令：' -ForegroundColor Yellow
    Write-Host "  $cmdB"
    return
}

if ($Gui) {
    Start-Process -FilePath $window -ArgumentList $aArgs
} else {
    Start-Process -FilePath 'cmd.exe' -ArgumentList '/k', $cmdA
}
Start-Sleep -Milliseconds 900
Start-Process -FilePath 'cmd.exe' -ArgumentList '/k', $cmdB

Write-Host ''
Write-Host "甲：TCP $aTcp  UDP $aUdp  （$dirA）" -ForegroundColor Cyan
Write-Host "乙：TCP $bTcp  UDP $bUdp  （$dirB）" -ForegroundColor Cyan
Write-Host ''
Write-Host '在任意一个终端里打一行字回车，另一边应立即出现 [lobby] 某某: …' -ForegroundColor Green
Write-Host 'Lumen 窗口打开时，右侧「节点信息 → 跨网穿透」能看见公网地址与 NAT 类型；'
Write-Host '侧栏的在线设备里，对方地址会标着「打洞」。'
Write-Host ''
Write-Host '收尾：关掉两个窗口即可；数据在 .\demo\ 下，删掉就等于重置。' -ForegroundColor DarkGray
