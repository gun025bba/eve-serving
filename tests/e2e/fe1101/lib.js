'use strict';
// Helpers for the eve-web 1.10.1 QA run (QA instance 127.0.0.1:8002 only). The page is opened at the REAL gateway origin (same origin as /v1),
// but every /ui/** request is fulfilled from a local checkout of the build under test (EVE_UI_DIR), so nothing is deployed and no proxy rewrites headers.
process.env.EVE_TARGET = process.env.EVE_TARGET || 'qa';
const fs = require('fs'), path = require('path');
const { webkit, chromium, devices } = require('@playwright/test');
const L = require('../release/lib');
if (String(L.PORT) !== '8002') throw new Error('QA 8002 only');
const UI_DIR = process.env.EVE_UI_DIR || path.join(L.HOME, 'eve-qa/fe/eve-web');
const EXPECT_V = process.env.EVE_EXPECT_UI_VERSION || '1.10.2';
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.webmanifest': 'application/manifest+json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon' };

let pass = 0, fail = 0, skip = 0; const fails = [];
const chk = (name, ok, extra = '') => { ok ? pass++ : (fail++, fails.push(name)); console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${extra !== '' ? '  — ' + String(extra).slice(0, 260) : ''}`); return !!ok; };
const info = (k, v) => console.log(`  INFO  ${k}: ${v}`);
const skp = (name, why) => { skip++; console.log(`  SKIP  ${name}  — ${why}`); };
const totals = () => ({ pass, fail, skip, fails });

async function session(login, file) {
  let st = L.stateRead(file); if (st && (await L.sessionValid(st.cookie)).status === 200) return st;
  st = { login, cookie: await L.pairQA(login) }; L.stateWrite(file, st); return st;
}
const api = (who, method, p, body) => L.req(method, p, { cookie: who.cookie, body });
const setIntro = (who, v) => api(who, 'PATCH', '/v1/settings', { ui: { memory_intro_seen: v } });
const setMemory = (who, v) => api(who, 'PATCH', '/v1/settings', { memory: { enabled: v } });
const getSettings = async who => (await api(who, 'GET', '/v1/settings')).json;

const STANDALONE = `(() => {
  Object.defineProperty(navigator, 'standalone', { value: true, configurable: true });
  const mm = window.matchMedia.bind(window);
  window.matchMedia = q => { const r = mm(q); if (/display-mode\\s*:\\s*standalone/.test(q)) return new Proxy(r, { get: (t, k) => k === 'matches' ? true : (typeof t[k] === 'function' ? t[k].bind(t) : t[k]) }); return r; };
})()`;
// records every moment the memory intro sheet is un-hidden (to catch a flash that a later poll would miss)
const WATCH_SHEET = `(() => {
  window.__sheetSeen = 0; window.__sheetLog = [];
  const arm = () => { const el = document.getElementById('memIntro'); if (!el) return false;
    const rec = () => { if (!el.hidden) { window.__sheetSeen++; window.__sheetLog.push(performance.now() | 0); } };
    new MutationObserver(rec).observe(el, { attributes: true, attributeFilter: ['hidden', 'class'] }); rec(); return true; };
  if (!arm()) document.addEventListener('DOMContentLoaded', arm);
})()`;

// opts: engine webkit|chromium, localSeen (preset localStorage eve.mem.introSeen=1 at every document start), standalone, scheme, cookie
async function openCtx(browser, o = {}) {
  const engine = o.engine || 'webkit';
  const ctx = await browser.newContext({ ...(engine === 'webkit' ? devices['iPhone 15'] : { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true }), colorScheme: o.scheme || 'dark', serviceWorkers: 'block' });
  if (o.memFlag !== false) await ctx.addInitScript(() => { try { localStorage.setItem('eve.flag.memory', '1'); } catch {} });   // memory UI is behind this localStorage flag (memory.js:2)
  if (o.localSeen) await ctx.addInitScript(() => { try { localStorage.setItem('eve.mem.introSeen', '1'); } catch {} });
  if (o.standalone) await ctx.addInitScript(STANDALONE);
  await ctx.addInitScript(WATCH_SHEET);
  if (o.cookie) await ctx.addCookies([{ name: 'eve_session', value: o.cookie, url: L.BASE + '/', httpOnly: true, secure: true, sameSite: 'Lax' }]);
  await ctx.route(u => new URL(u).origin === L.BASE && new URL(u).pathname.startsWith('/ui/'), route => {   // serve the build under test
    let p = decodeURIComponent(new URL(route.request().url()).pathname).slice(3); if (p.endsWith('/')) p += 'index.html';
    const f = path.join(UI_DIR, path.normalize(p).replace(/^(\.\.[/\\])+/, ''));
    fs.readFile(f, (e, b) => e ? route.fulfill({ status: 404, body: 'not found' }) : route.fulfill({ status: 200, headers: { 'Content-Type': MIME[path.extname(f)] || 'application/octet-stream', 'Cache-Control': 'no-store' }, body: b }));
  });
  const page = await ctx.newPage(); const errs = [], patches = [];
  page.on('pageerror', e => errs.push(String(e)));
  page.on('console', m => { if (m.type() === 'error') errs.push('console: ' + m.text()); });
  page.on('request', q => { if (q.method() === 'PATCH' && /\/v1\/settings/.test(q.url())) patches.push({ body: q.postData() || '', t: Date.now() }); });
  const introPatches = () => patches.filter(p => /memory_intro_seen/.test(p.body));
  return { ctx, page, errs, patches, introPatches };
}
const goHome = async (page, hash = '#/') => { await page.goto(L.BASE + '/ui/' + hash, { waitUntil: 'load' }); await page.waitForFunction(() => window.__app, null, { timeout: 15000 }); };
const sheetShown = page => page.evaluate(() => { const e = document.getElementById('memIntro'); return !!e && !e.hidden; });
const sheetEver = page => page.evaluate(() => window.__sheetSeen || 0);
const ls = page => page.evaluate(() => { try { return localStorage.getItem('eve.mem.introSeen'); } catch (e) { return 'ERR'; } });
// error line state, works for 1.10.1 (hidden attribute) and 1.10.2 (reserved line, .on / opacity): hidden=true means not visible to the user
const errState = page => page.evaluate(() => { const e = document.getElementById('memIntroErr'); if (!e) return null; const cs = getComputedStyle(e);
  const vis = !e.hidden && cs.display !== 'none' && parseFloat(cs.opacity) > 0.5; const r = e.getBoundingClientRect();
  return { hidden: !vis, text: e.textContent.trim(), role: e.getAttribute('role'), ariaHidden: e.getAttribute('aria-hidden'), h: Math.round(r.height * 10) / 10, op: cs.opacity }; });
async function waitSheet(page, ms = 8000) { try { await page.waitForFunction(() => { const e = document.getElementById('memIntro'); return e && !e.hidden; }, null, { timeout: ms }); return true; } catch { return false; } }
const sleep = L.sleep;
module.exports = { L, UI_DIR, EXPECT_V, chk, info, skp, totals, session, api, setIntro, setMemory, getSettings, openCtx, goHome, sheetShown, sheetEver, ls, errState, waitSheet, sleep, webkit, chromium, devices, STANDALONE };
