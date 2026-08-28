param(
    [double]$DurationMinutes = 120,
    [int]$IntervalSeconds = 5,
    [string]$Executable = (Join-Path $PSScriptRoot '..\dist\Kerneon.exe'),
    [string]$Output = (Join-Path $PSScriptRoot ('..\artifacts\soak-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.csv')),
    [switch]$Minimize
)

$ErrorActionPreference = 'Stop'
$exePath = [IO.Path]::GetFullPath($Executable)
$outputPath = [IO.Path]::GetFullPath($Output)
if (-not (Test-Path -LiteralPath $exePath)) { throw "Executable not found: $exePath. Run scripts\build.ps1 first." }
if ($DurationMinutes -le 0 -or $IntervalSeconds -lt 1) { throw 'Duration and interval must be positive.' }
New-Item -ItemType Directory -Force -Path ([IO.Path]::GetDirectoryName($outputPath)) | Out-Null

Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class KerneonSoakNative {
    [DllImport("user32.dll")] public static extern int GetGuiResources(IntPtr process, int flag);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr window, int command);
}
'@

$process = Start-Process -FilePath $exePath -WorkingDirectory ([IO.Path]::GetDirectoryName($exePath)) -PassThru -WindowStyle Normal
$samples = [Collections.Generic.List[object]]::new()
$logical = [Environment]::ProcessorCount
$deadline = (Get-Date).AddMinutes($DurationMinutes)
$lastAt = Get-Date
$lastCpu = [TimeSpan]::Zero
try {
    Start-Sleep -Seconds 2
    $process.Refresh()
    $lastCpu = $process.TotalProcessorTime
    $lastAt = Get-Date
    if ($Minimize) { [KerneonSoakNative]::ShowWindow($process.MainWindowHandle, 6) | Out-Null }
    while ((Get-Date) -lt $deadline -and -not $process.HasExited) {
        Start-Sleep -Seconds $IntervalSeconds
        $process.Refresh()
        $now = Get-Date
        $elapsed = ($now - $lastAt).TotalSeconds
        $cpu = if ($elapsed -gt 0) { 100 * ($process.TotalProcessorTime - $lastCpu).TotalSeconds / $elapsed / $logical } else { 0 }
        $samples.Add([pscustomobject]@{
            Timestamp = $now.ToUniversalTime().ToString('o')
            CPUPercent = [math]::Round($cpu, 4)
            WorkingSetMB = [math]::Round($process.WorkingSet64 / 1MB, 3)
            PrivateMemoryMB = [math]::Round($process.PrivateMemorySize64 / 1MB, 3)
            Handles = $process.HandleCount
            Threads = $process.Threads.Count
            GDIObjects = [KerneonSoakNative]::GetGuiResources($process.Handle, 0)
            Responding = $process.Responding
            WindowVisible = $process.MainWindowHandle -ne 0
        })
        $lastCpu = $process.TotalProcessorTime
        $lastAt = $now
    }
} finally {
    $samples | Export-Csv -NoTypeInformation -Encoding UTF8 -LiteralPath $outputPath
    if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force }
}

$samples | Measure-Object CPUPercent,WorkingSetMB,PrivateMemoryMB,Handles,Threads,GDIObjects -Minimum -Maximum -Average
Get-Item -LiteralPath $outputPath
