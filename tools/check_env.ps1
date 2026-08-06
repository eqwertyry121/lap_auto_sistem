$c = [System.IO.File]::ReadAllText((Resolve-Path '.env'))
foreach ($k in 'TELEGRAM_CHAT_ID','TELEGRAM_BOT_TOKEN','GEMINI_API_KEY','GEMINI_MODEL','GOOGLE_SHEETS_ID') {
    $m = [regex]::Match($c, ('(?mi)^' + $k + '=(.*)$'))
    if ($m.Success) {
        $v = $m.Groups[2].Value.Trim()
        if ($v) { Write-Output ($k + ' = SET(' + $v.Length + ')') } else { Write-Output ($k + ' = EMPTY') }
    } else {
        Write-Output ($k + ' = MISSING')
    }
}
