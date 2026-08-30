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

Push-Location $projectRoot
try {
    & $go test -count=1 -v ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    # The ADLX/Win32 FFI deliberately reconstructs vendor-owned pointers from
    # uintptr vtable results. Disable only vet's unsafeptr heuristic.
    & $go vet -unsafeptr=false ./...
    if ($LASTEXITCODE -ne 0) { throw 'Static analysis failed.' }
} finally {
    Pop-Location
}
