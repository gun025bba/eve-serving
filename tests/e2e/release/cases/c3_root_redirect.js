'use strict';
// Case 3: GET / -> /ui/ (eve-serving PR #91, merged into release/5.0.1 at bb78fb1). Read-only (GET/HEAD) except one POST that is skipped on prod-ro.
// #91 registers only the exact path "GET /{$}" -> 302 (Location always under /ui/, other paths stay 404).
// Since 5.0.1+f3d59e0 (#96) the query is preserved on the fixed /ui/ path (was dropped by #91). Other paths are untouched (not redirected).
const { goTests } = require('../gotest');
module.exports = {
  id: 3, name: 'GET / redirects to /ui/ (status, Location, paths/query)', readonly: true,
  async run(c) {
    const { req } = c.lib;
    const ui = await req('GET', '/ui/');
    if (ui.status !== 200) { c.warn('UI is not served here (/ui/ != 200)', `got ${ui.status}`); const r = await req('GET', '/'); c.check('UI off: GET / stays 404 JSON', r.status === 404, r.status); return; }
    const sameOrigin = loc => typeof loc === 'string' && loc.startsWith('/') && !loc.startsWith('//');
    const g = await req('GET', '/');
    c.check('GET / -> 302 (Found)', g.status === 302, `status ${g.status}`);
    c.check('Location is the same-origin path /ui/', g.headers.location === '/ui/', `Location=${g.headers.location}`);
    c.check('redirect carries Cache-Control: no-store', /no-store/.test(g.headers['cache-control'] || ''), g.headers['cache-control']);
    c.check('no auth needed (unauthenticated request got the redirect, not 401)', g.status !== 401);
    const h = await req('HEAD', '/');
    c.check('HEAD / -> 302 /ui/', h.status === 302 && h.headers.location === '/ui/', `${h.status} ${h.headers.location}`);
    // 5.0.1 (#96, f3d59e0): path is ALWAYS the fixed /ui/; the raw query is carried over only when plain printable ASCII and <= 2048 bytes.
    // EVE_EXPECT_QUERY_PRESERVED=0 restores the pre-#96 expectation (query dropped); default is preserved (PM decision 2026-10-28).
    const keep = process.env.EVE_EXPECT_QUERY_PRESERVED !== '0';
    const RAW = 'x=1&y=%E2%9C%93&z=a%20b';
    const q = await req('GET', '/?' + RAW);
    c.check('GET /?query is a 302 to the fixed /ui/ path', q.status === 302 && /^\/ui\/(\?|$)/.test(q.headers.location || ''), `${q.status} ${q.headers.location}`);
    c.check(keep ? 'query string preserved byte-for-byte: /?q -> /ui/?q' : 'query string dropped (pre-#96 behaviour)', q.headers.location === (keep ? '/ui/?' + RAW : '/ui/'), `Location=${q.headers.location}`);
    const e = await req('GET', '/?');
    c.check('empty query (/?) -> plain /ui/', e.status === 302 && e.headers.location === '/ui/', `${e.status} ${e.headers.location}`);
    const ev = await req('GET', '/?next=//evil.example/x&u=http://evil.example');
    c.check('no open redirect: Location stays under /ui/ whatever the query says', ev.status === 302 && /^\/ui\/(\?|$)/.test(ev.headers.location || '') && sameOrigin(ev.headers.location), ev.headers.location);
    const inj = await req('GET', '/?a=%0d%0aSet-Cookie:x=1');
    c.check('encoded CR/LF in the query is not decoded into a header (no injected header)', inj.status === 302 && !inj.headers['set-cookie'] && /^\/ui\//.test(inj.headers.location || ''), `${inj.status} ${inj.headers.location}`);
    const big = await req('GET', '/?a=' + 'b'.repeat(2100));
    c.check('query > 2048 bytes is dropped (plain /ui/), not copied into the header', big.status === 302 && big.headers.location === '/ui/', `${big.status} ${String(big.headers.location).slice(0, 40)}`);
    const sp = await req('GET', '/?a=1', { headers: { 'X-Forwarded-Host': 'evil.example', 'X-Forwarded-Proto': 'http' } });
    c.check('forwarded headers cannot steer the redirect', sp.status === 302 && sp.headers.location === (keep ? '/ui/?a=1' : '/ui/'), sp.headers.location);
    const f = await req('GET', g.headers.location || '/ui/');
    c.check('following the redirect: 200 text/html', f.status === 200 && /text\/html/.test(f.headers['content-type'] || ''), `${f.status} ${f.headers['content-type']}`);
    const u0 = await req('GET', '/ui');
    c.check('GET /ui -> 307 /ui/ (unchanged)', u0.status === 307 && u0.headers.location === '/ui/', `${u0.status} ${u0.headers.location}`);
    const u1 = await req('GET', '/ui/?x=1');
    c.check('GET /ui/?x=1 serves the UI (query not an error)', u1.status === 200, u1.status);
    for (const p of ['/nope', '/index.html', '/v1']) { const r = await req('GET', p); c.check(`GET ${p} is not redirected to the UI`, r.status !== 302 && r.status < 500 && !/\/ui\//.test(r.headers.location || ''), `${r.status} ${r.headers.location || ''}`); }
    const dd = await req('GET', '//'); c.check('GET // is not turned into a UI redirect', !(dd.status === 302 && dd.headers.location === '/ui/'), `${dd.status} ${dd.headers.location || ''}`);
    const fh = await req('GET', '/', { headers: { Host: 'evil.example' } });
    c.check('foreign Host header -> 421 (host allowlist wraps the redirect)', fh.status === 421, fh.status);
    if (!c.lib.RO()) { const p = await req('POST', '/', { csrf: true, body: {} }); c.check('POST / stays 404 (only GET redirects)', p.status === 404, p.status); }
    goTests(c, { ref: process.env.EVE_REF_501 || 'origin/release/5.0.1', run: 'TestRoot', pkgs: ['./internal/handlers/'], label: 'root redirect unit tests on the release ref (TestRootRedirects*, TestRootNoUI404)' });
  },
};
