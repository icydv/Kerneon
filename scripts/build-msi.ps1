param(
    [string]$ApplicationExecutable,
    [string]$Output
)

$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$outputRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot 'dist'))
if (-not $ApplicationExecutable) { $ApplicationExecutable = Join-Path $outputRoot 'Kerneon-0.1.0-preview.1-portable.exe' }
if (-not $Output) { $Output = Join-Path $outputRoot 'Kerneon-0.1.0-preview.1-x64.msi' }

$resolvedApplication = [IO.Path]::GetFullPath($ApplicationExecutable)
$resolvedOutput = [IO.Path]::GetFullPath($Output)
$installerDirectory = Join-Path $projectRoot 'installer'
$assetDirectory = Join-Path $installerDirectory 'assets'
$objectDirectory = Join-Path $installerDirectory 'obj'
$wixBin = 'C:\Program Files (x86)\WiX Toolset v3.14\bin'
$candle = Join-Path $wixBin 'candle.exe'
$light = Join-Path $wixBin 'light.exe'
$smoke = Join-Path $wixBin 'smoke.exe'
foreach ($required in @($resolvedApplication, $candle, $light, $smoke)) {
    if (-not (Test-Path -LiteralPath $required)) { throw "Required MSI build input was not found: $required" }
}

[IO.Directory]::CreateDirectory($assetDirectory) | Out-Null
[IO.Directory]::CreateDirectory($objectDirectory) | Out-Null
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null

& (Join-Path $installerDirectory 'Generate-InstallerAssets.ps1') -ApplicationExecutable $resolvedApplication -CanonicalMark (Join-Path $projectRoot 'assets\kerneon-icon.png') -OutputDirectory $assetDirectory

$definitions = @(
    "-dAppExe=$resolvedApplication",
    "-dRootDir=$projectRoot",
    "-dInstallerDir=$installerDirectory",
    "-dInstallerAssetDir=$assetDirectory"
)
$productObject = Join-Path $objectDirectory 'Product.wixobj'
$uiObject = Join-Path $objectDirectory 'UI.wixobj'
& $candle -nologo -arch x64 @definitions -out $objectDirectory\ (Join-Path $installerDirectory 'Product.wxs') (Join-Path $installerDirectory 'UI.wxs')
if ($LASTEXITCODE -ne 0) { throw 'WiX compilation failed.' }

$nextOutput = [IO.Path]::ChangeExtension($resolvedOutput, '.next.msi')
$pdbOutput = Join-Path $objectDirectory 'Kerneon.wixpdb'
& $light -nologo -ext WixUIExtension -cultures:en-us -sice:ICE61 -pdbout $pdbOutput -out $nextOutput $productObject $uiObject
if ($LASTEXITCODE -ne 0) { throw 'WiX linking or MSI validation failed.' }
& $smoke -nologo -cub (Join-Path $wixBin 'darice.cub') $nextOutput
if ($LASTEXITCODE -ne 0) { throw 'MSI ICE validation failed.' }

Move-Item -LiteralPath $nextOutput -Destination $resolvedOutput -Force
$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolvedOutput).Hash
Write-Output "MSI=$resolvedOutput"
Write-Output "SHA256=$hash"
