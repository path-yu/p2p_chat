param([int]$rx = 600, [int]$ry = 300)
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Mv {
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
}
"@
$p = Get-Process lumen -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if (-not $p) { Write-Output "NO WINDOW"; exit 1 }
$r = New-Object Mv+RECT
[void][Mv]::GetWindowRect($p.MainWindowHandle, [ref]$r)
[void][Mv]::SetCursorPos($r.Left + $rx, $r.Top + $ry)
Write-Output "moved"
