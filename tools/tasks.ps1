# tasks.ps1 — обёртки для задач планировщика Windows (PLAN_v4, Фаза 6).
# Планировщик стартует процессы с CWD=System32, поэтому все пути строятся
# от расположения скрипта. Heartbeat у фоновых прогонов ОТДЕЛЬНЫЙ, чтобы
# не маскировать зависание основного research для watchdog.
#
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\tasks.ps1 -Job research-refresh
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\tasks.ps1 -Job hwdb

param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('research-refresh', 'hwdb')]
    [string]$Job
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

switch ($Job) {
    'research-refresh' {
        # Бэкфилл search-полей всего рынка (~20 мин, только Search API).
        & (Join-Path $root 'research.exe') -search-refresh `
            -heartbeat (Join-Path $root 'data\research_refresh.heartbeat')
        exit $LASTEXITCODE
    }
    'hwdb' {
        # Ежемесячное обновление эталона мощности CPU/GPU (самопроверка внутри).
        & (Join-Path $root 'hwdb.exe')
        exit $LASTEXITCODE
    }
}
