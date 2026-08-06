# watchdog.ps1 — liveness watchdog for kpbot/research (PLAN_v4, Phase 0).
#
# Checks heartbeat-file FRESHNESS, not process presence in the OS list:
# a "running" but hung process (scheduler deadlock, endless HTTP request)
# does not refresh its heartbeat file, so the watchdog restarts it.
# A dead process does not refresh either — one criterion covers both.
#
# Freshness thresholds: kpbot <= 3 min, research <= 15 min (heartbeat is
# written every minute from a dedicated goroutine, independent of
# anti-bot pauses).
#
# Restart order: stop -> rotate logs -> start (a file held open by a live
# process' redirected output cannot be renamed on Windows).
#
# Log messages are ASCII on purpose: Windows PowerShell 5.1 reads BOM-less
# scripts as ANSI and would mangle non-ASCII text.
#
# Scheduled via Task Scheduler every 10 minutes (registration — Phase 6):
#   schtasks /Create /TN "KPBot Watchdog" /SC MINUTE /MO 10 ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File D:\the_bot_god_of_laptop\tools\watchdog.ps1"

$ErrorActionPreference = 'Stop'
$root  = Split-Path -Parent $PSScriptRoot
$data  = Join-Path $root 'data'
$wlog  = Join-Path $data 'watchdog.log'

$thresholdMin = @{ 'kpbot' = 3; 'research' = 15 }
$exeName      = @{ 'kpbot' = 'kpbot.exe'; 'research' = 'research.exe' }
$outLogName   = @{ 'kpbot' = 'bot.log'; 'research' = 'research_details.log' }
$errLogName   = @{ 'kpbot' = 'bot.err.log'; 'research' = 'research_details.err.log' }

function Write-WatchLog([string]$msg) {
    $line = '{0} {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg
    Add-Content -Path $wlog -Value $line -Encoding Ascii
}

function Restart-Service([string]$name) {
    Get-Process $name -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 2
    foreach ($f in @((Join-Path $data $outLogName[$name]), (Join-Path $data $errLogName[$name]))) {
        if (Test-Path $f) {
            Move-Item $f "$f.1" -Force   # rotate: history is preserved
        }
    }
    Start-Process (Join-Path $root $exeName[$name]) -WorkingDirectory $root -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $data $outLogName[$name]) `
        -RedirectStandardError  (Join-Path $data $errLogName[$name])
    Write-WatchLog "${name}: restarted (logs rotated)"
}

foreach ($name in @('kpbot', 'research')) {
    $hb    = Join-Path $data "$name.heartbeat"
    $fresh = $false
    if (Test-Path $hb) {
        $ageMin = ((Get-Date) - (Get-Item $hb).LastWriteTime).TotalMinutes
        if ($ageMin -le $thresholdMin[$name]) {
            $fresh = $true
        } else {
            Write-WatchLog "${name}: heartbeat stale ($([math]::Round($ageMin, 1)) min > $($thresholdMin[$name]) min)"
        }
    } else {
        Write-WatchLog "${name}: heartbeat file missing"
    }
    if ($fresh) { continue }
    Restart-Service $name
}
