$ErrorActionPreference = 'Continue'

$dua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36'
$base = 'https://www.kupujemprodajem.com'

function Get-SHA1Hex($s) {
    $sha1 = [System.Security.Cryptography.SHA1]::Create()
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($s)
    $hash = $sha1.ComputeHash($bytes)
    return ($hash | ForEach-Object { $_.ToString('x2') }) -join ''
}

function ConvertTo-Base36([long]$n) {
    $chars = '0123456789abcdefghijklmnopqrstuvwxyz'.ToCharArray()
    $sb = ''
    while ($n -gt 0) { $sb = $chars[$n % 36] + $sb; $n = [Math]::Floor($n / 36) }
    return $sb
}

# соль из обфусцированного JS
$salt1 = ('Nvt3ZRK'.ToCharArray() | ForEach-Object { [string]$_ * 5 }) -join ''
$salt2 = 'f64nGz7'.Substring(2) + 'f64nGz7'.Substring(0,2)
$salt3 = [regex]::Replace('GdxpEd5', '(..).', '$1|')
$suffix = $salt1 + $salt2 + $salt3
Write-Host ("SUFFIX = " + $suffix)

function Get-KpSignature($pathWithQuery, $bodyJson) {
    return Get-SHA1Hex("/api/web/v1/" + $pathWithQuery + $bodyJson + $script:suffix)
}

# сессия
$sess = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$hp = Invoke-WebRequest -Uri "$base/" -WebSession $sess -UserAgent $dua -UseBasicParsing
Write-Host ("HOME " + $hp.StatusCode)

$common = @{
    'accept' = 'application/json, text/plain, */*'
    'content-type' = 'application/json'
    'x-kp-webdriver' = 'false'
    'x-kp-dark' = 'false'
    'x-kp-theme' = 'system'
    'x-kp-channel' = 'desktop_react'
    'origin' = $base
    'referer' = "$base/"
}

# --- 1. get-token ---
$key = ConvertTo-Base36([DateTimeOffset]::Now.ToUnixTimeMilliseconds())
if ($key.Length -gt 10) { $key = $key.Substring($key.Length - 10) }
Write-Host ("KEY = " + $key)
$tokenPath = "auth/get-token?key=$key"
$h = $common.Clone()
$h['x-kp-signature'] = Get-KpSignature $tokenPath ''
try {
    $r = Invoke-WebRequest -Uri "$base/api/web/v1/$tokenPath" -Headers $h -WebSession $sess -UserAgent $dua -UseBasicParsing
    Write-Host ("GET-TOKEN " + $r.StatusCode)
    Write-Host $r.Content.Substring(0, [Math]::Min(600, $r.Content.Length))
    $tok = ($r.Content | ConvertFrom-Json)
    $tokenValue = $null
    if ($tok.PSObject.Properties['data']) { $tokenValue = $tok.data }
    if ($tok.PSObject.Properties['token']) { $tokenValue = $tok.token }
    Write-Host ("TOKEN VALUE TYPE: " + $tokenValue.GetType().Name)
} catch {
    Write-Host ("GET-TOKEN ERR " + $_.Exception.Message)
    $resp = $_.Exception.Response
    if ($resp) {
        $sr = New-Object System.IO.StreamReader($resp.GetResponseStream())
        Write-Host ("BODY: " + $sr.ReadToEnd())
    }
    exit
}

# --- 2. search с Authorization ---
$searchPath = 'search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort'
$h2 = $common.Clone()
$h2['x-kp-signature'] = Get-KpSignature $searchPath ''
if ($tokenValue) {
    if ($tokenValue -is [string]) { $h2['Authorization'] = $tokenValue }
    else { $h2['Authorization'] = ($tokenValue | ConvertTo-Json -Depth 5 -Compress) }
}
try {
    $r = Invoke-WebRequest -Uri "$base/api/web/v1/$searchPath" -Headers $h2 -WebSession $sess -UserAgent $dua -UseBasicParsing
    Write-Host ("SEARCH " + $r.StatusCode)
    $r.Content.Substring(0, [Math]::Min(800, $r.Content.Length)) | Write-Host
    $r.Content | Out-File 'D:\the_bot_god_of_laptop\tools\search_sample.json' -Encoding utf8
} catch {
    Write-Host ("SEARCH ERR " + $_.Exception.Message)
    $resp = $_.Exception.Response
    if ($resp) {
        $sr = New-Object System.IO.StreamReader($resp.GetResponseStream())
        Write-Host ("BODY: " + $sr.ReadToEnd())
    }
}
