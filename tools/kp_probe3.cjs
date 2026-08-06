// Проверка ad_attributes + диагностика ответов без info
const crypto = require('crypto');
const fs = require('fs');
const salt = 'Nvt3ZRK'.split('').map(e => e + e + e + e + e).join('')
    + 'f64nGz7'.slice(2) + 'f64nGz7'.slice(0, 2)
    + 'GdxpEd5'.replace(/(..)./g, '$1|');

function sig(p, b) { return crypto.createHash('sha1').update(p + (b || '') + salt, 'utf8').digest('hex'); }

async function api(p) {
    const r = await fetch('https://www.kupujemprodajem.com' + p, {
        headers: {
            'accept': 'application/json, text/plain, */*',
            'user-agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36',
            'x-kp-channel': 'desktop_react',
            'x-kp-signature': sig(p),
            'origin': 'https://www.kupujemprodajem.com',
            'referer': 'https://www.kupujemprodajem.com/',
        }
    });
    return { status: r.status, json: await r.json().catch(() => null) };
}

(async () => {
    const s = await api('/api/web/v1/search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort');
    console.log('search status:', s.status);
    if (!s.json || !s.json.results) {
        console.log('search body:', JSON.stringify(s.json).slice(0, 400));
        process.exit(2);
    }
    const ads = s.json.results.ads;
    let withAttrs = 0, noInfo = 0;
    for (const a of ads) {
        const d = await api('/api/web/v1/eds/' + a.ad_id);
        if (d.status !== 200 || !d.json || !d.json.info) {
            noInfo++;
            console.log('NO-INFO ad', a.ad_id, 'status', d.status, JSON.stringify(d.json).slice(0, 200));
            continue;
        }
        const at = d.json.info.ad_attributes || [];
        if (at.length) {
            withAttrs++;
            console.log('=== ad', a.ad_id, 'attrs:', at.length);
            console.log(JSON.stringify(at, null, 1).slice(0, 1500));
            if (withAttrs >= 2) break;
        }
    }
    console.log('with attrs:', withAttrs, '| no info:', noInfo);
})().catch(e => { console.error('FATAL', e); process.exit(1); });
