param([int]$rx = -1, [int]$ry = -1, [string]$text = "", [switch]$enter, [int]$ProcessId = 0)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName Microsoft.VisualBasic
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class W {
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, int e);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
}
"@
if ($ProcessId -gt 0) {
    $p = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
} else {
    $p = Get-Process lumen -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
}
if (-not $p) { Write-Output "NO WINDOW"; exit 1 }
$hwnd = $p.MainWindowHandle
# A window raised over all others takes the foreground, then goes back.
[void][W]::SetWindowPos($hwnd, [IntPtr](-1), 0, 0, 0, 0, 0x0013)
[void][W]::SetForegroundWindow($hwnd)
Start-Sleep -Milliseconds 250
[void][W]::SetWindowPos($hwnd, [IntPtr](-2), 0, 0, 0, 0, 0x0013)
Start-Sleep -Milliseconds 250
$r = New-Object W+RECT
[void][W]::GetWindowRect($hwnd, [ref]$r)
Write-Output "window $($r.Left),$($r.Top) $($r.Right - $r.Left)x$($r.Bottom - $r.Top)"
if ($rx -ge 0) {
  $x = $r.Left + $rx
  $y = $r.Top + $ry
  [void][W]::SetCursorPos($x, $y)
  Start-Sleep -Milliseconds 200
  [W]::mouse_event(0x0002, 0, 0, 0, 0)
  Start-Sleep -Milliseconds 60
  [W]::mouse_event(0x0004, 0, 0, 0, 0)
  Write-Output "clicked $x,$y"
  Start-Sleep -Milliseconds 400
}
if ($text -ne "") {
  [System.Windows.Forms.SendKeys]::SendWait($text)
  Start-Sleep -Milliseconds 300
}
if ($enter) {
  [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
}
Write-Output "done"
