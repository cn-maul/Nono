#Requires -Version 5.1
<#
  Build Nono.

  This script always runs under PowerShell 7: when started from Windows
  PowerShell it re-launches itself with pwsh.

  Examples:
    .\build.ps1
    .\build.ps1 -Test
    .\build.ps1 -NoStart
    .\build.ps1 -Output dist\nono.exe
#>
param(
    [switch]$Test,
    [switch]$NoStart,
    [switch]$ShowConsole,
    [string]$Output = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# ---------------------------------------------------------------------------
# Prefer PowerShell 7. The bootstrap keeps Windows PowerShell syntax only, so
# the file still parses under 5.1 before the re-launch.
# ---------------------------------------------------------------------------
if ($PSVersionTable.PSVersion.Major -lt 7) {
    $pwsh = Get-Command pwsh -ErrorAction SilentlyContinue
    if (-not $pwsh) {
        Write-Host "PowerShell 7 (pwsh) is required. Install it first:" -ForegroundColor Red
        Write-Host "  winget install --id Microsoft.PowerShell --source winget" -ForegroundColor Yellow
        exit 1
    }
    $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $PSCommandPath)
    if ($Test) { $arguments += "-Test" }
    if ($NoStart) { $arguments += "-NoStart" }
    if ($ShowConsole) { $arguments += "-ShowConsole" }
    if ($PSBoundParameters.ContainsKey("Output")) { $arguments += @("-Output", $Output) }
    & $pwsh.Source @arguments
    exit $LASTEXITCODE
}

$root = $PSScriptRoot
Set-Location -LiteralPath $root

if ([string]::IsNullOrWhiteSpace($Output)) {
    $Output = Join-Path $root "nono.exe"
} elseif (-not [System.IO.Path]::IsPathRooted($Output)) {
    $Output = Join-Path $root $Output
}

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
    throw "go was not found in PATH."
}

Write-Host "Nono build" -ForegroundColor Cyan
Write-Host ("  go      : " + (& go version))
$version = & git describe --tags --always --dirty 2>$null
if ($LASTEXITCODE -eq 0 -and $version) {
    Write-Host ("  version : " + $version)
}
Write-Host ("  output  : " + $Output)

# The running executable is locked on Windows, so stop it before building.
$wasRunning = $false
$procs = @(Get-Process -Name "nono" -ErrorAction SilentlyContinue)
if ($procs.Count -gt 0) {
    $wasRunning = $true
    $procs | Stop-Process -Force
    Start-Sleep -Milliseconds 400
    Write-Host "  stopped running Nono"
}

$watch = [System.Diagnostics.Stopwatch]::StartNew()
$tempOutput = $Output + ".tmp"

try {
    if ($Test) {
        Write-Host "Running go test ./..." -ForegroundColor Cyan
        & go test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "go test failed."
        }
    }

    if (Test-Path -LiteralPath $tempOutput) {
        Remove-Item -LiteralPath $tempOutput -Force
    }
    $outputDir = Split-Path -Parent $Output
    if ($outputDir -and -not (Test-Path -LiteralPath $outputDir)) {
        New-Item -ItemType Directory -Path $outputDir | Out-Null
    }

    Write-Host "Building..." -ForegroundColor Cyan
    & go build -trimpath -ldflags "-s -w" -o $tempOutput .
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed."
    }
    Move-Item -LiteralPath $tempOutput -Destination $Output -Force
} catch {
    if (Test-Path -LiteralPath $tempOutput) {
        Remove-Item -LiteralPath $tempOutput -Force -ErrorAction SilentlyContinue
    }
    if ($wasRunning -and (Test-Path -LiteralPath $Output)) {
        Start-Process -FilePath $Output -WorkingDirectory $root | Out-Null
        Write-Host "Restarted the previous build after the failure." -ForegroundColor Yellow
    }
    throw
}

$watch.Stop()
$file = Get-Item -LiteralPath $Output
$hash = (Get-FileHash -LiteralPath $Output -Algorithm SHA256).Hash.Substring(0, 12)
Write-Host ("Built {0} ({1:N1} MB) in {2:N1}s  sha256:{3}" -f $file.Name, ($file.Length / 1MB), $watch.Elapsed.TotalSeconds, $hash) -ForegroundColor Green

if ($NoStart) {
    if ($wasRunning) {
        Write-Host "Nono was running before the build and is now stopped (-NoStart)." -ForegroundColor Yellow
    }
} else {
    if ($ShowConsole) {
        $proc = Start-Process -FilePath $Output -WorkingDirectory $root -PassThru
    } else {
        # A hidden console keeps the app detached from the caller's terminal.
        $proc = Start-Process -FilePath $Output -WorkingDirectory $root -WindowStyle Hidden -PassThru
    }
    Start-Sleep -Milliseconds 800
    if (-not (Get-Process -Id $proc.Id -ErrorAction SilentlyContinue)) {
        Write-Host "Nono exited right after starting." -ForegroundColor Red
        exit 1
    }
    Write-Host ("Started Nono (pid {0})." -f $proc.Id) -ForegroundColor Green
}
