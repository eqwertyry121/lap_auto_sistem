$ErrorActionPreference = 'Continue'

$ua = 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Mobile Safari/537.36'
$api = 'https://www.kupujemprodajem.com/api/web/v1/search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort'

function Try-Request($name, $headers, $session) {
    try {
        if ($session) {
            $r = Invoke-WebRequest -Uri $api -Headers $headers -WebSession $session -UserAgent $ua -UseBasicParsing
        } else {
            $r = Invoke-WebRequest -Uri $api -Headers $headers -UserAgent $ua -UseBasicParsing
        }
        Write-Host "== $name => $($r.StatusCode)"
        Write-Host $r.Content.Substring(0, [Math]::Min(400, $r.Content.Length))
        return $true
    } catch {
        $resp = $_.Exception.Response
        $body = ''
        if ($resp) {
            try {
                $stream = $resp.GetResponseStream()
                $sr = New-Object System.IO.StreamReader($stream)
                $body = $sr.ReadToEnd()
            } catch {}
        }
        Write-Host "== $name => ERR $($_.Exception.Message)"
        if ($body) { Write-Host ("   body: " + $body.Substring(0, [Math]::Min(400, $body.Length))) }
        return $false
    }
}

Write-Host "--- получаю сессию с главной страницы ---"
$sess = New-Object Microsoft.PowerShell.Commands.WebRequestSession
try {
    $resp = Invoke-WebRequest -Uri 'https://www.kupujemprodajem.com/' -WebSession $sess -UserAgent $ua -UseBasicParsing
    Write-Host ("HOME " + $resp.StatusCode)
    $names = $sess.Cookies.GetCookies('https://www.kupujemprodajem.com/') | ForEach-Object Name
    Write-Host ("COOKIES: " + ($names -join ', '))
} catch {
    Write-Host ("HOME ERR " + $_.Exception.Message)
}

$null = Try-Request 'bare-401-body' @{'accept'='application/json, text/plain, */*'} $null
$null = Try-Request 'session+mobile_web_react' @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='mobile_web_react'} $sess
$null = Try-Request 'session+web_react+origin' @{'accept'='application/json, text/plain, */*'; 'origin'='https://www.kupujemprodajem.com'; 'referer'='https://www.kupujemprodajem.com/'; 'x-kp-channel'='web_react'} $sess
$null = Try-Request 'session-no-channel' @{'accept'='application/json, text/plain, */*'} $sess
$null = Try-Request 'channel=android_app' @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='android_app'} $null
