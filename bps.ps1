# bps-local launcher for PowerShell.
#
#   .\bps.ps1            start the bridge (Ctrl+C to stop)
#   .\bps.ps1 -Check     verify credentials without starting
#   .\bps.ps1 -Status    show whether the bridge is running
#
# Run .\bps-codex.ps1 in a SECOND terminal to use Codex through the bridge.

param(
    [switch]$Check,
    [switch]$Status,
    [string]$Proxy = "http://127.0.0.1:12450",
    [string]$Addr  = "127.0.0.1:8790"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe  = Join-Path $root "bps-local.exe"

if (-not (Test-Path $exe)) {
    Write-Host "bps-local.exe not found. Build it first:" -ForegroundColor Red
    Write-Host "  go build -o bps-local.exe ./cmd/bps-local"
    exit 1
}

# Is anything already listening on our port?
function Get-BridgeProcess {
    $conn = Get-NetTCPConnection -LocalPort ([int]($Addr -replace '.*:', '')) `
                -State Listen -ErrorAction SilentlyContinue
    if ($conn) { return $conn | Select-Object -First 1 }
    return $null
}

if ($Status) {
    $conn = Get-BridgeProcess
    if ($conn) {
        $p = Get-Process -Id $conn.OwningProcess -ErrorAction SilentlyContinue
        Write-Host "bridge running: $Addr (pid $($conn.OwningProcess), $($p.ProcessName))" -ForegroundColor Green
    } else {
        Write-Host "bridge not running on $Addr" -ForegroundColor Yellow
    }
    exit 0
}

if ($Check) {
    & $exe --check
    exit $LASTEXITCODE
}

# Refuse to start a second instance: the first one already owns the port, and a
# confusing bind error is worse than a clear message.
$existing = Get-BridgeProcess
if ($existing) {
    Write-Host "A bridge is already listening on $Addr (pid $($existing.OwningProcess))." -ForegroundColor Yellow
    Write-Host "Stop it first:  Get-Process bps-local | Stop-Process -Force"
    exit 1
}

Write-Host "Starting bridge on $Addr via proxy $Proxy" -ForegroundColor Cyan
Write-Host "Keep this window open. Press Ctrl+C to stop." -ForegroundColor DarkGray
Write-Host ""

& $exe --addr $Addr --proxy $Proxy
