$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$go = if ($env:KERNEON_GO) { $env:KERNEON_GO } elseif (Get-Command go -ErrorAction SilentlyContinue) { (Get-Command go).Source } else { [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\toolchains\go\bin\go.exe')) }
$winres = if ($env:KERNEON_WINRES) { $env:KERNEON_WINRES } elseif (Get-Command go-winres -ErrorAction SilentlyContinue) { (Get-Command go-winres).Source } else { [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\toolchains\bin\go-winres.exe')) }
if (-not (Test-Path -LiteralPath $go)) { throw 'Go was not found.' }
if (-not (Test-Path -LiteralPath $winres)) { throw 'go-winres v0.3.3 was not found. Set KERNEON_WINRES.' }

Push-Location $projectRoot
try {
    & $go run .\cmd\icon
    if ($LASTEXITCODE -ne 0) { throw 'Icon generation failed.' }
    & $winres simply --arch amd64 --out rsrc --manifest gui --file-description 'Kerneon system performance monitor' --product-name Kerneon --copyright 'Copyright 2026 Kerneon' --original-filename Kerneon.exe --icon assets\kerneon-icon.png --product-version 1.0.0 --file-version 1.0.0
    if ($LASTEXITCODE -ne 0) { throw 'Resource generation failed.' }
} finally {
    Pop-Location
}
