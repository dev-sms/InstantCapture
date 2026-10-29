# Zips the build output for a release. Only the files listed here are
# packaged, so the runtime config.json in dist\ (local save path, saved
# regions) is never shipped.
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent

$winres = Get-Content (Join-Path $root 'winres.json') -Raw | ConvertFrom-Json
$version = $winres.RT_VERSION.'#1'.'0000'.info.'0409'.ProductVersion
$name = "InstantCapture-$version-windows-x64"
$release = Join-Path $root 'release'
$stage = Join-Path $release $name
$zip = Join-Path $release "$name.zip"

$files = @(
    'dist\instantcapture.exe',
    'dist\instantcapture-window-helper.exe',
    'README.md',
    'LICENSE',
    'THIRD_PARTY_NOTICES.md'
) | ForEach-Object { Join-Path $root $_ }

if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage -Force | Out-Null
Copy-Item -LiteralPath $files -Destination $stage
if (Test-Path $zip) { Remove-Item $zip -Force }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip
Remove-Item $stage -Recurse -Force

$hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -Path "$zip.sha256" -Value "$hash  $name.zip" -Encoding ascii
Write-Host "Release package: $zip"
Write-Host "SHA256: $hash"
