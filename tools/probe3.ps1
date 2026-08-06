$ErrorActionPreference = 'Continue'

$mua = 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Mobile Safari/537.36'
$dua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36'

# --- 1. Desktop homepage: скрипты и hints ---
$sess = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$hp = Invoke-WebRequest -Uri 'https://www.kupujemprodajem.com/' -WebSession $sess -UserAgent $dua -UseBasicParsing
$hp.Content | Out-File -FilePath 'D:\the_bot_god_of_laptop\tools\home.html' -Encoding utf8
Write-Host ("HOME len=" + $hp.Content.Length)

$scripts = [regex]::Matches($hp.Content, 'src="([^"]+\.js[^"]*)"')
Write-Host "--- script srcs ---"
foreach ($m in $scripts) { Write-Host $m.Groups[1].Value }

foreach ($marker in @('__NEXT_DATA__','window.__INITIAL','kp-token','x-kp','Bearer','csrf')) {
    $idx = $hp.Content.IndexOf($marker)
    if ($idx -ge 0) { Write-Host ("MARKER '$marker' at " + $idx + ": ..." + $hp.Content.Substring($idx, [Math]::Min(200, $hp.Content.Length-$idx)) + "...") }
}

# --- 2. Мобильный сайт: ищем ссылку на категорию ноутбуков ---
$mh = Invoke-WebRequest -Uri 'https://m.kupujemprodajem.com/' -UserAgent $mua -UseBasicParsing
$mh.Content | Out-File -FilePath 'D:\the_bot_god_of_laptop\tools\mhome.html' -Encoding utf8
Write-Host ("MOBILE HOME len=" + $mh.Content.Length)
$links = [regex]::Matches($mh.Content, 'href="([^"]+)"')
Write-Host "--- ссылки на категории (первые 40) ---"
$count = 0
foreach ($m in $links) {
    $href = $m.Groups[1].Value
    if ($href -match 'laptop|kompjuter|kategorij|category') {
        Write-Host $href
        $count++
        if ($count -ge 40) { break }
    }
}
