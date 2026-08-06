$ErrorActionPreference = 'Continue'

$dua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36'
$base = 'https://www.kupujemprodajem.com'
$html = Get-Content 'D:\the_bot_god_of_laptop\tools\home.html' -Raw

$scripts = [regex]::Matches($html, 'src="(/_next/static/chunks/[^"]+)"') | ForEach-Object { $_.Groups[1].Value }
Write-Host ("chunks: " + $scripts.Count)

$outDir = 'D:\the_bot_god_of_laptop\tools\js'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$patterns = @('api/web/v1', 'x-kp-', 'Authorization', 'Bearer', 'auth/token', 'anonymous', 'x-kp-channel')

foreach ($path in $scripts) {
    $name = Split-Path $path -Leaf
    $dest = Join-Path $outDir $name
    try {
        $r = Invoke-WebRequest -Uri ($base + $path) -UserAgent $dua -UseBasicParsing
        [System.IO.File]::WriteAllText($dest, $r.Content)
        $hits = @()
        foreach ($p in $patterns) {
            if ($r.Content.Contains($p)) { $hits += $p }
        }
        if ($hits.Count -gt 0) {
            Write-Host ("HIT $name : " + ($hits -join ', ') + " (len " + $r.Content.Length + ")")
        }
    } catch {
        Write-Host ("ERR $name " + $_.Exception.Message)
    }
}
Write-Host 'done'
