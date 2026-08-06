$ErrorActionPreference = 'Continue'

$dua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36'
$api = 'https://www.kupujemprodajem.com/api/web/v1/search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort'

$sess = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$hp = Invoke-WebRequest -Uri 'https://www.kupujemprodajem.com/' -WebSession $sess -UserAgent $dua -UseBasicParsing
Write-Host ("HOME " + $hp.StatusCode)

function TryReq($name, $headers, $useSession) {
    try {
        if ($useSession) { $r = Invoke-WebRequest -Uri $api -Headers $headers -WebSession $sess -UserAgent $dua -UseBasicParsing }
        else { $r = Invoke-WebRequest -Uri $api -Headers $headers -UserAgent $dua -UseBasicParsing }
        Write-Host "== $name => $($r.StatusCode)"
        Write-Host $r.Content.Substring(0, [Math]::Min(500, $r.Content.Length))
    } catch {
        Write-Host "== $name => ERR $($_.Exception.Message)"
    }
}

TryReq 'desktop_react+session' @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='desktop_react'; 'referer'='https://www.kupujemprodajem.com/'} $true
TryReq 'desktop_react-no-session' @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='desktop_react'} $false
TryReq 'mobile_web_react+desktop-UA' @{'accept'='application/json, text/plain, */*'; 'x-kp-channel'='mobile_web_react'} $true
