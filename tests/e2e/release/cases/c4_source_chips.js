'use strict';
// Case 4: a web-search answer carries sources in the payload (SSE `chat.completion.sources`, persisted `message.sources`) and the UI (1.9.0) renders
// the chips (.sources .sc, +N chip .sc-more, expandable .src-list with .src-item a) with valid links.
// Needs: web search enabled on the instance (SearXNG) and a QA key (EVE_TEST_API_KEY). One short forced-search request (light MLX load).
const { uiBase, openPage } = require('../ui');
const EXPECT_V = process.env.EVE_EXPECT_UI_VERSION || '1.9.0';
const PRIVATE = /^(localhost|.*\.local|.*\.internal|.*\.lan|127\..*|10\..*|192\.168\..*|172\.(1[6-9]|2\d|3[01])\..*|169\.254\..*|0\.0\.0\.0|\[?::1\]?|.*\.ts\.net|.*\.test\.invalid)$/i;
const dom = u => { try { return new URL(u).hostname.replace(/^www\./, ''); } catch { return ''; } };

module.exports = {
  id: 4, name: 'web-search source chips: payload + UI + valid links', readonly: false, needsDevice: true,
  async run(c) {
    const L = c.lib, { req } = L;
    if (!L.apiKey()) { c.skip('whole case', 'no QA API key (EVE_TEST_API_KEY[_FILE])'); return; }
    const q = process.env.EVE_CASE4_QUERY || 'What is the tallest mountain in South Korea? One sentence, cite sources as [1]. /no_think';
    const body = { model: await L.model(), stream: true, max_tokens: 200, web_search: 'force', thread_id: 'new', messages: [{ role: 'user', content: q }] };
    const r = await req('POST', '/v1/chat/completions', { key: true, csrf: true, body, timeout: 240000 });
    if (r.status === 400 && /web_search_disabled/.test(r.text)) { c.skip('whole case', 'web search is disabled on this instance (web_search_disabled)'); return; }
    if (!c.check('forced web-search chat request -> 200 SSE', r.status === 200, `${r.status} ${r.text.slice(0, 150)}`)) return;
    const ev = L.parseSSE(r.text);
    const srcEv = ev.find(e => e.json && e.json.object === 'chat.completion.sources');
    const answer = ev.map(e => e.json && e.json.choices && e.json.choices[0] && e.json.choices[0].delta && e.json.choices[0].delta.content || '').join('');
    const meta = ev.map(e => e.json).find(j => j && (j.thread_id || (j.eve && j.eve.thread_id))) || {};
    const tid = meta.thread_id || (meta.eve && meta.eve.thread_id);
    const tr = ev.map(e => e.json).filter(j => j && j.object === 'chat.completion.tool_result');
    c.check('stream has a web_search tool_result with status ok', tr.some(t => t.status === 'ok'), JSON.stringify(tr.map(t => t.status)));
    if (!c.check('stream has a chat.completion.sources event with >=1 source (payload chips)', srcEv && srcEv.json.sources && srcEv.json.sources.length >= 1, srcEv ? 'empty sources' : 'no sources event (search backend down? answered without citations)')) return;
    const srcs = srcEv.json.sources, cited = srcEv.json.cited || [];
    c.info('payload', `${srcs.length} sources, cited=${JSON.stringify(cited)}, domains=${[...new Set(srcs.map(s => dom(s.url)))].join(', ')}`);
    // link validity
    const ns = srcs.map(s => s.n);
    c.check('source numbers are unique positive integers', new Set(ns).size === ns.length && ns.every(n => Number.isInteger(n) && n > 0), ns.join(','));
    for (const s of srcs) {
      let u; try { u = new URL(s.url); } catch { c.fail(`source [${s.n}] url parses`, s.url); continue; }
      const ok = (u.protocol === 'https:' || u.protocol === 'http:') && !u.username && !u.password && u.hostname.includes('.') && !PRIVATE.test(u.hostname);
      c.check(`source [${s.n}] link is a public http(s) URL without credentials`, ok, s.url);
      if (!s.title || !String(s.title).trim()) c.warn(`source [${s.n}] has no title`);
      if (s.domain && s.domain.replace(/^www\./, '') !== u.hostname.replace(/^www\./, '')) c.warn(`source [${s.n}] domain field != URL host`, `${s.domain} vs ${u.hostname}`);
    }
    c.check('cited numbers are a subset of the sources', cited.every(n => ns.includes(n)), JSON.stringify(cited));
    const inline = [...answer.matchAll(/\[(\d{1,2})\]/g)].map(m => +m[1]);
    if (inline.length) c.check('inline [n] in the answer all refer to a listed source (no dangling citation)', inline.every(n => ns.includes(n)), inline.join(','));
    else c.warn('the model did not cite [n] inline in this answer (model-dependent)', answer.slice(0, 100));
    if (process.env.EVE_CHECK_LINK_REACHABLE === '1') {
      for (const s of srcs.slice(0, 5)) { try { const f = await fetch(s.url, { method: 'GET', redirect: 'follow', signal: AbortSignal.timeout(10000), headers: { 'user-agent': 'Mozilla/5.0 eve-qa' } }); (f.status < 400) ? c.pass(`reachable [${s.n}] ${dom(s.url)}`, f.status) : c.warn(`[${s.n}] ${dom(s.url)} answered ${f.status} (bot-blocking is common)`); } catch (e) { c.warn(`[${s.n}] ${dom(s.url)} not reachable from the box`, e.message); } }
    } else c.info('link reachability not fetched', 'set EVE_CHECK_LINK_REACHABLE=1 to GET each URL from the box (outbound traffic)');
    // persisted payload
    let thread = null, msgs = null;
    if (tid) {
      await new Promise(r => setTimeout(r, 1500));
      const m = await req('GET', `/v1/threads/${tid}/messages`, { key: true }); msgs = m.json && (m.json.data || m.json.messages || m.json);
      const asst = Array.isArray(msgs) ? [...msgs].reverse().find(x => x.role === 'assistant') : null;
      c.check('persisted assistant message has the same sources (reload/replay shows chips)', asst && Array.isArray(asst.sources) && asst.sources.length >= 1 && asst.sources.every(s => srcs.some(x => x.url === s.url)), asst ? JSON.stringify((asst.sources || []).length) + ' sources' : 'no assistant message');
      thread = (await req('GET', `/v1/threads/${tid}`, { key: true })).json;
    } else c.warn('no thread id in the stream: persisted-sources check skipped (is persistence on?)');
    // UI
    const ui = await uiBase();
    try {
      let s;
      try { s = await openPage({ engine: 'webkit', standalone: true, base: ui.base, mock: { threads: thread ? [thread] : [], messages: tid && msgs ? { [tid]: msgs } : {} }, url: '/ui/#/' }); }
      catch (e) { c.fail('open UI', String(e.message).split('\n')[0]); return; }
      try {
        const v = await s.page.evaluate('__app.version');
        c.check(`UI version is ${EXPECT_V}`, v === EXPECT_V, v);
        // open the thread from the list (deep link #/c/<id> only resolves after the cached list is known)
        if (tid) { await s.page.waitForSelector(`#convList li.row[data-id="${tid}"]`, { timeout: 8000 }).then(() => s.page.click(`#convList li.row[data-id="${tid}"]`)).catch(() => {}); }
        await s.page.waitForSelector('#messages .msg.assistant .sources', { timeout: 8000 }).catch(() => {});
        const ch = await s.page.evaluate(() => { const all = [...document.querySelectorAll('#messages .msg.assistant .sources')]; const sw = all[all.length - 1]; if (!sw) return null;
          return { chips: [...sw.querySelectorAll('.sc:not(.sc-more)')].map(x => x.textContent.trim()), more: [...sw.querySelectorAll('.sc-more')].map(x => x.textContent.trim()), listBefore: !!sw.querySelector('.src-list') }; });
        if (!c.check('UI renders a .sources chip row under the assistant answer', ch, 'no .sources element (UI older than 1.9.0 or payload not replayed)')) return;
        const domsInOrder = [...new Set(srcs.map(x => dom(x.url)))];
        c.check(`UI chip count = min(2, distinct domains=${domsInOrder.length})`, ch.chips.length === Math.min(2, domsInOrder.length), JSON.stringify(ch));
        c.check('chip labels are payload domains in first-appearance order', ch.chips.every((t, i) => t.replace(/\s+/g, ' ').includes(domsInOrder[i])), `${JSON.stringify(ch.chips)} vs ${domsInOrder.slice(0, 2)}`);
        const restN = srcs.filter(x => !domsInOrder.slice(0, 2).includes(dom(x.url))).length;   // +N counts sources whose domain has no chip
        if (restN > 0) c.check(`+N chip shows ${restN} (sources whose domain has no chip)`, ch.more.length === 1 && ch.more[0].includes(String(restN)), JSON.stringify(ch.more));
        else c.check('no +N chip when every source domain has a chip', ch.more.length === 0, JSON.stringify(ch.more));
        await s.page.click('#messages .msg.assistant .sources .sc'); await s.page.waitForTimeout(400);
        const list = await s.page.evaluate(() => [...document.querySelectorAll('#messages .msg.assistant .src-list .src-item a')].map(a => ({ href: a.href, target: a.target, rel: a.rel })));
        c.check('tapping a chip expands the source list', list.length >= 1, `items=${list.length}`);
        const want = new Set(srcs.map(x => new URL(x.url).href));
        c.check('every listed link href equals a payload URL', list.every(a => want.has(a.href)), JSON.stringify(list.map(a => a.href)));
        c.check('links open in a new tab with rel noopener', list.every(a => a.target === '_blank' && /noopener/.test(a.rel)), JSON.stringify(list.slice(0, 1)));
        c.check('no javascript:/data: link in the answer or the source list', await s.page.evaluate(() => ![...document.querySelectorAll('#messages a[href]')].some(a => /^(javascript|data|vbscript):/i.test(a.getAttribute('href')))));
        const inAns = await s.page.evaluate(() => [...document.querySelectorAll('#messages .msg.assistant .md a[href]')].map(a => a.href));
        if (inAns.length) c.check('links inside the answer text point only at payload URLs', inAns.every(h => want.has(h)), inAns.join(' '));
        c.check('no page errors', s.errs.length === 0, s.errs.join('; '));
      } finally { await s.close(); }
    } finally { ui.close(); }
    if (tid) await req('DELETE', `/v1/threads/${tid}`, { key: true, csrf: true }).catch(() => {});   // QA thread of the API-key user
    c.device('iPhone: ask a web question in the PWA', 'chips appear under the answer, +N chip expands the list, a link opens in Safari; long domain names do not overflow the row.');
  },
};
