# Run Codex through the local Basispoints bridge.
#
# Start the bridge first, in another terminal:
#   .\bps.ps1
#
# Then, in this terminal:
#   .\bps-codex.ps1                        interactive Codex
#   .\bps-codex.ps1 exec "list files"      one-shot
#   .\bps-codex.ps1 -Model gpt-6-luna      different model
#   .\bps-codex.ps1 -Effort high           reasoning effort
#
# Everything that is not -Model / -Effort / -Port is forwarded to Codex.
#
# Do NOT use a bare `--` separator: PowerShell consumes it during parameter
# binding when the script is launched with -File, before this script runs.
# Write the Codex arguments directly instead:
#   .\bps-codex.ps1 exec --skip-git-repo-check "list files"

[CmdletBinding(PositionalBinding = $false)]
param(
    [string]$Model  = "gpt-6-astra",
    [string]$Effort = "",
    [int]$Port      = 8790,
    [Parameter(ValueFromRemainingArguments = $true)]
    [object[]]$Rest
)

$ErrorActionPreference = "Stop"
$addr = "127.0.0.1:$Port"

# Windows PowerShell 5.1 does not quote native-command arguments correctly: the
# double quotes inside our provider definition get stripped before codex.exe
# sees them, producing "invalid type: string". PowerShell 7 passes them through
# intact, so re-launch there when available.
if ($PSVersionTable.PSVersion.Major -lt 7) {
    $pwsh = Get-Command pwsh -ErrorAction SilentlyContinue
    if ($pwsh) {
        $fwd = [System.Collections.Generic.List[string]]::new()
        foreach ($kv in @(
            @{ n = 'Model';  v = $Model },
            @{ n = 'Effort'; v = $Effort },
            @{ n = 'Port';   v = $Port }
        )) {
            if ($kv.n -eq 'Effort' -and $kv.v -eq '') { continue }
            $fwd.Add("-$($kv.n)"); $fwd.Add([string]$kv.v)
        }
        foreach ($item in $Rest) {
            if ($null -eq $item) { continue }
            if ([string]$item -eq '--') { continue }
            $fwd.Add([string]$item)
        }
        & $pwsh.Source -NoProfile -ExecutionPolicy Bypass -File $MyInvocation.MyCommand.Path @fwd
        exit $LASTEXITCODE
    }
    Write-Host "PowerShell 7 not found; codex argument quoting will be incorrect." -ForegroundColor Yellow
    Write-Host "Install it with:  winget install Microsoft.PowerShell" -ForegroundColor Yellow
}

# Fail early and clearly when the bridge is down, instead of letting Codex emit
# a page of connection errors.
$listening = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
if (-not $listening) {
    Write-Host "No bridge listening on $addr." -ForegroundColor Red
    Write-Host "Start it first, in another terminal:" -ForegroundColor Yellow
    Write-Host "  .\bps.ps1"
    exit 1
}

$q = [char]34
$providerDef = 'model_providers.bps-local={name=' + $q + 'bps-local' + $q +
               ',base_url=' + $q + 'http://' + $addr + '/v1' + $q +
               ',wire_api=' + $q + 'responses' + $q +
               ',requires_openai_auth=false}'

$codexArgs = [System.Collections.Generic.List[string]]::new()
$codexArgs.Add('-c'); $codexArgs.Add('model_provider=bps-local')
$codexArgs.Add('-c'); $codexArgs.Add($providerDef)
$codexArgs.Add('-c'); $codexArgs.Add("model=$Model")
if ($Effort -ne '') {
    $codexArgs.Add('-c'); $codexArgs.Add("model_reasoning_effort=$Effort")
}
foreach ($item in $Rest) {
    if ($null -eq $item) { continue }
    # PowerShell can surface a literal "--" separator in the remaining
    # arguments; Codex does not understand it, so drop it.
    if ([string]$item -eq '--') { continue }
    $codexArgs.Add([string]$item)
}

& codex @codexArgs
exit $LASTEXITCODE
