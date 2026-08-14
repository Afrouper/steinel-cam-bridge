<#
.SYNOPSIS
    Setup Nabto Edge Client SDK for Local Development on Windows (x64)
#>
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RootDir = Split-Path -Parent $ScriptDir
$SdkDir = Join-Path $RootDir ".sdk"
$NabtoTag = "main"

Write-Host "==================================================" -ForegroundColor Cyan
Write-Host " Setting up Nabto Edge Client SDK ($NabtoTag) for Windows" -ForegroundColor Cyan
Write-Host " Target directory: $SdkDir" -ForegroundColor Cyan
Write-Host "==================================================" -ForegroundColor Cyan

$IncludeDir = Join-Path $SdkDir "include\nabto"
$LibDir = Join-Path $SdkDir "lib"
$TmpDir = Join-Path $SdkDir "tmp"

New-Item -ItemType Directory -Force -Path $IncludeDir | Out-Null
New-Item -ItemType Directory -Force -Path $LibDir | Out-Null
New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null

$DownloadUrl = "https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/$NabtoTag.zip"
$ZipPath = Join-Path $TmpDir "nabto-sdk.zip"

Write-Host "[*] Downloading Nabto Client SDK release archive..."
Invoke-WebRequest -Uri $DownloadUrl -OutFile $ZipPath -UseBasicParsing

Write-Host "[*] Extracting SDK artifacts..."
Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

$ExtractedRoot = Get-ChildItem -Path $TmpDir -Directory -Filter "nabto-client-sdk-releases*" | Select-Object -First 1

# Copy header files
$HeaderSource = Join-Path $ExtractedRoot.FullName "include\nabto"
if (Test-Path $HeaderSource) {
    Copy-Item -Path "$HeaderSource\*" -Destination $IncludeDir -Recurse -Force
}

# Copy Windows x86_64 library and DLL files
$Win64LibDir = Join-Path $ExtractedRoot.FullName "lib\windows-x86_64"
if (Test-Path $Win64LibDir) {
    Copy-Item -Path "$Win64LibDir\*" -Destination $LibDir -Recurse -Force
}

# Cleanup
Remove-Item -Path $TmpDir -Recurse -Force

Write-Host "==================================================" -ForegroundColor Green
Write-Host " [OK] Nabto SDK successfully installed into .sdk/" -ForegroundColor Green
Write-Host " Header:  $IncludeDir\nabto_client.h" -ForegroundColor Green
Write-Host " Library: $LibDir" -ForegroundColor Green
Write-Host "==================================================" -ForegroundColor Green
