// Зонд KP API: реплика клиентской криптографии из JS-бандла сайта.
const crypto = require('crypto');

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

async function api(pathWithQuery, { token, body, method = 'GET' } = {}) {
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
        'cookie': cookieHeader(),
    };
    if (token) headers['authorization'] = token;
    const resp = await fetch(base + '/api/web/v1/' + pathWithQuery, {
        method, headers, body: body ? JSON.stringify(body) : undefined, redirect: 'manual',
    });
    storeCookies(resp);
    const text = await resp.text();
    return { status: resp.status, text, headers: resp.headers };
}

(async () => {
    const home = await fetch(base + '/', { headers: { 'user-agent': UA, 'accept': 'text/html' } });
    storeCookies(home);
    console.log('HOME', home.status, 'cookies:', Object.keys(cookieJar).join(','));

    const key = generateAuth();
    const t = await api(`auth/get-token?key=${key}`);
    console.log('GET-TOKEN', t.status);
    console.log(t.text.slice(0, 1200));

    let token = null;
    try {
        const j = JSON.parse(t.text);
        token = j.data ?? j.token ?? null;
        if (token && typeof token === 'object') token = token.token ?? token.access_token ?? null;
    } catch { }
    console.log('TOKEN:', typeof token === 'string' ? token.slice(0, 100) : JSON.stringify(token));

    const sp = 'search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort';
    const s = await api(sp, { token });
    console.log('SEARCH', s.status);
    console.log(s.text.slice(0, 1500));
    if (s.status === 200) require('fs').writeFileSync('tools/search_sample.json', s.text);
})().catch(e => { console.error('FATAL', e); process.exit(1); });
