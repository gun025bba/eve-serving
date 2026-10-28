'use strict';
// QA test for eve-serving PR #99 (gateway self-description + memory_propose with web off), run on the QA instance ONLY (127.0.0.1:8002).
// Needs: QA gateway built from the PR head, its upstream pointed at capture-proxy.js (8090), two QA logins in allowed_users.
//   EVE_TARGET=qa EVE_BASE_URL=http://127.0.0.1:8002 node tests/e2e/pr99/run.js [--ttft N] [--skip-ttft]
// Part 1 (stub model, no MLX load): 3 memory states x 3 web modes + Bearer, checks the prompt the gateway SENT upstream.
// Part 2: memory_propose with web off: card, approve, next prompt has <user_memory>; refusals (web tool, memory off, pending, tainted history).
// Part 3 (real model, N short requests per arm): time to first token, memory off vs on, web off.
process.env.EVE_TARGET = process.env.EVE_TARGET || 'qa';
const http = require('http');
const L = require('../release/lib');
const { req } = L;
if (String(L.PORT) !== '8002') throw new Error('QA 8002 only');
const PROXY = 8090, EXPECT_VER = process.env.EVE_EXPECT_GW_VERSION || '5.0.2-qa.d045b10';
const NTTFT = +(process.argv.includes('--ttft') ? process.argv[process.argv.indexOf('--ttft') + 1] : 4);
const T = Date.now().toString(36), MARK = `CLIENT-SYS-${T}`, ITEM = `QA-PR99-${T} 좋아하는 색은 청록`;
let pass = 0, fail = 0; const fails = [], timing = {};
const chk = (name, ok, extra = '') => { ok ? pass++ : (fail++, fails.push(name)); console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  — ' + String(extra).slice(0, 220) : ''}`); return ok; };
const px = (method, p, body) => new Promise((res, rej) => { const d = body ? JSON.stringify(body) : undefined; const r = http.request({ host: '127.0.0.1', port: PROXY, method, path: p, headers: d ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(d) } : {} }, rs => { const c = []; rs.on('data', x => c.push(x)); rs.on('end', () => res(JSON.parse(Buffer.concat(c).toString() || 'null'))); }); r.on('error', rej); if (d) r.write(d); r.end(); });
const sysText = body => (body.messages || []).filter(m => m.role === 'system').map(m => typeof m.content === 'string' ? m.content : JSON.stringify(m.content)).join('\n---\n');
const toolNames = body => (body.tools || []).map(t => t.function && t.function.name);
async function session(login, file) { let st = L.stateRead(file); if (st && (await L.sessionValid(st.cookie)).status === 200) return st; st = { login, cookie: await L.pairQA(login) }; L.stateWrite(file, st); return st; }
const patch = (who, b) => req('PATCH', '/v1/settings', { cookie: who.cookie, body: b });
async function setMem(who, st) {
  if (st === 'on') { await patch(who, { memory: { enabled: true } }); await patch(who, { ui: { memory_intro_seen: true } }); }
  if (st === 'off') await patch(who, { memory: { enabled: false } });
  if (st === 'pending') { await patch(who, { memory: { enabled: true } }); await patch(who, { ui: { memory_intro_seen: false } }); }
  const s = await req('GET', '/v1/settings', { cookie: who.cookie }); return s.json;
}
async function chat(who, o = {}) {
  await px('POST', '/__reset');
  const body = { model: await L.model(), stream: o.stream !== false, max_tokens: o.max_tokens || 16, messages: o.messages || [{ role: 'system', content: MARK }, { role: 'user', content: o.text || 'ping' }] };
  if (o.web !== undefined) body.web_search = o.web;
  if (o.thread) body.thread_id = 'new'; else if (o.threadId) body.thread_id = o.threadId;
  const r = await req('POST', '/v1/chat/completions', { cookie: o.key ? undefined : who.cookie, key: o.key, csrf: true, body, timeout: 120000 });
  const cap = await px('GET', '/__captured');
  return { r, cap, events: L.parseSSE(r.text).map(e => e.json).filter(Boolean) };
}
const WEBS = [['off', 'off'], ['auto', 'auto'], ['default', undefined]];

(async () => {
  const w = await L.runningVersion(); console.log(`# PR99 QA test  target=${L.BASE}  version=${w.version}`);
  if (!chk(`QA runs the PR build ${EXPECT_VER}`, w.ok && w.version === EXPECT_VER, w.version || w.why)) process.exit(2);
  const mode0 = await px('POST', '/__mode', { mode: 'stub', tool: null });
  const A = await session(process.env.EVE_QA_LOGIN_A || 'qa-a@test.invalid', 'pr99-A.json'), B = await session(process.env.EVE_QA_LOGIN_B || 'qa-b@test.invalid', 'pr99-B.json');
  const orig = {}; for (const [n, u] of [['A', A], ['B', B]]) { const s = (await req('GET', '/v1/settings', { cookie: u.cookie })).json; orig[n] = { en: s.memory && s.memory.enabled, seen: s.ui && s.ui.memory_intro_seen }; }
  console.log('# original settings (restored at the end):', JSON.stringify(orig));
  const created = [];
  try {
    await setMem(A, 'on'); await req('DELETE', '/v1/memory', { cookie: A.cookie });
    const add = await req('POST', '/v1/memory/items', { cookie: A.cookie, body: { kind: 'fact', text: ITEM } }); chk('seed: A memory item added', add.status === 201, add.status);
    // ---------------- Part 1: state matrix ----------------
    console.log('\n== 1. self-description in 3 memory states x 3 web modes (prompt captured upstream) ==');
    let maxLen = 0;
    for (const ms of ['on', 'off', 'pending']) {
      const s = await setMem(A, ms);
      for (const [wname, wval] of WEBS) {
        const tag = `[mem ${ms} / web ${wname}]`; const { r, cap } = await chat(A, { web: wval });
        if (!chk(`${tag} request ok and reached the upstream`, r.status === 200 && cap.length >= 1, `${r.status} upstream_calls=${cap.length}`)) continue;
        const b = cap[0].body, sys = sysText(b), first = (b.messages[0] || {});
        const webOn = wname === 'auto'; 
        const lead = []; for (const m of b.messages) { if (m.role !== 'system') break; lead.push(m); }   // leading system run (web mode puts the tools prompt in front of it, as before the PR)
        const sd = lead.find(m => typeof m.content === 'string' && m.content.startsWith('[Eve 자기 설명'));
        chk(`${tag} self-description sits in the leading system run, directly followed by the client system text (${webOn ? 'after the tools prompt' : 'first message'})`, !!sd && sd.content.includes(MARK) && sd.content.indexOf('[Eve') < sd.content.indexOf(MARK) && (webOn ? lead.length === 2 && lead[0] !== sd : lead[0] === sd), `leading system msgs=${lead.length}`);
        chk(`${tag} shows the build version`, sys.includes(`버전: ${EXPECT_VER}`));
        const memOK = ms === 'on' ? sys.includes('켜짐. 선호·사실을 기억해') && sys.includes('확인 카드') : ms === 'pending' ? sys.includes('첫 사용 안내를 확인하기 전') && !sys.includes('확인 카드') : sys.includes('꺼짐. 아무것도 기억·저장하지 않아요') && !sys.includes('확인 카드');
        chk(`${tag} memory line matches the real state (${ms})`, memOK);
        chk(`${tag} web line matches the effective web mode`, sys.includes(webOn ? '웹 검색(켜짐)' : '웹 검색(꺼짐'), (sys.match(/도구: .*/) || [''])[0]);
        chk(`${tag} memory_propose is named/offered only when memory is on`, (ms === 'on') === sys.includes('memory_propose') && (ms === 'on') === toolNames(b).includes('memory_propose'), `tools=${toolNames(b)}`);
        chk(`${tag} web tools offered iff web is on`, webOn === toolNames(b).includes('web_search'), `tools=${toolNames(b)}`);
        if (sd) maxLen = Math.max(maxLen, sd.content.split(MARK)[0].length);
        const inj = sys.includes('<user_memory');
        chk(`${tag} <user_memory> block only when memory is on and the intro is seen, and it is last`, inj === (ms === 'on') && (!inj || sys.indexOf('<user_memory') > sys.indexOf(MARK)));
        const wantStream = !webOn;   // d045b10: the web-off memory loop streams; with web on the first round is still the buffered tool round
        chk(`${tag} upstream first round ${wantStream ? 'is streamed' : 'is the buffered (non-streaming) web tool round'}`, wantStream ? b.stream === true : !b.stream, `upstream stream=${b.stream}`);
        const rec = (await req('GET', '/v1/settings', { cookie: A.cookie })).json; void rec; void s;
      }
    }
    chk(`self-description length is small (<= 700 chars before client text; PR budget "<200 tokens")`, maxLen <= 700, `max ${maxLen} chars`);
    // user B has its own state; and a Bearer API key (memory unavailable)
    await setMem(A, 'on'); const bs = await setMem(B, 'pending');
    const rb = await chat(B, { web: 'off' }), ra = await chat(A, { web: 'off' });
    chk('B (pending) gets B\'s own self-description and no memory of A', sysText(rb.cap[0].body).includes('첫 사용 안내를 확인하기 전') && !sysText(rb.cap[0].body).includes(ITEM) && !toolNames(rb.cap[0].body).includes('memory_propose'));
    chk('A (on) in the same minute still gets its own state and its item', sysText(ra.cap[0].body).includes('확인 카드') && sysText(ra.cap[0].body).includes(ITEM));
    const rk = await chat(null, { key: true, web: 'off' }); const rkb = rk.cap[0] && rk.cap[0].body;
    chk('Bearer key: NO self-description, request forwarded unchanged (same messages), no <user_memory>, no memory_propose', rk.r.status === 200 && !!rkb && !JSON.stringify(rkb).includes('Eve 자기 설명') && rkb.messages.length === 2 && rkb.messages[0].content === MARK && !JSON.stringify(rkb).includes('<user_memory') && !toolNames(rkb).includes('memory_propose'), rk.r.status + (rkb ? ` msgs=${rkb.messages.length}` : ''));
    const rka = await chat(null, { key: true, web: 'auto' });
    chk('Bearer key with web auto: still no self-description', rka.r.status === 200 && !JSON.stringify(rka.cap[0].body).includes('Eve 자기 설명'), rka.r.status);
    const rs = await chat(A, { web: 'off' });
    chk('Session request right after: self-description present', rs.r.status === 200 && sysText(rs.cap[0].body).includes('[Eve 자기 설명'));
    const sysOnly = await chat(A, { web: 'off', messages: [{ role: 'user', content: 'ping' }] });
    chk('request without a system message gets a new leading system message', sysOnly.cap[0].body.messages[0].role === 'system' && sysOnly.cap[0].body.messages[0].content.startsWith('[Eve'));
    const arr = await chat(A, { web: 'off', messages: [{ role: 'system', content: [{ type: 'text', text: MARK }] }, { role: 'user', content: 'ping' }] });
    chk('system message with array content is left intact; self-description is a separate leading message', arr.r.status === 200 && arr.cap[0].body.messages[0].content.startsWith('[Eve') && JSON.stringify(arr.cap[0].body.messages[1].content).includes(MARK), arr.r.status);
    // not persisted in the thread
    const pt = await chat(A, { web: 'off', thread: true, text: 'persist check' }); const tid = (pt.events.find(e => e.thread_id) || {}).thread_id;
    if (tid) { const ms = await req('GET', `/v1/threads/${tid}/messages`, { cookie: A.cookie }); chk('self-description is not stored in the thread', ms.status === 200 && !ms.text.includes('Eve 자기 설명') && !ms.text.includes(EXPECT_VER), ms.status); created.push(tid); } else chk('thread created for the persistence check', false, pt.r.status);

    // ---------------- Part 2: memory_propose with web off ----------------
    console.log('\n== 2. memory_propose with web off ==');
    await setMem(A, 'on'); await req('DELETE', '/v1/memory', { cookie: A.cookie });
    const text = `QA-PR99-${T} 청록색 노트를 좋아함`;
    await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'add', text, kind: 'preference' } });
    const p = await chat(A, { web: 'off', thread: true, text: '이거 기억해줘' });
    const card = p.events.find(e => e.object === 'chat.completion.confirmation'); if (p.events.find(e => e.thread_id)) created.push(p.events.find(e => e.thread_id).thread_id);
    chk('web off: confirmation card event is emitted', !!card && card.status !== 'denied', card ? JSON.stringify(Object.keys(card)) + ' status=' + card.status : p.r.text.slice(0, 120));
    chk('web off: upstream round 1 carried memory_propose and no web tools', toolNames(p.cap[0].body).includes('memory_propose') && !toolNames(p.cap[0].body).includes('web_search'), `tools=${toolNames(p.cap[0].body)}`);
    chk('web off: round 2 got the tool result and the stream finished (STUB-OK), no sources event', p.cap.length >= 2 && p.r.text.includes('STUB-OK') && !p.r.text.includes('chat.completion.sources'), `upstream_calls=${p.cap.length}`);
    const aid = card && card.action_id, ph = card && card.payload_hash; let ct;
    chk('card holds action_id, payload_hash, scopes=memory.write, status=pending, not tainted', !!(aid && ph) && card.status === 'pending' && card.tainted === false, card ? `tool=${card.tool} scopes=${JSON.stringify(card.scopes)} tainted=${card.tainted}` : '');
    if (aid) { const ri = await req('POST', `/v1/actions/${aid}/reissue`, { cookie: A.cookie, csrf: true }); ct = ri.json && ri.json.confirm_token; chk('reissue returns a confirm token for the card', ri.status === 200 && !!ct, ri.status); }
    const before = await req('GET', '/v1/memory', { cookie: A.cookie });
    chk('nothing is stored before approval', before.status === 200 && !JSON.stringify(before.json).includes(text));
    if (aid && ct && ph) {
      const ap = await req('POST', `/v1/actions/${aid}/approve`, { cookie: A.cookie, csrf: true, body: { confirm_token: ct, payload_hash: ph } });
      chk('ADD card: approve -> 200', ap.status === 200, ap.status + ' ' + ap.text.slice(0, 100));
      const after = await req('GET', '/v1/memory', { cookie: A.cookie });
      const stored = after.status === 200 && JSON.stringify(after.json).includes(text);
      chk('ADD card: approved fact is now in A\'s memory', stored);
      if (ap.status !== 200) { const pe = L.pgEnv(); if (pe) { const q = require('child_process').spawnSync('psql', ['-X', '-Atc', `select payload::text, payload_hash from pending_actions where id='${aid}'`], { env: pe, encoding: 'utf8' }); console.log('    diag (QA db, pending_actions.payload as read back by Postgres JSONB):', (q.stdout || '').trim().slice(0, 200)); } }
      if (stored) {
        await px('POST', '/__mode', { mode: 'stub', tool: null });
        const nx = await chat(A, { web: 'off', text: '내가 뭘 좋아하지?' }); const s2 = sysText(nx.cap[0].body), i = s2.indexOf('<user_memory');
        chk('ADD: next request (web off) prompt includes <user_memory> with the approved fact, after the client system text', i > s2.indexOf(MARK) && s2.includes(text), `block at ${i}`);
      }
    }
    // FORGET card (payload hash is stable under Postgres JSONB because it has one key per item): full approve -> next-prompt path with web off
    {
      const ITEM2 = `QA-PR99-${T} 보라색 우산을 가지고 다님`;
      await req('DELETE', '/v1/memory', { cookie: A.cookie }); const ad = await req('POST', '/v1/memory/items', { cookie: A.cookie, body: { kind: 'fact', text: ITEM2 } });
      await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'forget', query: '보라색 우산' } });
      const f = await chat(A, { web: 'off', text: '보라색 우산 얘기는 잊어줘' }); const fc = f.events.find(e => e.object === 'chat.completion.confirmation');
      chk('FORGET (web off): prompt carried <user_memory> with the item, card emitted', ad.status === 201 && sysText(f.cap[0].body).includes('<user_memory') && sysText(f.cap[0].body).includes(ITEM2) && !!fc && fc.status === 'pending', fc && fc.status);
      if (fc) {
        const ri = await req('POST', `/v1/actions/${fc.action_id}/reissue`, { cookie: A.cookie, csrf: true });
        const ap = await req('POST', `/v1/actions/${fc.action_id}/approve`, { cookie: A.cookie, csrf: true, body: { confirm_token: ri.json && ri.json.confirm_token, payload_hash: fc.payload_hash } });
        chk('FORGET card: approve -> 200', ap.status === 200, ap.status + ' ' + ap.text.slice(0, 100));
        const mm = await req('GET', '/v1/memory', { cookie: A.cookie }); chk('FORGET card: item gone from memory', !JSON.stringify(mm.json).includes(ITEM2));
        await px('POST', '/__mode', { mode: 'stub', tool: null });
        const nx2 = await chat(A, { web: 'off', text: 'hello' });
        chk('FORGET: next request prompt no longer contains the item', !sysText(nx2.cap[0].body).includes(ITEM2));
        const rp = await req('POST', `/v1/actions/${fc.action_id}/approve`, { cookie: A.cookie, csrf: true, body: { confirm_token: ri.json && ri.json.confirm_token, payload_hash: fc.payload_hash } });
        chk('replaying an approved card is refused (409)', rp.status === 409, rp.status);
        const bapp = await req('POST', `/v1/actions/${fc.action_id}/approve`, { cookie: B.cookie, csrf: true, body: { confirm_token: ri.json && ri.json.confirm_token, payload_hash: fc.payload_hash } });
        chk('another user cannot approve it (404)', bapp.status === 404, bapp.status);
      }
      const bk = await req('POST', '/v1/actions/x/approve', { key: true, body: { confirm_token: 'x', payload_hash: 'x' } });
      chk('Bearer key cannot approve (403)', bk.status === 403, bk.status);
      // legacy-hash card: a forget card created by an older build carries sha256(compact payload in stored key order). Simulated by rewriting the stored hash of a fresh card (QA DB, one row).
      const pe = L.pgEnv(); if (pe) {
        const ITEM3 = `QA-PR99-${T} 노란 모자`; await req('POST', '/v1/memory/items', { cookie: A.cookie, body: { kind: 'fact', text: ITEM3 } });
        await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'forget', query: '노란 모자' } });
        const lg = await chat(A, { web: 'off', text: '노란 모자 잊어줘' }); const lc = lg.events.find(e => e.object === 'chat.completion.confirmation');
        const sql = q => require('child_process').spawnSync('psql', ['-X', '-Atc', q], { env: pe, encoding: 'utf8' });
        if (lc) {
          const pl = sql(`select payload::text from pending_actions where id='${lc.action_id}'`).stdout.trim(); const legacy = 'sha256:' + require('crypto').createHash('sha256').update(JSON.stringify(JSON.parse(pl))).digest('hex');
          chk('legacy: canonical hash differs from the legacy hash for this payload (so the test is meaningful)', legacy !== lc.payload_hash);
          const up = sql(`update pending_actions set payload_hash='${legacy}' where id='${lc.action_id}'`);
          const ri = await req('POST', `/v1/actions/${lc.action_id}/reissue`, { cookie: A.cookie, csrf: true });
          const ap = await req('POST', `/v1/actions/${lc.action_id}/approve`, { cookie: A.cookie, csrf: true, body: { confirm_token: ri.json && ri.json.confirm_token, payload_hash: ri.json && ri.json.payload_hash } });
          chk('legacy: a card whose stored hash is the pre-5.0.2 hash (stored key order still matching) is approved -> 200', up.status === 0 && ri.json && ri.json.payload_hash === legacy && ap.status === 200, `${ap.status} ${ap.text.slice(0, 80)}`);
          const bad = await req('POST', `/v1/actions/${lc.action_id}/approve`, { cookie: A.cookie, csrf: true, body: { confirm_token: 'x', payload_hash: legacy } }); void bad;
        } else chk('legacy: forget card created', false);
        await px('POST', '/__mode', { mode: 'stub', tool: null });
      }
    }
    // web tool call with web off: refused, never executed
    await px('POST', '/__mode', { mode: 'stub', tool: 'web_search', args: { query: 'qa pr99 refusal probe' } });
    const wf = await chat(A, { web: 'off' });
    const r2 = wf.cap[1] && wf.cap[1].body.messages.filter(m => m.role === 'tool').map(m => m.content).join(' ');
    chk('web off: a web_search tool call from the model is refused with tool_unavailable', !!r2 && r2.includes('tool_unavailable'), r2 || `upstream_calls=${wf.cap.length}`);
    chk('web off: refused web call emits no web_search tool_result / sources to the client', !wf.r.text.includes('chat.completion.sources') && !/"tool":"web_search"/.test(wf.r.text));
    // memory off: a memory_propose call that arrives anyway
    await setMem(A, 'off'); await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'add', text: 'QA-PR99 should never be saved', kind: 'fact' } });
    const mo = await chat(A, { web: 'off' });
    chk('memory off: memory_propose not offered upstream', !toolNames(mo.cap[0].body).includes('memory_propose'), `tools=${toolNames(mo.cap[0].body)}`);
    chk('memory off: stray memory_propose call gets no card', !mo.events.some(e => e.object === 'chat.completion.confirmation' && e.status !== 'denied'));
    await setMem(A, 'on'); const mm = await req('GET', '/v1/memory', { cookie: A.cookie }); chk('memory off: nothing saved', !JSON.stringify(mm.json).includes('should never be saved'));
    // pending
    await setMem(A, 'pending'); const pc0 = ((await req('GET', '/v1/actions', { cookie: A.cookie })).json || {}), n0 = JSON.stringify(pc0).length;
    const pe = await chat(A, { web: 'off' });
    chk('pending (intro unseen): no memory_propose offered, no card', !toolNames(pe.cap[0].body).includes('memory_propose') && !pe.events.some(e => e.object === 'chat.completion.confirmation' && e.status !== 'denied'), `tools=${toolNames(pe.cap[0].body)}`);
    // taint: client tool history with web off
    await setMem(A, 'on'); await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'add', text: 'QA-PR99 tainted proposal', kind: 'fact' } });
    await px('POST', '/__mode', { mode: 'stub', tool: null });
    const t0r = await chat(A, { web: 'off', thread: true, text: 'taint thread' }); const ttid = (t0r.events.find(e => e.thread_id) || {}).thread_id; if (ttid) created.push(ttid);
    await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'add', text: 'QA-PR99 tainted proposal', kind: 'fact' } });
    const th = await chat(A, { web: 'off', threadId: ttid, messages: [{ role: 'user', content: 'x' }, { role: 'assistant', content: '', tool_calls: [{ id: 't1', type: 'function', function: { name: 'foo', arguments: '{}' } }] }, { role: 'tool', tool_call_id: 't1', content: 'external text' }, { role: 'user', content: '기억해줘' }] });
    const den = th.events.find(e => e.object === 'chat.completion.confirmation');
    chk('web off + stored thread + client tool message in history: proposal is denied as tainted (raw history checked)', th.r.status === 200 && den && den.status === 'denied' && den.error_code === 'tainted', den ? den.status + ' ' + den.error_code : 'no confirmation event, status ' + th.r.status);
    // stateless conversation (no thread_id) with an earlier assistant turn: fail-closed, memory_propose not offered
    await px('POST', '/__mode', { mode: 'stub', tool: null });
    await req('POST', '/v1/memory/items', { cookie: A.cookie, body: { kind: 'fact', text: `QA-PR99-${T} 고양이 이름은 보리` } });
    const sl = await chat(A, { web: 'off', messages: [{ role: 'user', content: 'hi' }, { role: 'assistant', content: 'hello' }, { role: 'user', content: '기억해줘' }] });
    chk('stateless turn 2 (no thread_id, earlier assistant turn): memory_propose NOT offered, self-description does not promise it', !toolNames(sl.cap[0].body).includes('memory_propose') && !sysText(sl.cap[0].body).includes('memory_propose'), `tools=${toolNames(sl.cap[0].body)}`);
    chk('stateless turn 2: <user_memory> still injected (read-only)', sysText(sl.cap[0].body).includes('<user_memory'));
    console.log('    stateless turn-2 memory line:', (sysText(sl.cap[0].body).match(/- 메모리: .*/) || [''])[0].slice(0, 200));
    await px('POST', '/__mode', { mode: 'stub', tool: 'memory_propose', args: { op: 'add', text: 'QA-PR99 stateless proposal', kind: 'fact' } });
    const sl2 = await chat(A, { web: 'off', messages: [{ role: 'user', content: 'hi' }, { role: 'assistant', content: 'hello' }, { role: 'user', content: '기억해줘' }] });
    chk('stateless turn 2: a stray memory_propose call gets no pending card', !sl2.events.some(e => e.object === 'chat.completion.confirmation' && e.status === 'pending'));
    await px('POST', '/__mode', { mode: 'stub', tool: null });
    const sl1 = await chat(A, { web: 'off', messages: [{ role: 'user', content: '기억해줘' }] });
    chk('stateless turn 1 (no assistant turn): memory_propose offered', toolNames(sl1.cap[0].body).includes('memory_propose'));
    // UI-style conversation with a stored thread: thread_id + full history each turn; memory_propose offered on turns 2, 3, 4
    const hist = []; let utid = null; const tl = [];
    for (let k = 1; k <= 4; k++) {
      hist.push({ role: 'user', content: `turn ${k}` });
      const rr = await chat(A, { web: 'off', stream: true, messages: hist.slice(), thread: !utid, threadId: utid });
      utid = utid || (rr.events.find(e => e.thread_id) || {}).thread_id; if (utid && !created.includes(utid)) created.push(utid);
      const asst = rr.events.map(e => e.choices && e.choices[0] && e.choices[0].delta && e.choices[0].delta.content).filter(Boolean).join(''); hist.push({ role: 'assistant', content: asst || 'STUB-OK' });
      const bb = rr.cap[0].body; tl.push(`t${k}:${toolNames(bb).includes('memory_propose') ? 'tool' : 'NO-tool'}/${sysText(bb).includes('memory_propose') ? 'selfdesc' : 'NO-selfdesc'}/assistantTurnsSent=${hist.filter(m => m.role === 'assistant').length - (k > 0 ? 1 : 0)}`);
      chk(`UI thread conversation turn ${k}: memory_propose offered and self-description says so`, rr.r.status === 200 && toolNames(bb).includes('memory_propose') && sysText(bb).includes('memory_propose'), tl[tl.length - 1]);
    }
    void n0;
    await px('POST', '/__mode', { mode: 'stub', tool: null });
  } finally {
    // restore data + settings
    await req('DELETE', '/v1/memory', { cookie: A.cookie }).catch(() => {});
    for (const t of created) await req('DELETE', `/v1/threads/${t}`, { cookie: A.cookie, csrf: true }).catch(() => {});
    for (const [n, u] of [['A', A], ['B', B]]) { await patch(u, { memory: { enabled: orig[n].en !== false } }); await patch(u, { ui: { memory_intro_seen: !!orig[n].seen } }); }
  }
  // ---------------- Part 3: first-token latency ----------------
  if (!process.argv.includes('--skip-ttft')) {
    console.log(`\n== 3. time to first token, web off, real model (${NTTFT} requests per arm, max_tokens ${process.env.EVE_TTFT_MAXTOK || 64}) ==`);
    await px('POST', '/__mode', { mode: 'pass' });
    const arms = { 'memory off (baseline)': [], 'memory on': [] };
    const one = async (who) => {
      const body = { model: await L.model(), stream: true, max_tokens: +(process.env.EVE_TTFT_MAXTOK || 64), web_search: 'off', messages: [{ role: 'user', content: process.env.EVE_TTFT_PROMPT || '하늘은 왜 파란지 한 문장으로 답해줘. /no_think' }] };
      const t0 = Date.now(); let tb = 0, tc = 0, tl = 0, nd = 0, buf = '', pos = 0;
      const r = await req('POST', '/v1/chat/completions', { cookie: who.cookie, csrf: true, body, timeout: 180000, stream: d => { const now = Date.now() - t0; if (!tb) tb = now; buf += d.toString(); let nl; while ((nl = buf.indexOf('\n', pos)) >= 0) { const line = buf.slice(pos, nl); pos = nl + 1; if (!line.startsWith('data:') || line.includes('[DONE]')) continue; try { const j = JSON.parse(line.slice(5)); const dl = (j.choices && j.choices[0] && j.choices[0].delta) || {}; if (dl.content || dl.reasoning || dl.reasoning_content) { nd++; if (!tc) tc = now; tl = now; } } catch {} } } });
      return { status: r.status, firstByte: tb, firstToken: tc, lastToken: tl, deltas: nd, total: Date.now() - t0 };
    };
    const arm = ['memory off (baseline)', 'memory on'];
    await setMem(A, 'on'); await req('POST', '/v1/memory/items', { cookie: A.cookie, body: { kind: 'fact', text: ITEM } });
    const ordered = []; for (let i = 0; i < NTTFT; i++) ordered.push(...(i % 2 ? [1, 0] : [0, 1]));
    await one(A); // warm-up (not counted)
    for (const k of ordered) { await setMem(A, k ? 'on' : 'off'); arms[arm[k]].push(await one(A)); }
    await req('DELETE', '/v1/memory', { cookie: A.cookie }); await patch(A, { memory: { enabled: orig.A.en !== false } }); await patch(A, { ui: { memory_intro_seen: !!orig.A.seen } });
    const med = a => { const s = [...a].sort((x, y) => x - y); return s.length ? s[Math.floor(s.length / 2)] : null; };
    for (const [k, v] of Object.entries(arms)) { timing[k] = { n: v.length, ok: v.filter(x => x.status === 200).length, firstByte_ms: v.map(x => x.firstByte), firstToken_ms: v.map(x => x.firstToken), content_deltas: v.map(x => x.deltas), first_to_last_token_ms: v.map(x => x.lastToken - x.firstToken), total_ms: v.map(x => x.total), median_firstToken_ms: med(v.map(x => x.firstToken)), median_total_ms: med(v.map(x => x.total)) }; console.log('  ', k, JSON.stringify(timing[k])); }
    await px('POST', '/__mode', { mode: 'stub', tool: null });
  }
  console.log(`\nRESULT: ${pass} pass, ${fail} fail`); if (fail) console.log('FAILED:\n - ' + fails.join('\n - '));
  console.log('TIMING_JSON ' + JSON.stringify(timing));
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error('CRASH', e); process.exit(3); });
