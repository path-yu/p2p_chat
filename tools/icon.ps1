Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Ico {
  [DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr h, uint msg, IntPtr w, IntPtr l);
  [DllImport("user32.dll")] public static extern bool GetWindowText(IntPtr h, System.Text.StringBuilder s, int n);
}
"@
$procs = Get-Process lumen -ErrorAction SilentlyContinue
if (-not $procs) { Write-Output "NO WINDOW"; exit 1 }
foreach ($p in $procs) {
  $h = $p.MainWindowHandle
  $big = [Ico]::SendMessage($h, 0x007F, [IntPtr]1, [IntPtr]0)
  $small = [Ico]::SendMessage($h, 0x007F, [IntPtr]0, [IntPtr]0)
  Write-Output ("pid={0} hwnd={1} bigIcon={2} smallIcon={3}" -f $p.Id, $h, $big, $small)
}
