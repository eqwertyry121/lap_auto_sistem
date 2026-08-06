$ErrorActionPreference = 'Continue'

$f = 'D:\the_bot_god_of_laptop\tools\js\2vubi_b416i95.js'
$content = [System.IO.File]::ReadAllText($f)

function Show($kw, $before, $after, $max) {
    $idx = 0
    $count = 0
    while (($idx = $script:content.IndexOf($kw, $idx)) -ge 0 -and $count -lt $max) {
        $start = [Math]::Max(0, $idx - $before)
        $len = [Math]::Min($before + $after, $script:content.Length - $start)
        Write-Host ("==== '$kw' at $idx ====")
        Write-Host $script:content.Substring($start, $len)
        Write-Host ""
        $idx += $kw.Length
        $count++
    }
}

# 1. Полное определение generateSignature
Show 'generateSignature' 50 900 1

# 2. Реализация apiRequest (v="/api/web/v1/")
Show 'let v="/api/web/v1/"' 0 2500 1

# 3. Где используется generateSignature (заголовок?)
Show 'generateSignature)(' 300 300 3

# 4. заголовки с signature/key
Show 'x-kp-signature' 300 300 2
Show '"signature"' 300 300 2
