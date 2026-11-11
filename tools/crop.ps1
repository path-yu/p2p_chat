param([string]$src = "D:\GitCode\ptp_chat\screen.png", [int]$x0 = 900, [int]$y0 = 0, [int]$w = 240, [int]$h = 60, [int]$scale = 6)
Add-Type -AssemblyName System.Drawing
$img = [System.Drawing.Image]::FromFile($src)
$rect = New-Object System.Drawing.Rectangle $x0, $y0, $w, $h
$crop = New-Object System.Drawing.Bitmap ($w * $scale), ($h * $scale)
$g = [System.Drawing.Graphics]::FromImage($crop)
$g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::NearestNeighbor
$dst = New-Object System.Drawing.Rectangle 0, 0, ($w * $scale), ($h * $scale)
$g.DrawImage($img, $dst, $rect, [System.Drawing.GraphicsUnit]::Pixel)
$crop.Save("D:\GitCode\ptp_chat\crop.png", [System.Drawing.Imaging.ImageFormat]::Png)
$g.Dispose()
$crop.Dispose()
$img.Dispose()
Write-Output "cropped"
