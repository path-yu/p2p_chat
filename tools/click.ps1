param([int]$x, [int]$y)
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Mouse {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, int e);
  [DllImport("user32.dll")] public static extern IntPtr FindWindow(string c, string n);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
}
"@
$p = Get-Process lumen -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if (-not $p) { Write-Output "NO WINDOW"; exit 1 }
[void][Mouse]::SetForegroundWindow($p.MainWindowHandle)
Start-Sleep -Milliseconds 300
[void][Mouse]::SetCursorPos($x, $y)
Start-Sleep -Milliseconds 150
[Mouse]::mouse_event(0x0002, 0, 0, 0, 0)
Start-Sleep -Milliseconds 80
[Mouse]::mouse_event(0x0004, 0, 0, 0, 0)
Write-Output "clicked $x,$y"
