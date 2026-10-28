'use strict';
// eve-web 1.10.2 (PR #28): offline -> online while an answer is still generating must resume via GET .../stream + Last-Event-ID, keep the live bubble, no duplicate / lost text.
//   node tests/e2e/fe1101/s7_resume.js [--reps N]   (QA 8002 only; each run = one 300-line answer from MLX, ~15-25 s)
const H = require('./lib'); const { L, chk: chk0, info } = H;
const REPS = process.argv.includes('--reps') ? +process.argv[process.argv.indexOf('--reps') + 1] : 3;
const STREAM_RE = /\/v1\/threads\/[^/]+\/messages\/[^/]+\/stream/;
const PROMPT = '1부터 300까지 한 줄에 숫자 하나씩 쓰고 마지막 줄에 FINISHED 라고 써줘. /no_think';
const setVis = (page, v) => page.evaluate(x => { Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => x }); Object.defineProperty(document, 'hidden', { configurable: true, get: () => x === 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event(x === 'hidden' ? 'pagehide' : 'pageshow')); }, v);
const bubble = page => page.evaluate(() => { const a = [...document.querySelectorAll('#messages .msg.assistant')]; const e = a[a.length - 1]; return { n: a.length, t: e ? e.innerText : '', tagged: !!(e && e.__qa), resuming: /불러오는 중/.test(document.getElementById('messages').innerText), err: document.querySelectorAll('#messages .msg.error').length }; });
const nums = t => t.split('\n').map(x => x.trim()).filter(x => /^\d+$/.test(x)).map(Number);
const seqOk = a => a.length === 300 && a.every((v, i) => v === i + 1);
const txt = c => typeof c === 'string' ? c : JSON.stringify(c);

async function run(browser, engine, how, ms, B, attempt = 1) {
  const R = [], chk = (n, ok, d) => R.push([n, ok, d]); let invalid = false;
  const tag = `[${engine} ${how}${ms ? ' ' + ms / 1000 + 's' : ''}] `;
  const c = await H.openCtx(browser, { engine, cookie: B.cookie, localSeen: true, standalone: true });
  const stream = []; c.page.on('request', q => { if (STREAM_RE.test(q.url())) stream.push({ t: Date.now(), lei: q.headers()['last-event-id'], url: q.url() }); });
  try {
    await H.goHome(c.page); await H.sleep(900); await c.page.evaluate('__app.newChat()'); await H.sleep(500);
    await c.page.fill('#input', PROMPT); await c.page.click('#sendBtn');
    for (let i = 0; i < 200 && !(await bubble(c.page)).t.trim(); i++) await H.sleep(100);
    await H.sleep(1500);
    await c.page.evaluate(() => { const a = [...document.querySelectorAll('#messages .msg.assistant')]; a[a.length - 1].__qa = true; });   // marks the live bubble node
    const before = await bubble(c.page), tid = await c.page.evaluate(() => (location.hash.match(/#\/t\/(.+)/) || [])[1] || '');
    const t0 = Date.now(); let onlineAt;
    if (how === 'offline') { await c.ctx.setOffline(true); await H.sleep(ms); onlineAt = Date.now(); await c.ctx.setOffline(false); }
    else { await setVis(c.page, 'hidden'); await H.sleep(ms); onlineAt = Date.now(); await setVis(c.page, 'visible'); }
    const srvMid = tid && !/^tmp-/.test(tid) ? (await H.api(B, 'GET', `/v1/threads/${tid}/messages`)).json : null;
    const generating = !!(srvMid && (srvMid.data || []).some(m => m.role === 'assistant' && m.status === 'generating'));
    let b = before, grew = false, firstGrow = 0;
    for (let i = 0; i < 400; i++) { b = await bubble(c.page); if (b.t.length > before.t.length + 20 && !grew) { grew = true; firstGrow = Date.now() - onlineAt; } if (/FINISHED/.test(b.t) && !b.resuming) break; await H.sleep(300); }
    await H.sleep(800); b = await bubble(c.page);
    const post = stream.filter(r => r.t >= onlineAt - 50);
    const srv = tid ? (await H.api(B, 'GET', `/v1/threads/${tid}/messages`)).json : null, am = srv ? (srv.data || []).filter(m => m.role === 'assistant') : [];
    const sn0 = am[0] && typeof am[0].content === 'string' ? nums(am[0].content) : [];
    if (!seqOk(sn0) && (generating === false || how === 'offline') && JSON.stringify(sn0) === JSON.stringify(nums(b.t))) invalid = true;   // the model itself stopped early (UI == server): not a UI sample
    info(tag + 'summary', JSON.stringify({ midGeneration: generating, textBefore: before.t.length, stream: post.map(r => ({ afterMs: r.t - onlineAt, lastEventId: r.lei })), firstGrowMs: firstGrow }));
    chk(tag + 'was really mid-generation when the network returned (server message status generating)', how === 'offline' ? generating : true, JSON.stringify({ generating }));
    if (how === 'offline') chk(tag + 'UI re-requested GET .../stream with Last-Event-ID after the network returned', post.length > 0 && post.some(r => r.lei !== undefined && /^\d+$/.test(String(r.lei))), JSON.stringify(post.map(r => ({ afterMs: r.t - onlineAt, lei: r.lei }))));
    chk(tag + 'answer completes: FINISHED shown, no stuck "답변 불러오는 중", no error bubble', /FINISHED/.test(b.t) && !b.resuming && b.err === 0, `${b.t.length} chars resuming=${b.resuming} err=${b.err}`);
    chk(tag + 'live bubble node was kept (not replaced by initServer) and it is the only assistant message', b.tagged && b.n === 1, JSON.stringify({ tagged: b.tagged, n: b.n }));
    const un = nums(b.t); chk(tag + 'UI text: numbers 1..300 exactly once each, in order (no duplicate / lost text), FINISHED once', seqOk(un) && (b.t.match(/FINISHED/g) || []).length === 1, `count=${un.length} first-bad=${un.findIndex((v, i) => v !== i + 1)}`);
    const sc = am[0] ? txt(am[0].content) : '';
    chk(tag + 'UI text equals server text', JSON.stringify(nums(b.t)) === JSON.stringify(sn0), `ui=${nums(b.t).length} srv=${sn0.length}`);
    chk(tag + 'server: exactly one assistant message, status complete, same 1..300 sequence', am.length === 1 && am[0].status === 'complete' && seqOk(nums(typeof am[0].content === 'string' ? am[0].content : '')), `n=${am.length} status=${am[0] && am[0].status}`);
    chk(tag + 'no page errors', c.errs.filter(x => !/Load failed|net::ERR|Failed to load resource/i.test(x)).length === 0, c.errs.join(' | '));
  } catch (e) { chk(tag + 'crashed', false, String(e.stack || e).split('\n').slice(0, 3).join(' | ')); }
  finally { await c.ctx.close(); }
  if (invalid && attempt < 3) { info(tag + `sample discarded: the model did not write 1..300 (UI text == server text); rerun ${attempt + 1}/3`); return run(browser, engine, how, ms, B, attempt + 1); }
  for (const [n, ok, d] of R) chk0(n, ok, d);
}
(async () => {
  const w = await L.runningVersion(); console.log(`# s7 offline-resume  gateway=${w.version}  UI=${H.UI_DIR}`);
  const B = await H.session('qa-b@test.invalid', 'fe-B.json'); await H.setIntro(B, true);
  const wk = await H.webkit.launch(), cr = await H.chromium.launch();
  try {
    for (let i = 0; i < REPS; i++) for (const [eng, br] of [['webkit', wk], ['chromium', cr]]) for (const ms of [5000, 2000]) { console.log(`\n-- rep ${i + 1} ${eng} offline ${ms}ms`); await run(br, eng, 'offline', ms, B); }
    for (const [eng, br] of [['webkit', wk], ['chromium', cr]]) for (let i = 0; i < 2; i++) { console.log(`\n-- ${eng} hidden->visible ${i + 1}`); await run(br, eng, 'hidden', 4000, B); }
  } finally { await wk.close(); await cr.close(); }
  const t = H.totals(); console.log(`\n== s7 SUMMARY pass=${t.pass} fail=${t.fail}`); t.fails.forEach(f => console.log('  FAILED: ' + f)); process.exit(t.fail ? 1 : 0);
})();
