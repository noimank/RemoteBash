<#
.SYNOPSIS
    RemoteBash cross-platform build script (Windows PowerShell)
.DESCRIPTION
    Cross-compiles the Go project into static binaries for all major platforms.
    Output goes to build/ with a SHA256 checksum file.
.PARAMETER Version
    Version string; defaults to latest git tag, falls back to "dev".
.PARAMETER OutputDir
    Output directory; defaults to "build".
.PARAMETER Platforms
    Comma-separated target platforms; defaults to all.
    Options: linux/amd64, linux/arm64, linux/armv7, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64
.EXAMPLE
    .\scripts\build.ps1
.EXAMPLE
    .\scripts\build.ps1 -Version "v2.1.0"
.EXAMPLE
    .\scripts\build.ps1 -Platforms "linux/amd64,windows/amd64"
#>

param(
    [string]$Version = "",
    [string]$OutputDir = "build",
    [string]$Platforms = ""
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Resolve-Path "$ScriptDir\.."

Push-Location $ProjectRoot

# Version: param > git tag > "dev"
if (-not $Version) {
    try {
        $Version = git describe --tags --abbrev=0 2>$null
        if (-not $Version) { throw "no tag" }
    } catch {
        $Version = "dev"
    }
}
Write-Host "=== RemoteBash Build Script ===" -ForegroundColor Cyan
Write-Host "Version: $Version"
Write-Host "Output:   $OutputDir"
Write-Host ""

# Platform definitions
$allPlatforms = @(
    @{ GOOS = "linux";   GOARCH = "amd64"; Suffix = "_linux_amd64";     Ext = "" },
    @{ GOOS = "linux";   GOARCH = "arm64"; Suffix = "_linux_arm64";     Ext = "" },
    @{ GOOS = "linux";   GOARCH = "arm";   Suffix = "_linux_armv7";     Ext = "";  GOARM = "7" },
    @{ GOOS = "darwin";  GOARCH = "amd64"; Suffix = "_darwin_amd64";    Ext = "" },
    @{ GOOS = "darwin";  GOARCH = "arm64"; Suffix = "_darwin_arm64";    Ext = "" },
    @{ GOOS = "windows"; GOARCH = "amd64"; Suffix = "_windows_amd64";   Ext = ".exe" },
    @{ GOOS = "windows"; GOARCH = "arm64"; Suffix = "_windows_arm64";   Ext = ".exe" }
)

$targets = @($allPlatforms)
if ($Platforms) {
    $filter = $Platforms -split "," | ForEach-Object { $_.Trim() }
    $targets = @($allPlatforms | Where-Object { "$($_.GOOS)/$($_.GOARCH)" -in $filter })
    if ($targets.Count -eq 0) {
        $valid = ($allPlatforms | ForEach-Object { "$($_.GOOS)/$($_.GOARCH)" }) -join ", "
        Write-Host "ERROR: no matching platform. Valid: $valid" -ForegroundColor Red
        Pop-Location
        exit 1
    }
}

# Create output directory
$null = New-Item -ItemType Directory -Force -Path $OutputDir

# Build flags (strip debug info, shrink binary)
$ldflags = "-s -w"
$buildTime = (Get-Date -Format "yyyy-MM-dd HH:mm:ss UTC")

Write-Host "Targets:  $($targets.Count)"
Write-Host "LDFLAGS:  $ldflags"
Write-Host ""

# Build each platform
$successCount = 0
$failCount = 0
$binaries = @()

foreach ($t in $targets) {
    $binaryName = "remotebash$($t.Suffix)$($t.Ext)"
    $outputPath = Join-Path $OutputDir $binaryName

    $label = "$($t.GOOS)/$($t.GOARCH)"
    if ($t.GOARM) { $label += " (GOARM=$($t.GOARM))" }

    Write-Host "[BUILD] $label  ->  $binaryName" -ForegroundColor Yellow

    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH
    $env:CGO_ENABLED = "0"
    if ($t.GOARM) { $env:GOARM = $t.GOARM } else { Remove-Item Env:\GOARM -ErrorAction SilentlyContinue }

    try {
        $buildArgs = @(
            "build",
            "-ldflags=$ldflags",
            "-o", $outputPath,
            "./cmd/remotebash/"
        )
        $result = & go $buildArgs 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw $result
        }

        $fileSize = (Get-Item $outputPath).Length
        $sizeKB = [math]::Round($fileSize / 1KB, 1)
        Write-Host "  [OK]  ($sizeKB KB)" -ForegroundColor Green

        $successCount++
        $binaries += @{
            Name     = $binaryName
            Path     = $outputPath
            Platform = $label
            Size     = $fileSize
        }
    } catch {
        Write-Host "  [FAIL] $_" -ForegroundColor Red
        $failCount++
    }
}

# Generate checksum file
if ($successCount -gt 0) {
    Write-Host ""
    Write-Host "=== Generate SHA256 Checksums ===" -ForegroundColor Cyan

    $checksumFile = Join-Path $OutputDir "checksums.txt"
    $checksums = @"
# RemoteBash $Version -- SHA256 Checksums
# Generated: $buildTime
#
# Verify:  sha256sum -c checksums.txt          (Linux/macOS)
#          certutil -hashfile <file> SHA256    (Windows)
#
"@

    foreach ($b in $binaries) {
        $hash = (Get-FileHash -Algorithm SHA256 $b.Path).Hash.ToLower()
        $checksums += "`n$hash  $($b.Name)"
    }

    $checksums | Out-File -FilePath $checksumFile -Encoding utf8
    Write-Host "  [OK] $checksumFile" -ForegroundColor Green
}

# Summary
Write-Host ""
Write-Host "=== Build Summary ===" -ForegroundColor Cyan
if ($failCount -eq 0) {
    Write-Host "OK: $successCount  FAIL: $failCount" -ForegroundColor Green
} else {
    Write-Host "OK: $successCount  FAIL: $failCount" -ForegroundColor Red
}
Write-Host "Output: $(Resolve-Path $OutputDir)"

Write-Host ""
Write-Host "Artifacts:"
foreach ($b in $binaries) {
    $sizeKB = [math]::Round($b.Size / 1KB, 1)
    Write-Host "  $($b.Name)  ($sizeKB KB)  [$($b.Platform)]"
}

Pop-Location

if ($failCount -gt 0) {
    exit 1
}
