$ErrorActionPreference = 'Continue'

$files = @(
    'D:\the_bot_god_of_laptop\tools\js\0arkzlrr21a_j.js',
    'D:\the_bot_god_of_laptop\tools\js\3phxflvvic_vg.js'
)
$keywords = @('Authorization', 'anonymous', 'x-kp-', 'token', 'login')

foreach ($f in $files) {
    $content = [System.IO.File]::ReadAllText($f)
    Write-Host ("===== FILE: " + (Split-Path $f -Leaf) + " len=" + $content.Length + " =====")
    foreach ($kw in $keywords) {
        $idx = 0
        $count = 0
        while (($idx = $content.IndexOf($kw, $idx)) -ge 0 -and $count -lt 3) {
            $start = [Math]::Max(0, $idx - 250)
            $len = [Math]::Min(600, $content.Length - $start)
            $snippet = $content.Substring($start, $len)
            Write-Host ("--- '$kw' at $idx ---")
            Write-Host $snippet
            Write-Host ""
            $idx += $kw.Length
            $count++
        }
    }
}
