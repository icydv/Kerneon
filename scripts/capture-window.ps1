param(
    [Parameter(Mandatory=$true)][string]$Executable,
    [Parameter(Mandatory=$true)][string]$Output,
    [int]$TargetProcessId = 0,
    [long]$TargetWindowHandle = 0,
    [int]$ClickX = -1,
    [int]$ClickY = -1,
    [int]$HoverX = -1,
    [int]$HoverY = -1
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class KerneonCapture {
    [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hwnd, out RECT rect);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hwnd, IntPtr hdc, uint flags);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hwnd, uint msg, UIntPtr wparam, IntPtr lparam);
}
'@

$resolved = [IO.Path]::GetFullPath($Executable)
$process = if ($TargetProcessId -gt 0) { Get-Process -Id $TargetProcessId -ErrorAction SilentlyContinue } else { Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $resolved -and $_.MainWindowHandle -ne [IntPtr]::Zero } | Select-Object -First 1 }
if (-not $process) {
    $process = Start-Process -FilePath $resolved -PassThru
    Start-Sleep -Milliseconds 1800
    $process.Refresh()
}
$hwnd = if ($TargetWindowHandle -ne 0) { [IntPtr]$TargetWindowHandle } else { $process.MainWindowHandle }
if ($hwnd -eq [IntPtr]::Zero) { throw 'Kerneon main window was not available.' }
if ($ClickX -ge 0 -and $ClickY -ge 0) {
    $packed = [IntPtr](($ClickY -shl 16) -bor ($ClickX -band 0xffff))
	[KerneonCapture]::PostMessage($hwnd, 0x0200, [UIntPtr]::Zero, $packed) | Out-Null
	Start-Sleep -Milliseconds 80
	[KerneonCapture]::PostMessage($hwnd, 0x0201, ([UIntPtr]::new([uint64]1)), $packed) | Out-Null
	Start-Sleep -Milliseconds 55
    [KerneonCapture]::PostMessage($hwnd, 0x0202, [UIntPtr]::Zero, $packed) | Out-Null
    Start-Sleep -Milliseconds 450
}
if ($HoverX -ge 0 -and $HoverY -ge 0) {
    $packed = [IntPtr](($HoverY -shl 16) -bor ($HoverX -band 0xffff))
    [KerneonCapture]::PostMessage($hwnd, 0x0200, [UIntPtr]::Zero, $packed) | Out-Null
    Start-Sleep -Milliseconds 180
}
$rect = New-Object KerneonCapture+RECT
if (-not [KerneonCapture]::GetWindowRect($hwnd, [ref]$rect)) { throw 'Could not read the window bounds.' }
$bitmap = New-Object Drawing.Bitmap ($rect.Right-$rect.Left), ($rect.Bottom-$rect.Top), ([Drawing.Imaging.PixelFormat]::Format32bppArgb)
$graphics = [Drawing.Graphics]::FromImage($bitmap)
$hdc = $graphics.GetHdc()
try {
    if (-not [KerneonCapture]::PrintWindow($hwnd, $hdc, 2)) { throw 'PrintWindow failed.' }
} finally {
    $graphics.ReleaseHdc($hdc)
    $graphics.Dispose()
}
$bitmap.Save([IO.Path]::GetFullPath($Output), [Drawing.Imaging.ImageFormat]::Png)
$bitmap.Dispose()
