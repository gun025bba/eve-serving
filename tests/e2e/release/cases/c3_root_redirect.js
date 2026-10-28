'use strict';
// Case 3: GET / -> /ui/ (eve-serving PR #91, merged into release/5.0.1 at bb78fb1). Read-only (GET/HEAD) except one POST that is skipped on prod-ro.
// ASSUMPTION: #91 registers only the exact path "GET /{$}" -> 302 "/ui/" (Location fixed, query string NOT carried, other paths stay 404).
// PM's wording "paths and query strings are preserved" is therefore checked as: other paths are untouched (not redirected), and a query on "/"
// is recorded; it only FAILs when EVE_EXPECT_QUERY_PRESERVED=1. A Location that leaves the origin always FAILs.
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
    const q = await req('GET', '/?x=1&y=%E2%9C%93');
    c.check('GET /?query is still a redirect to a same-origin path', q.status === 302 && sameOrigin(q.headers.location), `${q.status} ${q.headers.location}`);
    const kept = /x=1/.test(q.headers.location || '');
    if (kept) c.pass('query string preserved on the redirect', q.headers.location);
    else if (process.env.EVE_EXPECT_QUERY_PRESERVED === '1') c.fail('query string preserved on the redirect', `Location=${q.headers.location}`);
    else c.warn('query string is NOT carried by / -> /ui/ (matches PR #91 as written)', `Location=${q.headers.location}; set EVE_EXPECT_QUERY_PRESERVED=1 if PM wants it`);
    const ev = await req('GET', '/?next=//evil.example/x');
    c.check('no open redirect via query', ev.status === 302 && sameOrigin(ev.headers.location) && !/evil/.test(ev.headers.location), ev.headers.location);
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
