$ErrorActionPreference = 'Continue'

$mua = 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Mobile Safari/537.36'
$dua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36'
$api = 'https://www.kupujemprodajem.com/api/web/v1/search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort'

$sess = New-Object Microsoft.PowerShell.Commands.WebRequestSession
try {
    $hp = Invoke-WebRequest -Uri 'https://www.kupujemprodajem.com/' -WebSession $sess -UserAgent $dua -UseBasicParsing
    Write-Host ("HOME " + $hp.StatusCode + " len=" + $hp.Content.Length)
} catch {
    Write-Host ("HOME ERR " + $_.Exception.Message)
}
$cookies = $sess.Cookies.GetCookies('https://www.kupujemprodajem.com/') | ForEach-Object Name
Write-Host ("COOKIES: " + ($cookies -join ', '))

# 1) API c cookie-сессией
try {
    $r = Invoke-WebRequest -Uri $api -WebSession $sess -UserAgent $dua -Headers @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='web_react'; 'referer'='https://www.kupujemprodajem.com/'} -UseBasicParsing
    Write-Host ("API " + $r.StatusCode)
    $r.Content.Substring(0, [Math]::Min(400, $r.Content.Length)) | Write-Host
} catch {
    Write-Host ("API ERR " + $_.Exception.Message)
    $resp = $_.Exception.Response
    if ($resp) {
        try {
            $stream = $resp.GetResponseStream()
            if ($stream.CanSeek) { $stream.Position = 0 }
            $sr = New-Object System.IO.StreamReader($stream)
            $body = $sr.ReadToEnd()
            Write-Host ("API BODY: " + $body.Substring(0, [Math]::Min(400, $body.Length)))
        } catch { Write-Host ("API BODY read failed: " + $_.Exception.Message) }
    }
}

# 2) HTML-страница категории ноутбуков (desktop UA)
$catUrls = @(
    'https://www.kupujemprodajem.com/kompjuteri-laptop-i-tablet/laptopovi',
    'https://www.kupujemprodajem.com/kompjuteri-laptop-i-tablet/laptopovi?page=1'
)
foreach ($u in $catUrls) {
    try {
        $r = Invoke-WebRequest -Uri $u -WebSession $sess -UserAgent $dua -UseBasicParsing
        Write-Host ("CAT OK $u status=" + $r.StatusCode + " len=" + $r.Content.Length)
        $r.Content | Out-File -FilePath 'D:\the_bot_god_of_laptop\tools\cat.html' -Encoding utf8
        # ищем встроенные данные
        foreach ($marker in @('__NEXT_DATA__', 'window.__', 'application/ld+json', 'data-entity', 'entity-id', 'ad_id', 'price')) {
            $idx = $r.Content.IndexOf($marker)
            Write-Host ("  marker '$marker' at index " + $idx)
        }
        break
    } catch {
        Write-Host ("CAT ERR $u => " + $_.Exception.Message)
    }
}

# 3) мобильная версия сайта
try {
    $r = Invoke-WebRequest -Uri 'https://m.kupujemprodajem.com/' -UserAgent $mua -UseBasicParsing
    Write-Host ("MOBILE HOME " + $r.StatusCode + " len=" + $r.Content.Length)
} catch {
    Write-Host ("MOBILE HOME ERR " + $_.Exception.Message)
}
