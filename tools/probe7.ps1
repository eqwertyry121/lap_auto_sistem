$ErrorActionPreference = 'Continue'

$dir = 'D:\the_bot_god_of_laptop\tools\js'
$files = Get-ChildItem $dir -Filter *.js

# Ищем по всем файлам ключевые механизмы
$targets = @{
    'generateAuth' = 'generateAuth'
    'get-token'    = 'get-token'
    'x-kp-session' = 'x-kp-session'
    'kpSession'    = 'kpSession'
    'anonymous'    = 'anonymous'
    'guest'        = 'guest'
}

foreach ($f in $files) {
    $content = [System.IO.File]::ReadAllText($f.FullName)
    foreach ($kw in $targets.Keys) {
        $idx = $content.IndexOf($kw)
        if ($idx -ge 0) {
            Write-Host ("### [" + $f.Name + "] '$kw' at $idx")
        }
    }
}
Write-Host '--- now deep-dive generateAuth / get-token / session bootstrap ---'

# Теперь детально: ищем generateAuth реализацию и apiRequest base
foreach ($f in $files) {
    $content = [System.IO.File]::ReadAllText($f.FullName)
    foreach ($kw in @('generateAuth', 'get-token', 'kpSession=', 'machine_id', 'x-kp-machine-id', 'api/web/v1/search')) {
        $idx = 0
        $count = 0
        while (($idx = $content.IndexOf($kw, $idx)) -ge 0 -and $count -lt 2) {
            $start = [Math]::Max(0, $idx - 200)
            $len = [Math]::Min(500, $content.Length - $start)
            Write-Host ("==== [" + $f.Name + "] '$kw' at $idx ====")
            Write-Host $content.Substring($start, $len)
            Write-Host ""
            $idx += $kw.Length
            $count++
        }
    }
}
