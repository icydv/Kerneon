param(
    [Parameter(Mandatory=$true)][string]$ApplicationExecutable,
    [Parameter(Mandatory=$true)][string]$CanonicalMark,
    [Parameter(Mandatory=$true)][string]$OutputDirectory
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$resolvedExecutable = [IO.Path]::GetFullPath($ApplicationExecutable)
$resolvedMark = [IO.Path]::GetFullPath($CanonicalMark)
$resolvedOutput = [IO.Path]::GetFullPath($OutputDirectory)
if (-not (Test-Path -LiteralPath $resolvedExecutable)) { throw "Application executable was not found: $resolvedExecutable" }
if (-not (Test-Path -LiteralPath $resolvedMark)) { throw "Canonical mark was not found: $resolvedMark" }
[IO.Directory]::CreateDirectory($resolvedOutput) | Out-Null

function New-RoundedPath([Drawing.RectangleF]$Rectangle, [float]$Radius) {
    $path = New-Object Drawing.Drawing2D.GraphicsPath
    $diameter = $Radius * 2
    $path.AddArc($Rectangle.X, $Rectangle.Y, $diameter, $diameter, 180, 90)
    $path.AddArc($Rectangle.Right-$diameter, $Rectangle.Y, $diameter, $diameter, 270, 90)
    $path.AddArc($Rectangle.Right-$diameter, $Rectangle.Bottom-$diameter, $diameter, $diameter, 0, 90)
    $path.AddArc($Rectangle.X, $Rectangle.Bottom-$diameter, $diameter, $diameter, 90, 90)
    $path.CloseFigure()
    return $path
}

function New-KerneonBackdrop([string]$Path, [int]$Width, [int]$Height, [bool]$StandardLayout) {
    $bitmap = New-Object Drawing.Bitmap $Width, $Height, ([Drawing.Imaging.PixelFormat]::Format24bppRgb)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    $graphics.SmoothingMode = [Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.InterpolationMode = [Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $graphics.PixelOffsetMode = [Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $bounds = New-Object Drawing.Rectangle 0, 0, $Width, $Height
    $gradient = New-Object Drawing.Drawing2D.LinearGradientBrush $bounds, ([Drawing.Color]::FromArgb(7,11,13)), ([Drawing.Color]::FromArgb(17,24,27)), 22
    $graphics.FillRectangle($gradient, $bounds)

    $glowPath = New-Object Drawing.Drawing2D.GraphicsPath
    $glowDiameter = if ($Height -gt 100) { 218 } else { 104 }
    $glowPath.AddEllipse(-$glowDiameter/2, ($Height-$glowDiameter)/2, $glowDiameter, $glowDiameter)
    $glowBrush = New-Object Drawing.Drawing2D.PathGradientBrush $glowPath
    $glowBrush.CenterColor = [Drawing.Color]::FromArgb(62, 92, 220, 230)
    $glowBrush.SurroundColors = @([Drawing.Color]::FromArgb(0, 92, 220, 230))
    $graphics.FillPath($glowBrush, $glowPath)
    $glowBrush.Dispose(); $glowPath.Dispose()

    if ($StandardLayout) {
        $lightRectangle = New-Object Drawing.Rectangle 158, 0, ($Width-158), $Height
        $lightGradient = New-Object Drawing.Drawing2D.LinearGradientBrush $lightRectangle, ([Drawing.Color]::FromArgb(244,248,249)), ([Drawing.Color]::FromArgb(226,234,236)), 90
        $graphics.FillRectangle($lightGradient, $lightRectangle)
        $lightGradient.Dispose()
        $dividerPen = New-Object Drawing.Pen ([Drawing.Color]::FromArgb(94, 117, 209, 218)), 1
        $graphics.DrawLine($dividerPen, 158, 0, 158, $Height)
        $dividerPen.Dispose()
    } else {
        $panelRectangle = New-Object Drawing.RectangleF 158, 10, 317, ($Height-16)
        $panelPath = New-RoundedPath $panelRectangle 18
        $panelBrush = New-Object Drawing.SolidBrush ([Drawing.Color]::FromArgb(186, 16,23,26))
        $panelPen = New-Object Drawing.Pen ([Drawing.Color]::FromArgb(72, 112, 220, 228)), 1
        $graphics.FillPath($panelBrush, $panelPath)
        $graphics.DrawPath($panelPen, $panelPath)
        $panelPen.Dispose(); $panelBrush.Dispose(); $panelPath.Dispose()
    }

    $markImage = [Drawing.Image]::FromFile($resolvedMark)
    $markSize = if ($Height -gt 100) { 102 } else { 42 }
    $markX = if ($Height -gt 100) { 26 } else { 17 }
    $markY = [int](($Height-$markSize)/2)
    $graphics.DrawImage($markImage, $markX, $markY, $markSize, $markSize)
    $markImage.Dispose()

    $edgePen = New-Object Drawing.Pen ([Drawing.Color]::FromArgb(44, 112, 220, 228)), 1
    $graphics.DrawLine($edgePen, 0, $Height-1, $Width, $Height-1)
    $edgePen.Dispose(); $gradient.Dispose(); $graphics.Dispose()
    $bitmap.Save($Path, [Drawing.Imaging.ImageFormat]::Bmp)
    $bitmap.Dispose()
}

New-KerneonBackdrop (Join-Path $resolvedOutput 'dialog.bmp') 493 224 $false
New-KerneonBackdrop (Join-Path $resolvedOutput 'dialog-standard.bmp') 493 312 $true
New-KerneonBackdrop (Join-Path $resolvedOutput 'banner.bmp') 493 58 $true

$applicationIcon = [Drawing.Icon]::ExtractAssociatedIcon($resolvedExecutable)
$iconStream = [IO.File]::Create((Join-Path $resolvedOutput 'Kerneon.ico'))
try { $applicationIcon.Save($iconStream) } finally { $iconStream.Dispose(); $applicationIcon.Dispose() }
