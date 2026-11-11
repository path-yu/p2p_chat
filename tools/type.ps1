param([string]$text, [switch]$enter)
Add-Type -AssemblyName System.Windows.Forms
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Fg {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
}
"@
$p = Get-Process lumen -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if (-not $p) { Write-Output "NO WINDOW"; exit 1 }
[void][Fg]::SetForegroundWindow($p.MainWindowHandle)
Start-Sleep -Milliseconds 350
[System.Windows.Forms.SendKeys]::SendWait($text)
Start-Sleep -Milliseconds 250
if ($enter) {
  [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
}
Write-Output "typed"
