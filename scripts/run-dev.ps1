<#
.SYNOPSIS
    Build and run Steinel Bridge locally on Windows (requires CGo / MinGW GCC)
#>
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RootDir = Split-Path -Parent $ScriptDir
$SdkDir = Join-Path $RootDir ".sdk"

$HeaderFile = Join-Path $SdkDir "include\nabto\nabto_client.h"
$LibDir = Join-Path $SdkDir "lib"

if (-not (Test-Path $HeaderFile) -or -not (Test-Path $LibDir)) {
    Write-Host "[*] Nabto SDK not found. Running setup-sdk.ps1..." -ForegroundColor Yellow
    & (Join-Path $ScriptDir "setup-sdk.ps1")
}

Set-Location $RootDir

Write-Host "==================================================" -ForegroundColor Cyan
Write-Host " Building & Starting Steinel Bridge (Windows Dev)" -ForegroundColor Cyan
Write-Host " Output RTSP: rtsp://127.0.0.1:8554/steinel" -ForegroundColor Cyan
Write-Host "==================================================" -ForegroundColor Cyan

# Add SDK lib directory to PATH so Windows can resolve nabto_client.dll at runtime
$env:PATH = "$LibDir;" + $env:PATH

go build -o steinel-bridge.exe ./cmd/steinel-bridge

& .\steinel-bridge.exe $args
