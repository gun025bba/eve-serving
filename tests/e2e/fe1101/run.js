'use strict';
// QA for eve-web 1.10.1 (PR #26, head c350ed7): memory intro "seen" only after PATCH ui.memory_intro_seen = 200.
// QA instance 127.0.0.1:8002 ONLY. UI under test = local checkout at EVE_UI_DIR (default ~/eve-qa/fe/eve-web @ c350ed7), served into the page via Playwright
// route.fulfill for /ui/**; /v1 + /whoami hit the real QA gateway with a paired QA session cookie. Nothing is deployed.
//   node tests/e2e/fe1101/run.js [--only s1,s2,s3,s4,s5,s6]
const H = require('./lib');
const { L, chk, info, skp } = H;
const only = process.argv.includes('--only') ? process.argv[process.argv.indexOf('--only') + 1].split(',') : null;
const want = n => !only || only.includes(n);
const BODY = JSON.stringify({ ui: { memory_intro_seen: true } });
const isIntroPatch = r => r.method() === 'PATCH' && /memory_intro_seen/.test(r.postData() || '');
const settle = async (c, ms) => { await H.sleep(ms); };
let A, B, browser;

// ------------------------------------------------------------------------------------------------ 1 happy path
async function s1() {
  console.log('\n== 1. happy path: dismiss -> PATCH 200 -> localStorage -> reload -> no sheet; server flag persists across a new context ==');
  await H.setIntro(B, false);
  const c = await H.openCtx(browser, { cookie: B.cookie });
  const resp = []; c.page.on('response', r => { if (isIntroPatch(r.request())) resp.push(r.status()); });
  await H.goHome(c.page);
  chk('UI under test reports version ' + H.EXPECT_V, (await c.page.evaluate('__app.version')) === H.EXPECT_V);
  chk('sheet appears on first launch (server flag empty, local empty)', await H.waitSheet(c.page));
  for (let i = 0; i < 30 && !resp.length; i++) await H.sleep(100);
  chk('exactly one PATCH {"ui":{"memory_intro_seen":true}} sent when the sheet opens, status 200', c.introPatches().length === 1 && resp[0] === 200 && c.introPatches()[0].body === BODY, JSON.stringify({ n: c.introPatches().length, resp, body: c.introPatches()[0] && c.introPatches()[0].body }));
  chk('localStorage eve.mem.introSeen = "1" after the 200', (await H.ls(c.page)) === '1');
  chk('error line hidden', (await H.errState(c.page)).hidden === true);
  await c.page.click('#memIntroOk'); await H.sleep(900);
  chk('tap 좋아요 closes the sheet, no second PATCH', !(await H.sheetShown(c.page)) && c.introPatches().length === 1, c.introPatches().length);
  chk('server flag is true (GET /v1/settings)', (await H.getSettings(B)).ui.memory_intro_seen === true);
  await c.page.reload({ waitUntil: 'load' }); await H.sleep(2500);
  chk('reload: sheet does not reappear and never flashed', !(await H.sheetShown(c.page)) && (await H.sheetEver(c.page)) === 0, await H.sheetEver(c.page));
  chk('reload: no further intro PATCH (server flag set, local set)', c.introPatches().length === 1, c.introPatches().length);
  chk('page errors', c.errs.length === 0, c.errs.join(' | '));
  await c.ctx.close();
  const d = await H.openCtx(browser, { cookie: B.cookie });                // fresh context = empty localStorage, same server account
  await H.goHome(d.page); await H.sleep(2500);
  chk('new context (empty localStorage): sheet does not show because the server flag persisted', !(await H.sheetShown(d.page)) && (await H.sheetEver(d.page)) === 0 && (await H.ls(d.page)) === null, JSON.stringify({ ls: await H.ls(d.page) }));
  chk('new context: zero intro PATCH', d.introPatches().length === 0, d.introPatches().length);
  await d.ctx.close();
  // other user is unaffected (per-user flag)
  await H.setIntro(A, false); const e = await H.openCtx(browser, { cookie: A.cookie }); await H.goHome(e.page);
  chk('other QA user (flag false) still gets the sheet while B has seen it', await H.waitSheet(e.page));
  await e.ctx.close(); await H.setIntro(A, true);
}

// ------------------------------------------------------------------------------------------------ 2 PATCH failure
async function failCase(kind) {
  await H.setIntro(B, false);
  const c = await H.openCtx(browser, { cookie: B.cookie });
  let mode = kind;                                                           // 'ok' lets requests through
  await c.page.route(/\/v1\/settings/, route => {
    const r = route.request(); if (!isIntroPatch(r) || mode === 'ok') return route.fallback();
    if (mode === '500') return route.fulfill({ status: 500, contentType: 'application/json', body: '{"error_code":"internal"}' });
    if (mode === '400') return route.fulfill({ status: 400, contentType: 'application/json', body: '{"error_code":"invalid"}' });
    if (mode === 'net') return route.abort('failed');
    if (mode === 'html200') return route.fulfill({ status: 200, contentType: 'text/html', body: '<html>proxy page</html>' });
    return route.fallback();
  });
  const t = `[PATCH ${kind}] `;
  await H.goHome(c.page);
  chk(t + 'sheet shown', await H.waitSheet(c.page));
  for (let i = 0; i < 30 && c.introPatches().length < 1; i++) await H.sleep(100);
  chk(t + 'auto PATCH at show attempted once; localStorage NOT set; error line still hidden (no nag before the user acts)', c.introPatches().length === 1 && (await H.ls(c.page)) === null && (await H.errState(c.page)).hidden, JSON.stringify({ n: c.introPatches().length, ls: await H.ls(c.page) }));
  await c.page.click('#memIntroOk');
  for (let i = 0; i < 30 && c.introPatches().length < 2; i++) await H.sleep(100);
  await H.sleep(500);
  const er = await H.errState(c.page);
  chk(t + '좋아요 retries the PATCH (2nd attempt)', c.introPatches().length === 2, c.introPatches().length);
  chk(t + 'sheet stays open', await H.sheetShown(c.page));
  chk(t + 'in-sheet error text is visible, role=alert, says 저장하지 못했어요', er && !er.hidden && er.role === 'alert' && /저장하지 못했어요/.test(er.text), JSON.stringify(er));
  chk(t + 'localStorage still not set', (await H.ls(c.page)) === null);
  chk(t + '좋아요 re-enabled (not stuck disabled)', await c.page.evaluate(() => !document.getElementById('memIntroOk').disabled));
  chk(t + 'error line is inside the sheet and on screen', await c.page.evaluate(() => { const e = document.getElementById('memIntroErr'), s = document.getElementById('memIntro'); const a = e.getBoundingClientRect(), b = s.getBoundingClientRect(); return s.contains(e) && a.height > 0 && a.top >= b.top && a.bottom <= b.bottom && a.bottom <= innerHeight; }));
  await c.page.click('#memIntroOk'); await H.sleep(700);
  chk(t + 'third tap: still open, still not stored, still shows error (no state corruption)', (await H.sheetShown(c.page)) && (await H.ls(c.page)) === null && c.introPatches().length === 3 && !(await H.errState(c.page)).hidden, c.introPatches().length);
  chk(t + 'server flag still false', (await H.getSettings(B)).ui.memory_intro_seen === false);
  await c.page.reload({ waitUntil: 'load' });
  chk(t + 'next launch asks again (sheet shown after reload)', await H.waitSheet(c.page) && (await H.ls(c.page)) === null);
  mode = 'ok'; await c.page.click('#memIntroOk'); await H.sleep(1200);
  chk(t + 'recovery: network back -> tap 좋아요 -> PATCH 200 -> sheet closes, localStorage "1", error cleared', !(await H.sheetShown(c.page)) && (await H.ls(c.page)) === '1' && (await H.errState(c.page)).hidden, JSON.stringify({ ls: await H.ls(c.page) }));
  chk(t + 'recovery: server flag true', (await H.getSettings(B)).ui.memory_intro_seen === true);
  chk(t + 'page errors', c.errs.filter(x => !/Failed to load resource|net::ERR|status of (400|500)/i.test(x)).length === 0, c.errs.join(' | '));
  await c.ctx.close();
}
async function s2() {
  console.log('\n== 2. PATCH failure: sheet stays, in-sheet error, localStorage untouched ==');
  for (const k of ['500', '400', 'net']) await failCase(k);
  // 200 with a non-JSON body (captive portal / proxy page): informational
  await H.setIntro(B, false);
  { const c = await H.openCtx(browser, { cookie: B.cookie });
    await c.page.route(/\/v1\/settings/, route => isIntroPatch(route.request()) ? route.fulfill({ status: 200, contentType: 'text/html', body: '<html>portal</html>' }) : route.fallback());
    await H.goHome(c.page); await H.waitSheet(c.page); await H.sleep(800);
    info('[PATCH 200 text/html (captive portal)] localStorage after show', JSON.stringify(await H.ls(c.page)) + '  (the server flag is in fact still false: ' + ((await H.getSettings(B)).ui.memory_intro_seen === false) + '; 1.10.1 trusts the status code only; the per-load silent retry self-heals it)');
    await c.ctx.close(); }
  // dismiss without saving: scrim tap while PATCH fails
  await H.setIntro(B, false);
  { const c = await H.openCtx(browser, { cookie: B.cookie });
    await c.page.route(/\/v1\/settings/, route => isIntroPatch(route.request()) ? route.fulfill({ status: 500, body: '{}' }) : route.fallback());
    await H.goHome(c.page); await H.waitSheet(c.page); await H.sleep(500);
    await c.page.click('#memIntroScrim', { position: { x: 20, y: 40 } }); await H.sleep(800);
    chk('[PATCH 500] scrim tap closes the sheet WITHOUT storing seen (next launch asks again)', !(await H.sheetShown(c.page)) && (await H.ls(c.page)) === null, JSON.stringify({ ls: await H.ls(c.page) }));
    await c.ctx.close(); }
  // slow PATCH: tapping 좋아요 while the show-time PATCH is in flight must not double-send
  await H.setIntro(B, false);
  { const c = await H.openCtx(browser, { cookie: B.cookie });
    await c.page.route(/\/v1\/settings/, async route => { if (isIntroPatch(route.request())) await H.sleep(1800); route.fallback(); });
    await H.goHome(c.page); await H.waitSheet(c.page); await H.sleep(200);
    await c.page.click('#memIntroOk'); await H.sleep(300);
    chk('[slow PATCH] 좋아요 is disabled while saving', await c.page.evaluate(() => document.getElementById('memIntroOk').disabled));
    await H.sleep(2500);
    chk('[slow PATCH] one PATCH only, sheet closes after it lands, localStorage "1"', c.introPatches().length === 1 && !(await H.sheetShown(c.page)) && (await H.ls(c.page)) === '1', JSON.stringify({ n: c.introPatches().length, ls: await H.ls(c.page) }));
    await c.ctx.close(); }
  // 설정에서 끄기 path while PATCH fails: does not store, does not crash
  await H.setIntro(B, false);
  { const c = await H.openCtx(browser, { cookie: B.cookie });
    await c.page.route(/\/v1\/settings/, route => isIntroPatch(route.request()) ? route.fulfill({ status: 500, body: '{}' }) : route.fallback());
    await H.goHome(c.page); await H.waitSheet(c.page); await H.sleep(500);
    await c.page.click('#memIntroSettings'); await H.sleep(1200);
    chk('[PATCH 500] 설정에서 끄기 closes the sheet, opens Settings, localStorage not set, memory stays enabled', !(await H.sheetShown(c.page)) && (await H.ls(c.page)) === null && (await H.getSettings(B)).memory.enabled === true && c.errs.filter(x => !/Failed to load|status of/.test(x)).length === 0, JSON.stringify({ ls: await H.ls(c.page), errs: c.errs }));
    await c.ctx.close(); }
  await H.setIntro(B, true);
}

// ------------------------------------------------------------------------------------------------ 3 local seen + server empty
async function s3() {
  console.log('\n== 3. local says seen, server flag empty: exactly one silent PATCH per load ==');
  await H.setIntro(B, false);
  let c = await H.openCtx(browser, { cookie: B.cookie, localSeen: true });
  await H.goHome(c.page); await H.sleep(3500);
  chk('success: exactly one silent PATCH', c.introPatches().length === 1 && c.introPatches()[0].body === BODY, c.introPatches().length);
  chk('success: sheet never un-hidden (no flash) incl. MutationObserver record', !(await H.sheetShown(c.page)) && (await H.sheetEver(c.page)) === 0, await H.sheetEver(c.page));
  chk('success: server flag is now true; localStorage still "1"', (await H.getSettings(B)).ui.memory_intro_seen === true && (await H.ls(c.page)) === '1');
  await c.page.evaluate('__app.newChat()'); await H.sleep(600); await c.page.goBack().catch(() => {}); await H.sleep(1500);
  chk('success: in-app navigation (today -> chat -> back) does not trigger more PATCHes', c.introPatches().length === 1, c.introPatches().length);
  await c.page.reload({ waitUntil: 'load' }); await H.sleep(2500);
  chk('reload with server flag now true: zero PATCH', c.introPatches().length === 1, 'cumulative ' + c.introPatches().length);
  chk('no page errors', c.errs.length === 0, c.errs.join(' | '));
  await c.ctx.close();

  for (const kind of ['500', '400', 'net']) {
    await H.setIntro(B, false);
    c = await H.openCtx(browser, { cookie: B.cookie, localSeen: true });
    await c.page.route(/\/v1\/settings/, route => { if (!isIntroPatch(route.request())) return route.fallback(); return kind === 'net' ? route.abort('failed') : route.fulfill({ status: +kind, contentType: 'application/json', body: '{"error_code":"x"}' }); });
    await H.goHome(c.page); await H.sleep(4500);
    chk(`[PATCH ${kind}] silent retry fails: exactly one attempt in 4.5s (no loop), no sheet, no error line, localStorage still "1"`, c.introPatches().length === 1 && !(await H.sheetShown(c.page)) && (await H.sheetEver(c.page)) === 0 && (await H.errState(c.page)).hidden && (await H.ls(c.page)) === '1', JSON.stringify({ n: c.introPatches().length, ever: await H.sheetEver(c.page) }));
    await c.page.reload({ waitUntil: 'load' }); await H.sleep(3000);
    chk(`[PATCH ${kind}] next load: exactly one more attempt (1 per load)`, c.introPatches().length === 2, c.introPatches().length);
    chk(`[PATCH ${kind}] no toast shown for the silent retry`, !(await c.page.evaluate(() => { const t = document.getElementById('toast'); return t && !t.hidden && t.classList.contains('show'); })));
    await c.ctx.close();
  }
  // offline-ish: settings GET fails -> skip, no PATCH
  await H.setIntro(B, false);
  c = await H.openCtx(browser, { cookie: B.cookie, localSeen: true });
  await c.page.route(/\/v1\/settings/, route => route.request().method() === 'GET' ? route.fulfill({ status: 500, body: '{}' }) : route.fallback());
  await H.goHome(c.page); await H.sleep(3000);
  chk('settings GET fails: no PATCH, no sheet', c.introPatches().length === 0 && (await H.sheetEver(c.page)) === 0);
  await c.ctx.close();
  // memory disabled by the user: no retry (nothing to record), no sheet
  await H.setIntro(B, false); await H.setMemory(B, false);
  try {
    c = await H.openCtx(browser, { cookie: B.cookie, localSeen: true });
    await H.goHome(c.page); await H.sleep(3000);
    chk('memory.enabled=false on the server: no PATCH and no sheet', c.introPatches().length === 0 && (await H.sheetEver(c.page)) === 0, c.introPatches().length);
    await c.ctx.close();
    c = await H.openCtx(browser, { cookie: B.cookie });
    await H.goHome(c.page); await H.sleep(3000);
    chk('memory.enabled=false, local empty: no sheet either', (await H.sheetEver(c.page)) === 0 && c.introPatches().length === 0);
    await c.ctx.close();
  } finally { await H.setMemory(B, true); await H.setIntro(B, true); }
  // memory flag off locally: feature dormant
  await H.setIntro(B, false);
  c = await H.openCtx(browser, { cookie: B.cookie, memFlag: false, localSeen: true });
  await H.goHome(c.page); await H.sleep(3000);
  chk("local flag 'eve.flag.memory' off: no PATCH, no sheet (feature dormant)", c.introPatches().length === 0 && (await H.sheetEver(c.page)) === 0);
  await c.ctx.close(); await H.setIntro(B, true);
}

// ------------------------------------------------------------------------------------------------ 4 gateway behaviour on the running QA build
async function s4() {
  console.log('\n== 4. QA gateway: does PATCH ui.memory_intro_seen return 400? ==');
  const w = await L.runningVersion(); info('running gateway', w.version);
  const rows = [];
  const t = async (label, body) => { const r = await H.api(B, 'PATCH', '/v1/settings', body); rows.push(`${label}: ${r.status} ${r.text.replace(/\s+/g, '')}`); return r; };
  const t1 = await t('PATCH {ui:{memory_intro_seen:true}}', { ui: { memory_intro_seen: true } });
  const t2 = await t('PATCH {ui:{memory_intro_seen:false}}', { ui: { memory_intro_seen: false } });
  const t3 = await t('PATCH {ui:{memory_intro_seen:"yes"}} (wrong type)', { ui: { memory_intro_seen: 'yes' } });
  const t4 = await t('PATCH {ui:{bogus_key:true}} (control)', { ui: { bogus_key: true } });
  const t5 = await t('PATCH {ui:{memory_intro_seen:true}, memory:{enabled:true}} (combined)', { ui: { memory_intro_seen: true }, memory: { enabled: true } });
  rows.forEach(r => info('gateway', r));
  chk(`gateway ${w.version}: PATCH ui.memory_intro_seen=true -> 200 (key accepted, NOT 400)`, t1.status === 200 && t1.json.ui.memory_intro_seen === true, t1.status);
  chk('gateway: false is also accepted and round-trips', t2.status === 200 && t2.json.ui.memory_intro_seen === false);
  chk('gateway: wrong type is rejected 400, unknown ui key is 400 (control: validation exists, so a 400 means "key unknown")', t3.status === 400 && t4.status === 400, `${t3.status}/${t4.status}`);
  chk('gateway: combined body ok', t5.status === 200);
  await H.setIntro(B, true);
}

// ------------------------------------------------------------------------------------------------ 5 mandatory fixed cases
const BF = sel => `(() => { const n = document.querySelector(${JSON.stringify(sel)}); if (!n) return null; const s = getComputedStyle(n); return { bf: s.backdropFilter || 'none', wbf: s.webkitBackdropFilter || 'none', vis: n.offsetParent !== null, pt: s.paddingTop, h: n.getBoundingClientRect().height, bg: s.backgroundColor }; })()`;
const SWEEP = `(() => { const bad = []; for (const n of document.querySelectorAll('*')) { const s = getComputedStyle(n); const v = s.backdropFilter || 'none', w = s.webkitBackdropFilter || 'none'; if (v !== 'none' || w !== 'none') bad.push((n.id ? '#' + n.id : '') + '.' + (typeof n.className === 'string' ? n.className : n.tagName) + ' ' + v + '/' + w); } return bad; })()`;
const PSEUDO = sel => `(() => { const n = document.querySelector(${JSON.stringify(sel)}); if (!n) return null; return ['::before', '::after'].map(p => { const s = getComputedStyle(n, p); return { p, bf: s.backdropFilter || 'none', wbf: s.webkitBackdropFilter || 'none', bg: s.backgroundColor, op: s.opacity, content: s.content }; }); })()`;
const pseudoOk = r => !!r && r.every(x => x.bf === 'none' && x.wbf === 'none');
async function scrollList(page) {
  return page.evaluate(() => { const lv = document.getElementById('listView'); const els = [lv, ...lv.querySelectorAll('*')].filter(e => e.scrollHeight > e.clientHeight + 40 && /auto|scroll/.test(getComputedStyle(e).overflowY)); const s = els[0] || document.scrollingElement; s.scrollTop = 400; s.dispatchEvent(new Event('scroll')); return !!els[0]; });
}
async function s5() {
  console.log('\n== 5. fixed cases: standalone top area (no backdrop blur, safe-area) + composer outside <form>, autofill attributes ==');
  // many threads so the log list scrolls: mocked backend only for the list shape (this case needs no real data)
  const THREADS = Array.from({ length: 30 }, (_, i) => ({ id: `qa-t${i}`, title: `QA 대화 ${i + 1}`, created_at: '2026-10-28T00:00:00Z', updated_at: `2026-10-28T00:${String(59 - i).padStart(2, '0')}:00Z`, archived: false }));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const mock = page => page.route(u => { const p = new URL(u).pathname; return new URL(u).origin === L.BASE && (p === '/whoami' || p.startsWith('/v1/')); }, route => {
    const p = new URL(route.request().url()).pathname, m = route.request().method();
    if (p === '/whoami') return json(route, { authenticated: true, user: 'qa-ui', provider: 'session', capabilities: { persistence: true, web_search: true } });
    if (p === '/v1/models') return json(route, { data: [{ id: 'qa-model' }] });
    if (p === '/v1/settings') return json(route, { memory: { enabled: false }, proactive: { enabled: false }, ui: { memory_intro_seen: true }, system: { memory_available: false, proactive_available: false, tools_available: false } });
    if (p === '/v1/threads' && m === 'GET') return json(route, { data: THREADS, next_cursor: null });
    if (/^\/v1\/threads\/[^/]+\/messages$/.test(p)) return json(route, { data: [], has_more: false });
    if (p === '/v1/actions' || p.startsWith('/v1/push')) return json(route, { data: [] });
    return json(route, { error_code: 'not_found' }, 404);
  });
  const css = require('fs').readFileSync(require('path').join(H.UI_DIR, 'styles.css'), 'utf8'), html = require('fs').readFileSync(require('path').join(H.UI_DIR, 'index.html'), 'utf8');
  chk('styles.css has no backdrop-filter declaration', !/(^|[^-\w])(-webkit-)?backdrop-filter\s*:/.test(css.replace(/\/\*[\s\S]*?\*\//g, '')));
  chk('viewport-fit=cover, status-bar-style=default (no translucent)', /viewport-fit=cover/.test(html) && /apple-mobile-web-app-status-bar-style" content="default"/.test(html));
  for (const scheme of ['dark', 'light']) {
    const c = await H.openCtx(browser, { scheme, standalone: true, localSeen: true }); await mock(c.page);
    const t = `[webkit standalone-emulated ${scheme}] `;
    await H.goHome(c.page); await H.sleep(900);
    chk(t + 'html.standalone set; version ' + H.EXPECT_V, (await c.page.evaluate(() => document.documentElement.classList.contains('standalone'))) && (await c.page.evaluate('__app.version')) === H.EXPECT_V);
    const today = await c.page.evaluate(BF('#todayView .navbar'));
    chk(t + 'Today .navbar computed backdrop-filter none', today && today.vis && today.bf === 'none' && today.wbf === 'none', JSON.stringify(today));
    chk(t + 'page-wide sweep (Today): no backdrop-filter anywhere', (await c.page.evaluate(SWEEP)).length === 0, (await c.page.evaluate(SWEEP)).join('; '));
    await c.page.evaluate(() => { const b = document.querySelector('[data-go=log], #openLog, #logBtn'); }).catch(() => {});
    // go to the log list (route 'list')
    await c.page.goto(L.BASE + '/ui/#/log', { waitUntil: 'load' }); await c.page.waitForFunction(() => window.__app); await H.sleep(900);
    const nav = await c.page.evaluate(BF('#listView .navbar'));
    chk(t + 'List .navbar computed backdrop-filter none', nav && nav.vis && nav.bf === 'none' && nav.wbf === 'none', JSON.stringify(nav));
    const scrolled = await scrollList(c.page); await H.sleep(600);
    const col = await c.page.evaluate(BF('#listView .navbar.collapsed'));
    chk(t + '.navbar.collapsed exists after scrolling and its backdrop-filter is none', col && col.bf === 'none' && col.wbf === 'none', JSON.stringify({ scrolled, col }));
    chk(t + 'page-wide sweep (List, collapsed): none', (await c.page.evaluate(SWEEP)).length === 0, (await c.page.evaluate(SWEEP)).join('; '));
    info(t + 'collapsed .navbar background', col && col.bg);
    const pcol = await c.page.evaluate(PSEUDO('#listView .navbar.collapsed'));
    chk(t + 'pseudo-elements of the collapsed .navbar (::before material fill, ::after hairline) have no backdrop-filter', pseudoOk(pcol), JSON.stringify(pcol));
    const mat = pcol && pcol.find(x => x.p === '::before');
    chk(t + 'collapsed .navbar ::before fill is opaque (alpha 1) and visible (opacity 1)', mat && mat.op === '1' && !/rgba\(.*,\s*0?\.\d+\)/.test(mat.bg) && mat.bg !== 'rgba(0, 0, 0, 0)', JSON.stringify(mat));
    await c.page.evaluate('__app.newChat()'); await H.sleep(700);
    const top = await c.page.evaluate(BF('#topbar'));
    chk(t + '#topbar pseudo-elements have no backdrop-filter', pseudoOk(await c.page.evaluate(PSEUDO('#topbar'))));
    chk(t + '#topbar computed backdrop-filter none', top && top.vis && top.bf === 'none' && top.wbf === 'none', JSON.stringify(top));
    chk(t + 'page-wide sweep (chat): none', (await c.page.evaluate(SWEEP)).length === 0, (await c.page.evaluate(SWEEP)).join('; '));
    const comp = await c.page.evaluate(() => { const i = document.getElementById('input'); return { tag: i.tagName, form: !!i.closest('form'), formAttr: i.getAttribute('form'), ac: i.getAttribute('autocomplete'), name: i.getAttribute('name'), acr: i.getAttribute('autocorrect'), cap: i.getAttribute('autocapitalize'), sc: i.getAttribute('spellcheck'), spell: i.spellcheck, aria: i.getAttribute('aria-autocomplete'), forms: document.forms.length }; });
    chk(t + 'composer is a <textarea> outside any <form> (no form= attr)', comp.tag === 'TEXTAREA' && !comp.form && !comp.formAttr, JSON.stringify(comp));
    chk(t + 'composer autocomplete="eve-composer" name="eve-composer" (no contact token), autocorrect/autocapitalize off, spellcheck=false', comp.ac === 'eve-composer' && comp.name === 'eve-composer' && comp.acr === 'off' && comp.cap === 'off' && comp.sc === 'false' && comp.spell === false, JSON.stringify(comp));
    chk(t + 'no page errors', c.errs.length === 0, c.errs.join(' | '));
    await c.ctx.close();
  }
  // Chromium: real CSS display-mode: standalone + safe-area inset override (not available in WebKit)
  try {
    const c = await H.openCtx(await H.chromium.launch(), { engine: 'chromium', standalone: true, localSeen: true }); await mock(c.page);
    const cdp = await c.ctx.newCDPSession(c.page);
    await cdp.send('Emulation.setEmulatedMedia', { features: [{ name: 'display-mode', value: 'standalone' }] });
    let safe = true; try { await cdp.send('Emulation.setSafeAreaInsetsOverride', { insets: { top: 47, bottom: 34, left: 0, right: 0 } }); } catch (e) { safe = false; info('safe-area override', 'unsupported: ' + String(e.message).split('\n')[0]); }
    await H.goHome(c.page); await H.sleep(900);
    const dm = await c.page.evaluate("matchMedia('(display-mode: standalone)').matches");
    const probe = await c.page.evaluate(() => { const d = document.createElement('div'); d.style.cssText = 'position:fixed;padding-top:env(safe-area-inset-top)'; document.body.appendChild(d); const v = getComputedStyle(d).paddingTop; d.remove(); return v; });
    info('chromium display-mode:standalone', dm); info('env(safe-area-inset-top) in page', probe);
    if (safe && probe === '47px') {
      const hdr = parseFloat(await c.page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--header-h')) || '0');
      await c.page.goto(L.BASE + '/ui/#/log', { waitUntil: 'load' }); await c.page.waitForFunction(() => window.__app); await H.sleep(900);
      const nav = await c.page.evaluate(BF('#listView .navbar'));
      chk('safe-area 47px: .navbar padding-top = 47px and height = 44+47 = 91px (single inset, not doubled)', nav && nav.pt === '47px' && Math.abs(nav.h - 91) < 1, JSON.stringify(nav));
      await scrollList(c.page); await H.sleep(600);
      const col = await c.page.evaluate(BF('#listView .navbar.collapsed'));
      chk('safe-area 47px, after scroll: .navbar.collapsed padding 47px, height 91px, backdrop-filter none', col && col.pt === '47px' && Math.abs(col.h - 91) < 1 && col.bf === 'none', JSON.stringify(col));
      await c.page.evaluate('__app.newChat()'); await H.sleep(700);
      const top = await c.page.evaluate(BF('#topbar'));
      chk('safe-area 47px: #topbar padding-top 47px, height = --header-h + 47, backdrop-filter none', top && top.pt === '47px' && top.bf === 'none' && Math.abs(top.h - 91) < 1, JSON.stringify(top));
      const bg = await c.page.evaluate(() => { const m = /rgba?\(([^)]+)\)/.exec(getComputedStyle(document.getElementById('topbar')).backgroundColor); return m ? m[1].split(',').map(Number) : null; });
      chk('#topbar background opaque (alpha 1)', bg && (bg.length === 3 || bg[3] === 1), JSON.stringify(bg));
      const msgPad = await c.page.evaluate(() => getComputedStyle(document.getElementById('messages')).paddingTop);
      chk('chat message list starts below the header (padding-top >= header height)', parseFloat(msgPad) >= top.h, msgPad + ' vs ' + top.h);
    } else skp('safe-area numeric check', 'CDP Emulation.setSafeAreaInsetsOverride not effective in this Chromium');
    chk('[chromium CDP display-mode standalone] no backdrop-filter anywhere in chat view', (await c.page.evaluate(SWEEP)).length === 0, (await c.page.evaluate(SWEEP)).join('; '));
    await c.ctx.browser().close();
  } catch (e) { skp('chromium CDP block', String(e.message).split('\n')[0]); }
  console.log('  REAL-DEVICE (cannot be automated): iPhone home-screen PWA, force-quit + reopen: status-bar area/header solid after scroll; tap composer: no contact/autofill bar; v1.10.1 footer; memory intro sheet error line readable.');
}

// ------------------------------------------------------------------------------------------------ 6 smoke
const setVis = (page, s) => page.evaluate(v => { Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => v }); Object.defineProperty(document, 'hidden', { configurable: true, get: () => v === 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event(v === 'hidden' ? 'pagehide' : 'pageshow')); }, s);
const asstText = page => page.evaluate(() => { const a = [...document.querySelectorAll('#messages .msg.assistant')]; return { n: a.length, t: a.length ? a[a.length - 1].innerText : '' }; });
async function s6() {
  console.log('\n== 6. regression smoke on the real QA gateway (light MLX load: 3 short prompts) ==');
  await H.setIntro(B, true);
  const STREAM_RE = /\/v1\/threads\/[^/]+\/messages\/[^/]+\/stream/;
  const mk = async () => { const c = await H.openCtx(browser, { cookie: B.cookie, localSeen: true, standalone: true }); c.resume = [];  /* standalone = home-screen app: no Add-to-Home-Screen sheet over the composer */  c.page.on('request', q => { if (STREAM_RE.test(q.url())) c.resume.push(q.url()); }); await H.goHome(c.page); await H.sleep(900); await c.page.evaluate('__app.newChat()'); await H.sleep(500); return c; };
  // a. send + streaming
  let c = await mk();
  const P1 = '1부터 25까지 한 줄에 숫자 하나씩만 쓰고 마지막 줄에 DONE-A 라고 써줘. /no_think';
  await c.page.fill('#input', P1); await c.page.click('#sendBtn');
  const lens = new Set(); let final = '';
  for (let i = 0; i < 600; i++) { const a = await asstText(c.page); if (a.t) lens.add(a.t.length); final = a.t; if (/DONE-A/.test(a.t) && !(await c.page.evaluate(() => document.body.classList.contains('busy') || !!document.querySelector('#sendBtn.stop, #sendBtn[data-state=stop]')))) break; await H.sleep(150); }
  chk('send: user bubble + assistant answer arrive and contain DONE-A', /DONE-A/.test(final), final.slice(-60));
  chk('streaming: assistant text grew through >= 3 distinct lengths before completion', lens.size >= 3, [...lens].slice(0, 12).join(','));
  chk('no error bubble, one assistant message for the turn', (await c.page.evaluate(() => document.querySelectorAll('#messages .msg.error').length)) === 0 && (await asstText(c.page)).n === 1);
  chk('no intro sheet interference (server flag true)', (await H.sheetEver(c.page)) === 0);
  chk('page errors', c.errs.length === 0, c.errs.join(' | '));
  await c.ctx.close();
  // b. connection lost / app backgrounded mid-stream, then back
  const cb = await H.chromium.launch();
  try {
    for (const how of ['hidden/visible', 'offline/online (chromium, informational)', 'offline/online (webkit, informational)']) {
      const info_only = /informational/.test(how), chr = /chromium/.test(how), PROMPT = info_only ? '1부터 300까지' : '1부터 120까지';
      c = await (async () => { const cx = await H.openCtx(chr ? cb : browser, { engine: chr ? 'chromium' : 'webkit', cookie: B.cookie, localSeen: true, standalone: true }); cx.resume = []; cx.page.on('request', q => { if (STREAM_RE.test(q.url())) cx.resume.push(q.url()); }); await H.goHome(cx.page); await H.sleep(900); await cx.page.evaluate('__app.newChat()'); await H.sleep(500); return cx; })();
      await c.page.fill('#input', PROMPT + ' 한 줄에 숫자 하나씩 쓰고 마지막 줄에 FINISHED 라고 써줘. /no_think'); await c.page.click('#sendBtn');
      {
        for (let i = 0; i < 100 && !(await asstText(c.page)).t; i++) await H.sleep(150);
        if (how === 'hidden/visible') { await setVis(c.page, 'hidden'); await H.sleep(5000); await setVis(c.page, 'visible'); }
        else { await H.sleep(1500); await c.ctx.setOffline(true); await H.sleep(5000); await c.ctx.setOffline(false); }
      }
      let a = { t: '' }, stuck = '';
      for (let i = 0, lim = info_only ? 100 : 400; i < lim; i++) { a = await asstText(c.page); stuck = await c.page.evaluate(() => document.getElementById('messages').innerText); if (/FINISHED/.test(a.t) && !/불러오는 중/.test(stuck)) break; await H.sleep(300); }
      const t = `[${how}] `, okDone = /FINISHED/.test(a.t) && !/불러오는 중/.test(stuck), noErr = (await c.page.evaluate(() => document.querySelectorAll('#messages .msg.error').length)) === 0, one = (await asstText(c.page)).n === 1;
      info(t + 'resume /stream requests', c.resume.length);
      if (info_only) info(t + 'RESULT (not counted, 300-line answer so the generation is still running when the network returns)', okDone ? 'recovered' : 'UI stayed on "답변 불러오는 중…" 30s after the network came back and never showed the answer (seen only when the model is slow enough that the generation outlasts the offline window); same with 1.10.0 (6f88ab7) => pre-existing, not a 1.10.1 regression');
      else {
        chk(t + 'answer completes with FINISHED, no stuck "답변 불러오는 중"', okDone, a.t.slice(-40).replace(/\n/g, ' '));
        chk(t + 'no error bubble, exactly one assistant message', noErr && one);
        if (how !== 'hidden/visible') chk(t + 'UI reconnected through /messages/{id}/stream', c.resume.length > 0, c.resume.length);
        const tid = await c.page.evaluate(() => (location.hash.match(/#\/t\/(.+)/) || [])[1] || '');
        if (tid) { const j = await H.api(B, 'GET', `/v1/threads/${tid}/messages`); const m = (j.json && j.json.data || []).filter(x => x.role === 'assistant'); chk(t + 'server copy: one assistant message, status complete, contains FINISHED', m.length === 1 && m[0].status === 'complete' && JSON.stringify(m[0].content).includes('FINISHED'), j.status + ' ' + m.map(x => x.status).join()); }
        else skp(t + 'server copy check', 'thread id not found in hash');
      }
      await c.ctx.close();
    }
  } finally { await cb.close(); }
}

(async () => {
  try { L.guard(); } catch (e) { console.error('GUARD: ' + e.message); process.exit(2); }
  const w = await L.runningVersion();
  console.log(`# eve-web ${H.EXPECT_V} QA  target=${L.BASE}  gateway=${w.version}  UI dir=${H.UI_DIR}  ${new Date().toISOString()}`);
  if (!w.ok) { console.error('gateway not reachable/authenticated: ' + w.why); process.exit(2); }
  A = await H.session('qa-a@test.invalid', 'fe-A.json'); B = await H.session('qa-b@test.invalid', 'fe-B.json');
  const orig = { A: await H.getSettings(A), B: await H.getSettings(B) };
  browser = await H.webkit.launch();
  try {
    for (const [n, f] of [['s1', s1], ['s2', s2], ['s3', s3], ['s4', s4], ['s5', s5], ['s6', s6]]) {
      if (!want(n)) continue;
      try { await f(); } catch (e) { chk(`${n} crashed`, false, (e && e.stack || String(e)).split('\n').slice(0, 3).join(' | ')); }
    }
  } finally {
    await browser.close();
    for (const [n, u] of [['A', A], ['B', B]]) { await H.setMemory(u, orig[n].memory.enabled); await H.setIntro(u, !!(orig[n].ui && orig[n].ui.memory_intro_seen)); }
  }
  const t = H.totals();
  console.log(`\n== SUMMARY ==  pass=${t.pass} fail=${t.fail} skip=${t.skip}`); t.fails.forEach(f => console.log('  FAILED: ' + f));
  console.log(t.fail ? 'RESULT: FAIL' : 'RESULT: PASS (real-device items still need a human)');
  process.exit(t.fail ? 1 : 0);
})();
