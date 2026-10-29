'use strict';
// eve-web 2.0.0 (PR #37): automatic tool selection UI. QA 8002 only, light MLX load (short prompts + a few 300-line resume runs).
//   A  request bodies carry no web_search / web toggle (new chat, 2nd message, regenerate, retry, resume, Today composer), legacy localStorage is harmless
//   B  leftover web-toggle UI is gone
//   C  tool activity / sources / notes render from SERVER events. The QA gateway is 5.0.0 (no auto tools), so the 6.0.0 SSE/metadata contract
//      (eve-serving PR #158 CHANGELOG: tool_call/tool_result, persisted.fresh + query-free tools[], messages.metadata{fresh,tools}) is injected by
//      rewriting the REAL gateway response with Playwright route.fetch; the thread/messages are real rows on the QA gateway.
//   node tests/e2e/fe1101/s8_autotools.js [--only A,B,C]
const H = require('./lib'); const { L, chk, info } = H;
const only = process.argv.includes('--only') ? process.argv[process.argv.indexOf('--only') + 1].split(',') : ['A', 'B', 'C'];
const SHORT = '지금 한 단어로만 대답해: 네 /no_think';
const BAD = /web[_ -]?search|websearch|"web"|web_fetch|\bweb\b/i;
const ALLOWED = new Set(['model', 'messages', 'stream', 'stream_options', 'thread_id', 'replace_message_id']);
const lastAsst = page => page.evaluate(() => { const a = [...document.querySelectorAll('#messages .msg.assistant')]; const e = a[a.length - 1]; return e ? { t: e.innerText, actions: !!e.querySelector('.actions'), n: a.length } : { t: '', actions: false, n: 0 }; });
async function waitDone(page, n = 1, ms = 150000) { for (let i = 0; i < ms / 300; i++) { const a = await lastAsst(page); if (a.actions && a.n >= n && a.t.trim()) return a; await H.sleep(300); } return await lastAsst(page); }
const send = async (page, text) => { await page.fill('#input', text); await page.click('#sendBtn'); };
const LEGACY = () => { if (sessionStorage.__seeded) return; sessionStorage.__seeded = '1'; try {
  localStorage.setItem('llmchat.webSearch', '1'); localStorage.setItem('llmchat.autoHintSeen', '1');
  localStorage.setItem('llmchat.settings', JSON.stringify({ webSearch: true, web_search: 'auto', web: { on: true }, mode: 'force' })); } catch {} };
function track(ctx) { const reqs = []; ctx.on('request', q => { const u = new URL(q.url()); if (u.origin === L.BASE && u.pathname.startsWith('/v1')) reqs.push({ m: q.method(), url: q.url(), path: u.pathname, search: u.search, body: q.postData() || '' }); }); return reqs; }
const keysDeep = (o, out = []) => { if (o && typeof o === 'object') for (const [k, v] of Object.entries(o)) { out.push(k); if (k !== 'messages') keysDeep(v, out); } return out; };
const comp = reqs => reqs.filter(r => r.m === 'POST' && r.path === '/v1/chat/completions');
function bodyOk(tag, r, extra) { let j = null; try { j = JSON.parse(r.body); } catch {}
  const keys = j ? Object.keys(j) : [], bad = keys.filter(k => !ALLOWED.has(k)); const content = j ? JSON.stringify(j.messages || []) : '';
  chk(`${tag} body keys only {model,messages,stream,stream_options,thread_id,replace_message_id}`, !!j && bad.length === 0, bad.join(',') || r.body.slice(0, 120));
  chk(`${tag} body has no web / web_search / tool field or text (outside message content)`, !!j && !BAD.test(JSON.stringify(Object.assign({}, j, { messages: undefined }))) && !/tool/i.test(keys.join(',')));
  if (extra) extra(j); return j; }

async function partA(browser, engine, B) {
  const t = `[A ${engine}] `;
  const c = await H.openCtx(browser, { engine, cookie: B.cookie, localSeen: true, standalone: true });
  await c.ctx.addInitScript(LEGACY); const reqs = track(c.ctx);
  try {
    await H.goHome(c.page); await H.sleep(1200);
    const st = await c.page.evaluate(() => ({ ws: localStorage.getItem('llmchat.webSearch'), hint: localStorage.getItem('llmchat.autoHintSeen'), settings: localStorage.getItem('llmchat.settings') }));
    chk(t + 'legacy keys llmchat.webSearch / llmchat.autoHintSeen are removed on load; legacy extra keys in llmchat.settings do not break the app', st.ws === null && st.hint === null && !!(await c.page.evaluate('!!window.__app')), JSON.stringify(st));
    await c.page.evaluate('__app.newChat()'); await H.sleep(500);
    await send(c.page, SHORT); let a = await waitDone(c.page);
    chk(t + 'new chat answers', a.actions && a.t.trim().length > 0, a.t.slice(0, 40));
    let cs = comp(reqs); chk(t + 'one POST /v1/chat/completions so far', cs.length === 1, String(cs.length));
    const j1 = bodyOk(t + 'new chat:', cs[0]); chk(t + 'new chat: thread_id "new"', j1?.thread_id === 'new');
    await send(c.page, SHORT); a = await waitDone(c.page, 2); cs = comp(reqs);
    const j2 = bodyOk(t + '2nd message:', cs[1]); chk(t + '2nd message: existing thread id, 3 messages in history', !!j2 && j2.thread_id && j2.thread_id !== 'new' && j2.messages.length === 3, JSON.stringify([j2?.thread_id, j2?.messages?.length]));
    // regenerate
    await c.page.click('#messages .msg.assistant:last-of-type .actions .r').catch(async () => { await c.page.locator('#messages .msg.assistant .actions .r').last().click(); });
    for (let i = 0; i < 200 && comp(reqs).length < 3; i++) await H.sleep(150); await waitDone(c.page, 2); cs = comp(reqs);
    const j3 = bodyOk(t + 'regenerate:', cs[2]); chk(t + 'regenerate: replace_message_id set', !!j3?.replace_message_id);
    // retry after a failure (500 once)
    let failed = false; await c.ctx.route(u => new URL(u).pathname === '/v1/chat/completions', async route => { if (!failed && route.request().method() === 'POST') { failed = true; return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'upstream_unavailable', message: 'x' } }) }); } return route.fallback(); });
    const before = comp(reqs).length; await send(c.page, SHORT);
    await c.page.waitForSelector('#messages .msg.error .btn.retry', { timeout: 20000 }); await c.page.click('#messages .msg.error .btn.retry');
    for (let i = 0; i < 200 && comp(reqs).length < before + 2; i++) await H.sleep(150); await waitDone(c.page, 3); cs = comp(reqs);
    chk(t + 'retry: failed POST + retried POST were both sent', cs.length === before + 2, `${cs.length - before}`);
    bodyOk(t + 'failed send:', cs[before]); bodyOk(t + 'retry:', cs[before + 1]);
    await c.ctx.unroute(u => new URL(u).pathname === '/v1/chat/completions').catch(() => {});
    // resume after offline (300 lines, mid generation)
    await c.page.evaluate('__app.newChat()'); await H.sleep(500); const nb = comp(reqs).length;
    await send(c.page, '1부터 300까지 한 줄에 숫자 하나씩 쓰고 마지막 줄에 FINISHED 라고 써줘. /no_think');
    for (let i = 0; i < 200 && !(await lastAsst(c.page)).t.trim(); i++) await H.sleep(100); await H.sleep(1500);
    const sr0 = reqs.length; await c.ctx.setOffline(true); await H.sleep(3000); await c.ctx.setOffline(false);
    for (let i = 0; i < 200; i++) { const x = await lastAsst(c.page); if (/FINISHED/.test(x.t) && x.actions) break; await H.sleep(300); }
    const after = reqs.slice(sr0), st2 = after.filter(r => /\/stream$/.test(r.path));
    info(t + 'resume', JSON.stringify(after.map(r => `${r.m} ${r.path.replace(/[0-9a-f-]{20,}/g, '…')}${r.search}`)));
    chk(t + 'resume: GET .../stream sent (or generation ended before the network returned)', st2.length > 0 || /FINISHED/.test((await lastAsst(c.page)).t), `${st2.length}`);
    for (const r of reqs.slice(sr0 - 1)) if (r.m === 'POST' && r.path === '/v1/chat/completions') bodyOk(t + 'resume-run POST:', r);
    chk(t + 'resume: no request carries a web field in its URL query or body', reqs.every(r => !/web/i.test(r.search) && !(r.m !== 'GET' && r.path !== '/v1/chat/completions' && BAD.test(r.body))), reqs.filter(r => /web/i.test(r.search)).map(r => r.url).join(' '));
    chk(t + 'all /v1 request URLs: no web* query parameter', reqs.every(r => !/web/i.test(r.search)));
    // Today composer
    await H.goHome(c.page, '#/'); await H.sleep(1200);
    if (await c.page.$('#tInput')) { const nb2 = comp(reqs).length; await c.page.fill('#tInput', SHORT); await c.page.click('#tSend').catch(() => {});
      for (let i = 0; i < 100 && comp(reqs).length < nb2 + 1; i++) await H.sleep(150);
      const cs2 = comp(reqs); if (cs2.length > nb2) bodyOk(t + 'Today composer:', cs2[nb2]); else info(t + 'Today composer did not send (route?)'); await waitDone(c.page);
    } else info(t + 'no #tInput on this route');
    chk(t + 'no page errors', c.errs.filter(x => !/Load failed|net::ERR|Failed to load resource|NetworkError|aborted/i.test(x)).length === 0, c.errs.join(' | '));
  } catch (e) { chk(t + 'crashed', false, String(e.stack || e).split('\n').slice(0, 3).join(' | ')); }
  finally { await c.ctx.close(); }
}

async function partB(browser, engine, B) {
  const t = `[B ${engine}] `;
  const c = await H.openCtx(browser, { engine, cookie: B.cookie, localSeen: true, standalone: true });
  await c.ctx.addInitScript(LEGACY);
  try {
    await H.goHome(c.page); await H.sleep(1200); await c.page.evaluate('__app.newChat()'); await H.sleep(900);
    const r = await c.page.evaluate(() => {
      const ids = ['plusBtn', 'plusMenu', 'webBtn', 'tPlus', 'tMenu', 'tWeb'].filter(i => document.getElementById(i));
      const texts = [...document.querySelectorAll('[aria-label],[title],button,label,option,li,p,span,small')].map(e => (e.getAttribute('aria-label') || '') + ' ' + (e.getAttribute('title') || '') + ' ' + (e.children.length ? '' : e.textContent)).filter(s => /웹\s*검색|웹검색|web\s*search|검색 (켜|끄)|자동 검색|검색 모드|인터넷/i.test(s));
      const ta = document.getElementById('input'), r = ta.getBoundingClientRect(), form = ta.closest('.composer, #composer, form, .cbox') || ta.parentElement, fr = form.getBoundingClientRect();
      const send = document.getElementById('sendBtn').getBoundingClientRect(); const t = document.getElementById('tInput');
      return { ids, texts, ta: [r.x, r.width], form: [fr.x, fr.width], send: [send.x, send.width], vw: innerWidth, settings: document.getElementById('settings') ? /웹|검색/.test(document.getElementById('settings').textContent) : null, tOk: !!t };
    });
    chk(t + 'no leftover web-toggle / + menu elements in the DOM (#plusBtn #plusMenu #webBtn #tPlus #tMenu #tWeb)', r.ids.length === 0, r.ids.join(','));
    chk(t + 'no leftover web-search toggle copy (aria-label/title/text)', r.texts.length === 0, r.texts.join(' | ').slice(0, 200));
    chk(t + 'settings dialog has no web-search setting', r.settings === false || r.settings === null, String(r.settings));
    chk(t + 'composer layout: textarea starts near the left edge (no empty gap where the + button was) and ends before the send button', r.ta[0] - r.form[0] <= 20 && r.ta[0] + r.ta[1] <= r.send[0] + 1, JSON.stringify(r));
    const css = await c.page.evaluate(async () => { const t = await (await fetch('/ui/styles.css?v=x')).text(); return { plus: /#plusMenu|#tMenu|\.plus-menu|#webBtn/.test(t) }; });
    info(t + 'styles.css still has + menu rules: ' + css.plus);
    chk(t + 'no page errors', c.errs.filter(x => !/Load failed|net::ERR|Failed to load resource|NetworkError/i.test(x)).length === 0, c.errs.join(' | '));
  } catch (e) { chk(t + 'crashed', false, String(e.stack || e).split('\n').slice(0, 3).join(' | ')); }
  finally { await c.ctx.close(); }
}

// ---- C: server events -> UI. v = { name, calls:[...events inserted before the first answer token], persisted:{...fields added}, meta:{fresh,tools} injected into GET messages }
const SRC = [{ n: 1, title: '서울 날씨 예보', url: 'https://weather.example.com/seoul', domain: 'weather.example.com', published: (d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`)(new Date()) }, { n: 2, title: '기상청', url: 'https://www.kma.example.org/x', domain: 'kma.example.org' }];
const TC = (id, q) => ({ object: 'chat.completion.tool_call', id, tool: 'web_search', args: { query: q }, round: 1, selected_by: 'model' });
const TR = (id, o) => Object.assign({ object: 'chat.completion.tool_result', id, tool: 'web_search', selected_by: 'model', elapsed_ms: 400 }, o);
const VARS = [
  { name: 'ok+sources', calls: [TC('tc_1', '서울 오늘 날씨'), TR('tc_1', { status: 'ok', count: 2, blocked_count: 0, sources: SRC })], persisted: { fresh: 'ok', tools: [{ tool: 'web_search', selected_by: 'model', status: 'ok', count: 2 }] }, sources: true, note: null, tq: '서울 오늘 날씨' },
  { name: 'unverified+timeout', calls: [TC('tc_1', '오늘 환율'), TR('tc_1', { status: 'timeout', error_code: 'timeout', count: 0, sources: [] })], persisted: { fresh: 'unverified', tools: [{ tool: 'web_search', selected_by: 'model', status: 'timeout', error_code: 'timeout' }] }, sources: false, note: 'timeout', title: '검색 시간이 초과됐어요' },
  { name: 'unverified+empty-result', calls: [TC('tc_1', '아주 희귀한 검색어'), TR('tc_1', { status: 'ok', count: 0, blocked_count: 0, sources: [] })], persisted: { fresh: 'unverified', tools: [{ tool: 'web_search', selected_by: 'model', status: 'ok', count: 0 }] }, sources: false, note: 'empty', title: '검색 결과를 찾지 못했어요' },
  { name: 'unverified+no-tool', calls: [], persisted: { fresh: 'unverified', tools: [] }, sources: false, note: 'nosearch', title: '최신 정보를 확인하지 못했어요' },
  { name: 'ok (fresh authority) despite failed tool_result', calls: [TC('tc_1', '뉴스'), TR('tc_1', { status: 'error', error_code: 'search_unavailable', count: 0, sources: [] })], persisted: { fresh: 'ok', tools: [{ tool: 'web_search', selected_by: 'model', status: 'ok', count: 1 }] }, sources: false, note: null },
  { name: 'unknown tool + web_fetch', calls: [{ object: 'chat.completion.tool_call', id: 'x1', tool: 'quantum_tool', args: { a: 1 }, round: 1 }, { object: 'chat.completion.tool_call', id: 'f1', tool: 'web_fetch', args: { url: 'https://www.example.com/notes' }, round: 1, selected_by: 'model' }, TR('f1', { tool: 'web_fetch', status: 'ok', count: 1, sources: [SRC[0]] })], persisted: { fresh: 'ok', tools: [{ tool: 'web_fetch', selected_by: 'model', status: 'ok', count: 1 }] }, sources: true, note: null },
];
function rewriteSSE(text, v) {
  const blocks = text.split('\n\n'); const out = []; let inserted = false;
  for (const b of blocks) {
    const m = b.match(/(?:^|\n)data: (\{.*\})\s*$/s); let j = null; try { j = m && JSON.parse(m[1]); } catch {}
    if (j && j.object === 'chat.completion.chunk' && !inserted && (j.choices || []).some(x => x.delta && (x.delta.content || x.delta.reasoning_content))) { inserted = true; for (const e of v.calls) out.push('data: ' + JSON.stringify(e)); }
    if (j && j.object === 'chat.completion.persisted') { Object.assign(j, v.persisted); out.push('data: ' + JSON.stringify(j)); continue; }
    out.push(b);
  }
  if (!inserted) { const i = out.findIndex(x => /chat\.completion"/.test(x)); out.splice(i < 0 ? 0 : i, 0, ...v.calls.map(e => 'data: ' + JSON.stringify(e))); }
  return out.join('\n\n');
}
const noteInfo = page => page.evaluate(() => { const a = [...document.querySelectorAll('#messages .msg.assistant')]; const e = a[a.length - 1]; const n = e && e.querySelector('.tnote'); return { n: a.length, note: n ? { key: n.dataset.key, text: n.innerText.trim() } : null, chips: e ? [...e.querySelectorAll('.sources .sc .sc-n')].map(x => x.textContent) : [], tline: !!(e && e.querySelector('.tline')), md: e ? (e.querySelector('.md') || {}).innerText : '' }; });

async function partC(browser, engine, B) {
  for (const v of VARS) {
    const t = `[C ${engine} ${v.name}] `;
    const c = await H.openCtx(browser, { engine, cookie: B.cookie, localSeen: true, standalone: true }); const reqs = track(c.ctx);
    const meta = {};   // message_id -> {fresh,tools,sources}
    try {
      await c.ctx.route(u => new URL(u).pathname === '/v1/chat/completions', async route => {
        if (route.request().method() !== 'POST') return route.fallback();
        const r = await route.fetch(); const body = await r.text(); const nb = rewriteSSE(body, v);
        const pm = nb.match(/"object": ?"chat\.completion\.persisted"[^\n]*/); try { const pj = JSON.parse(pm[0].replace(/^"object"/, '{"object"') .replace(/^\{"object"/, '{"object"')); } catch {}
        for (const line of nb.split('\n')) if (line.startsWith('data: {') && /chat\.completion\.persisted/.test(line)) { const pj = JSON.parse(line.slice(6)); meta[pj.message_id] = { fresh: pj.fresh, tools: pj.tools }; }
        return route.fulfill({ status: r.status(), headers: r.headers(), body: nb });
      });
      // GET messages: what a 6.0.0 gateway returns (metadata {fresh, tools[]} on every assistant message; sources if cited)
      await c.ctx.route(u => /^\/v1\/threads\/[^/]+\/messages$/.test(new URL(u).pathname), async route => {
        if (route.request().method() !== 'GET') return route.fallback(); const r = await route.fetch(); let j; try { j = await r.json(); } catch { return route.fulfill({ response: r }); }
        for (const m of j.data || []) if (m.role === 'assistant' && meta[m.id]) { m.metadata = Object.assign({}, m.metadata, { fresh: meta[m.id].fresh, tools: meta[m.id].tools }); if (v.sources) m.sources = SRC; }
        return route.fulfill({ response: r, body: JSON.stringify(j) });
      });
      await H.goHome(c.page); await H.sleep(1200); await c.page.evaluate('__app.newChat()'); await H.sleep(500);
      await send(c.page, SHORT); let a = await waitDone(c.page); await H.sleep(500);
      let n = await noteInfo(c.page);
      chk(t + 'answer shown, status line gone after the first token, one assistant bubble', a.actions && !n.tline && n.n === 1, JSON.stringify({ tline: n.tline, n: n.n }));
      chk(t + (v.sources ? 'source chips from tool_result/persisted sources: ' + SRC[0].domain : 'no source chips'), v.sources ? n.chips.includes('weather.example.com') || n.chips.includes('www.example.com') || n.chips.includes('example.com') : n.chips.length === 0, JSON.stringify(n.chips));
      if (v.note) { await H.sleep(300); n = await noteInfo(c.page); chk(t + `persisted.fresh=unverified → note "${v.title}" (key ${v.note}) visible after the answer`, !!n.note && n.note.key === v.note && n.note.text.includes(v.title), JSON.stringify(n.note)); }
      else chk(t + 'no failure note', n.note === null, JSON.stringify(n.note));
      if (v.tq) { await c.page.locator('#messages .msg.assistant .sources .sc').first().click(); await H.sleep(500);
        const tq = await c.page.evaluate(() => { const e = [...document.querySelectorAll('#messages .msg.assistant')].pop().querySelector('.src-list .tq'); return e ? e.innerText : null; });
        chk(t + 'opened source list shows the model-written query line (검색어 “…” · Eve가 필요하다고 판단해 검색했어요)', !!tq && tq.includes(v.tq) && tq.includes('Eve가 필요하다고'), String(tq));
        const dt = await c.page.evaluate(() => [...document.querySelectorAll('#messages .msg.assistant')].pop().querySelector('.src-item .sd')?.innerText); chk(t + 'source item shows the server-provided `published` date (오늘) next to the domain (§7)', /오늘/.test(dt || ''), String(dt)); }
      // reload + reopen: server messages carry metadata → note survives; queries do not (query-free tools[])
      const tid = await c.page.evaluate(() => (location.hash.match(/#\/t\/(.+)/) || [])[1]);
      await c.page.reload({ waitUntil: 'load' }); await c.page.waitForFunction(() => window.__app, null, { timeout: 15000 }); await H.sleep(1800);
      n = await noteInfo(c.page); if (n.n === 0) { await c.page.evaluate(id => location.hash = '#/t/' + id, tid); await H.sleep(2000); n = await noteInfo(c.page); }
      const sameKey = v.note && n.note && n.note.key === v.note; chk(t + 'after RELOAD the thread shows a note' + (v.note ? '' : ' (none expected)'), v.note ? !!n.note : n.note === null, JSON.stringify({ n: n.n, note: n.note }));
      if (v.note && n.note && !sameKey) info(t + `after reload the note degrades from ${v.note} to ${n.note.key} (server tools[] has no count/query: status ok, count unknown)`);
      if (v.sources) chk(t + 'after reload the source chips are still there', n.chips.length > 0, JSON.stringify(n.chips));
      // leave the thread and reopen it from the list
      await c.page.evaluate('__app.newChat()'); await H.sleep(700);
      await c.page.evaluate(() => { location.hash = '#/log'; }); await H.sleep(900);
      const row = c.page.locator(`#convList li.row[data-id="${tid}"]`); if (await row.count()) { await row.first().click(); await H.sleep(2500); }
      n = await noteInfo(c.page);
      chk(t + 'after leaving and REOPENING the thread: note state unchanged', n.n === 1 && (v.note ? !!n.note : n.note === null), JSON.stringify({ n: n.n, note: n.note }));
      const m = reqs.filter(r => r.m === 'POST' && r.path === '/v1/chat/completions'); chk(t + 'request body clean', m.length === 1 && !BAD.test(m[0].body.replace(/"messages".*?\],?/s, '')) && !('web_search' in JSON.parse(m[0].body)));
      chk(t + 'no page errors', c.errs.filter(x => !/Load failed|net::ERR|Failed to load resource|NetworkError/i.test(x)).length === 0, c.errs.join(' | '));
    } catch (e) { chk(t + 'crashed', false, String(e.stack || e).split('\n').slice(0, 3).join(' | ')); }
    finally { await c.ctx.close(); }
  }
}
(async () => {
  const w = await L.runningVersion(); console.log(`# s8 auto tools  gateway=${w.version}  UI=${H.UI_DIR}  expected UI ${H.EXPECT_V}`);
  const B = await H.session('qa-b@test.invalid', 'fe-B.json'); await H.setIntro(B, true);
  const wk = await H.webkit.launch(), cr = await H.chromium.launch();
  try { for (const [eng, br] of [['webkit', wk], ['chromium', cr]]) {
    if (only.includes('A')) { console.log(`\n== A ${eng}: request bodies`); await partA(br, eng, B); }
    if (only.includes('B')) { console.log(`\n== B ${eng}: leftover UI`); await partB(br, eng, B); }
    if (only.includes('C')) { console.log(`\n== C ${eng}: tool events / persisted.fresh`); await partC(br, eng, B); }
  } } finally { await wk.close(); await cr.close(); }
  const t = H.totals(); console.log(`\n== s8 SUMMARY pass=${t.pass} fail=${t.fail}`); t.fails.forEach(f => console.log('  FAILED: ' + f)); process.exit(t.fail ? 1 : 0);
})();
