param(
    [string]$Output = (Join-Path $PSScriptRoot '..\dist\Kerneon-0.1.0-preview.1-portable.exe')
)

$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$go = if ($env:KERNEON_GO) {
    $env:KERNEON_GO
} elseif (Get-Command go -ErrorAction SilentlyContinue) {
    (Get-Command go).Source
} else {
    [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\toolchains\go\bin\go.exe'))
}
if (-not (Test-Path -LiteralPath $go)) { throw 'Go was not found. Install Go 1.27+ or set KERNEON_GO.' }

$resolvedOutput = [IO.Path]::GetFullPath($Output)
New-Item -ItemType Directory -Force -Path ([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
Push-Location $projectRoot
try {
    & $go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    # The ADLX/Win32 FFI deliberately reconstructs vendor-owned pointers from
    # uintptr vtable results. Disable only vet's unsafeptr heuristic; every
    # other standard analyzer remains enabled.
    & $go vet -unsafeptr=false ./...
    if ($LASTEXITCODE -ne 0) { throw 'Static analysis failed.' }
    & $go build -buildvcs=false -trimpath -ldflags '-s -w -H=windowsgui' -o $resolvedOutput .
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
    Get-Item -LiteralPath $resolvedOutput
} finally {
    Pop-Location
}
