'use strict';
// Playwright helpers. The UI under test is the one served at <base>/ui/ (or a static dir via EVE_UI_DIR for dry runs).
// Auth is never typed into the page: a paired QA session cookie is added to /v1 + /whoami requests by a route handler
// (the Secure cookie flag would otherwise block it on plain http), or the backend is mocked when there is no session.
const http = require('http'), fs = require('fs'), path = require('path');
const { webkit, chromium, devices } = require('@playwright/test');
const lib = require('./lib');

const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.webmanifest': 'application/manifest+json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon' };
function serveDir(dir) {             // static /ui/ server on a random loopback port (not 8000/8001)
  return new Promise(resolve => {
    const s = http.createServer((q, r) => {
      let p = decodeURIComponent(new URL(q.url, 'http://x').pathname);
      if (p === '/' ) { r.writeHead(302, { Location: '/ui/' }); return r.end(); }
      if (!p.startsWith('/ui/')) { r.writeHead(404); return r.end('{}'); }
      p = p.slice(3); if (p.endsWith('/')) p += 'index.html';
      const f = path.join(dir, path.normalize(p).replace(/^(\.\.[/\\])+/, ''));
      fs.readFile(f, (e, b) => { if (e) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'Content-Type': MIME[path.extname(f)] || 'application/octet-stream' }); r.end(b); });
    }).listen(0, '127.0.0.1', () => resolve({ base: `http://127.0.0.1:${s.address().port}`, close: () => s.close() }));
  });
}
async function uiBase() {
  if (process.env.EVE_UI_DIR) { const s = await serveDir(process.env.EVE_UI_DIR); return { base: s.base, close: s.close, source: `dir:${process.env.EVE_UI_DIR}` }; }
  return { base: lib.BASE, close() {}, source: 'gateway' };
}

const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
// Minimal backend mock (no session): enough for the list + chat views to render.
function mockBackend(page, { threads = [], messages = {} } = {}) {
  return page.route(u => { const p = new URL(u).pathname; return p === '/whoami' || p.startsWith('/v1/'); }, route => {
    const u = new URL(route.request().url()), p = u.pathname, m = route.request().method();
    if (p === '/whoami') return json(route, { authenticated: true, user: 'qa-ui', provider: 'session', capabilities: { persistence: true, web_search: true } });
    if (p === '/v1/models') return json(route, { data: [{ id: 'qa-model' }] });
    if (p === '/v1/settings') return json(route, { memory: { enabled: false }, proactive: { enabled: false }, ui: { memory_intro_seen: true }, system: { memory_available: false, proactive_available: false, tools_available: false } });
    if (p === '/v1/threads' && m === 'GET') return json(route, { data: threads, next_cursor: null });
    let mm = p.match(/^\/v1\/threads\/([^/]+)\/messages$/); if (mm) return json(route, { data: messages[mm[1]] || [], has_more: false });
    mm = p.match(/^\/v1\/threads\/([^/]+)$/); if (mm) return json(route, threads.find(t => t.id === mm[1]) || { error_code: 'not_found' }, threads.find(t => t.id === mm[1]) ? 200 : 404);
    if (p === '/v1/actions' || p.startsWith('/v1/push') ) return json(route, { data: [] });
    return json(route, { error_code: 'not_found' }, 404);
  });
}
// Real backend as a paired QA user: put the session cookie in the browser's own jar (a route handler cannot override the Cookie header: /whoami stays 401).
// httpOnly + secure like the real cookie; both WebKit and Chromium accept a Secure cookie for a plain-http loopback URL when it is added this way.
async function withSession(ctx, cookie, base) {
  await ctx.addCookies([{ name: 'eve_session', value: cookie, url: base.replace(/\/$/, '') + '/', httpOnly: true, secure: true, sameSite: 'Lax' }]);
}
// iOS standalone emulation: JS side only (matchMedia + navigator.standalone). CSS @media (display-mode) and the notch safe-area
// cannot be emulated by Playwright WebKit -> those stay real-device items.
const STANDALONE = `(() => {
  Object.defineProperty(navigator, 'standalone', { value: true, configurable: true });
  const mm = window.matchMedia.bind(window);
  window.matchMedia = q => { const r = mm(q); if (/display-mode\\s*:\\s*standalone/.test(q)) return new Proxy(r, { get: (t, k) => k === 'matches' ? true : (typeof t[k] === 'function' ? t[k].bind(t) : t[k]) }); return r; };
})()`;

async function openPage({ engine = 'webkit', scheme = 'dark', standalone = true, mock, cookie, base, device = 'iPhone 15', url = '/ui/#/' }) {
  const b = await (engine === 'chromium' ? chromium : webkit).launch();
  const ctx = await b.newContext({ ...(engine === 'webkit' ? devices[device] : { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true }), colorScheme: scheme, serviceWorkers: 'block' });
  await ctx.addInitScript(() => { localStorage.setItem('eve.mem.introSeen', '1'); });
  if (standalone) await ctx.addInitScript(STANDALONE);
  const page = await ctx.newPage(); const errs = [];
  page.on('pageerror', e => errs.push(String(e)));
  if (cookie) await withSession(ctx, cookie, base); else await mockBackend(page, mock || {});
  await page.goto(base + url, { waitUntil: 'load' });
  await page.waitForFunction(() => window.__app, null, { timeout: 15000 });
  await page.waitForTimeout(800);
  return { page, errs, close: () => b.close(), browser: b, ctx };
}
module.exports = { uiBase, openPage, mockBackend, serveDir, devices };
