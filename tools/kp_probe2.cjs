// Зонд KP API v2: подпись работает. Уточняем: нужны ли куки, формат /eds/{id}.
const crypto = require('crypto');
const fs = require('fs');

const UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36';
const base = 'https://www.kupujemprodajem.com';

function generateAuth() { return (+new Date).toString(36).slice(-10); }
function generateSignature(path, body) {
    const bodyStr = (body && typeof body === 'object') ? JSON.stringify(body) : '';
    const n = path + bodyStr +
        'Nvt3ZRK'.split('').map(e => e + e + e + e + e).join('') +
        ('f64nGz7'.slice(2) + 'f64nGz7'.slice(0, 2)) +
        'GdxpEd5'.replace(/(..)./g, '$1|');
    return crypto.createHash('sha1').update(n, 'utf8').digest('hex');
}

const cookieJar = {};
function storeCookies(resp) {
    const sc = resp.headers.getSetCookie ? resp.headers.getSetCookie() : [];
    for (const c of sc) {
        const pair = c.split(';')[0];
        const idx = pair.indexOf('=');
        if (idx > 0) cookieJar[pair.slice(0, idx).trim()] = pair.slice(idx + 1).trim();
    }
}
function cookieHeader() {
    return Object.entries(cookieJar).map(([k, v]) => `${k}=${v}`).join('; ');
}

async function api(pathWithQuery, { token, body, method = 'GET', useCookies = true } = {}) {
    const sig = generateSignature('/api/web/v1/' + pathWithQuery, body);
    const headers = {
        'accept': 'application/json, text/plain, */*',
        'accept-language': 'sr-RS,sr;q=0.9,en-US;q=0.8,en;q=0.7',
        'content-type': 'application/json',
        'user-agent': UA,
        'origin': base,
        'referer': base + '/',
        'x-kp-channel': 'desktop_react',
        'x-kp-signature': sig,
        'x-kp-webdriver': 'false',
        'x-kp-dark': 'false',
        'x-kp-theme': 'system',
    };
    if (useCookies) headers['cookie'] = cookieHeader();
    if (token) headers['authorization'] = token;
    const resp = await fetch(base + '/api/web/v1/' + pathWithQuery, {
        method, headers, body: body ? JSON.stringify(body) : undefined, redirect: 'manual',
    });
    storeCookies(resp);
    const text = await resp.text();
    return { status: resp.status, text };
}

function shape(obj, depth = 0, maxDepth = 2) {
    if (depth > maxDepth) return '…';
    if (Array.isArray(obj)) return obj.length ? `[${obj.length}] ` + shape(obj[0], depth + 1, maxDepth) : '[]';
    if (obj && typeof obj === 'object') {
        const out = {};
        for (const k of Object.keys(obj)) out[k] = shape(obj[k], depth + 1, maxDepth);
        return out;
    }
    if (obj === null) return 'null';
    return typeof obj + ':' + String(obj).slice(0, 60);
}

(async () => {
    // 1. search БЕЗ кук (только подпись)
    const sp = 'search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort';
    const s1 = await api(sp, { useCookies: false });
    console.log('SEARCH-NO-COOKIES', s1.status, s1.text.slice(0, 120));

    // 2. куки с главной
    const home = await fetch(base + '/', { headers: { 'user-agent': UA, 'accept': 'text/html' } });
    storeCookies(home);
    console.log('HOME', home.status, 'cookies:', Object.keys(cookieJar).join(','));

    const s2 = await api(sp);
    console.log('SEARCH-WITH-COOKIES', s2.status);
    const sj = JSON.parse(s2.text);
    const ads = sj.results.ads;
    console.log('ads count:', ads.length, 'total:', sj.results.total, 'pages:', sj.results.pages);
    console.log('--- структура первого объявления ---');
    console.log(JSON.stringify(shape(ads[0]), null, 1).slice(0, 3000));
    fs.writeFileSync('tools/search_sample.json', s2.text);

    // 3. детали первого объявления
    const adId = ads[0].ad_id;
    console.log('\n--- /eds/' + adId + ' ---');
    const d = await api('eds/' + adId);
    console.log('EDS status:', d.status);
    if (d.status === 200) {
        fs.writeFileSync('tools/eds_sample.json', d.text);
        const dj = JSON.parse(d.text);
        console.log(JSON.stringify(shape(dj, 0, 3), null, 1).slice(0, 5000));
    } else {
        console.log(d.text.slice(0, 500));
    }
})().catch(e => { console.error('FATAL', e); process.exit(1); });
