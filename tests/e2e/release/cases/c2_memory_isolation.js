'use strict';
// Case 2: memory / threads / search are separated per user (5.0.x: memory default on; Bearer API keys are memory-off, 403 scope_denied on /v1/memory*).
// Needs two paired QA browser sessions for two QA-only logins (both must be in the QA instance's allowed_users). Never uses real people.
// Data written: 1 memory item + 1 imported thread per QA login, all removed at the end (own data only).
const { goTests } = require('../gotest');
const LA = process.env.EVE_QA_LOGIN_A || 'qa-a@test.invalid', LB = process.env.EVE_QA_LOGIN_B || 'qa-b@test.invalid';
const T = Date.now().toString(36);
const CA = `qaiso${T}alpha`, CB = `qaiso${T}bravo`;       // canary tokens: single words so search/recall can find them

async function session(L, login, file) {
  let st = L.stateRead(file);
  if (st) { const v = await L.sessionValid(st.cookie); if (v.status === 200 && v.json.provider === 'session' && v.json.user === (v.json.user)) return st; }
  const cookie = await L.pairQA(login); st = { login, cookie, at: new Date().toISOString() }; L.stateWrite(file, st); return st;
}
module.exports = {
  id: 2, name: 'memory/threads/search isolated per user (QA identities only)', readonly: false,
  async run(c) {
    const L = c.lib, { req } = L;
    goTests(c, { ref: process.env.EVE_REF_501 || 'origin/release/5.0.1', run: 'PerUser|Isolation|TestMemoryAPI|TestV5|CrossUser|OtherUser', pkgs: ['./internal/handlers/', './internal/memory/...', './internal/store/...'], label: 'in-process isolation tests on the release ref (always runnable, no server needed)' });
    if (!L.cliSupportsPair() && !process.env.EVE_PAIR_CMD) { c.skip('live per-user checks', `${L.GW_CLI} predates pairing (cannot mint two browser sessions); point EVE_GW_CLI at a 4.0.0+ build for the QA instance`); return; }
    let A, B;
    try { A = await session(L, LA, 'pairing-A.json'); B = await session(L, LB, 'pairing-B.json'); }
    catch (e) { c.skip('live per-user checks', `cannot pair two QA logins (${LA}, ${LB}): ${e.message}. Add both to allowed_users of the QA instance (see doc, "QA setup").`); return; }
    const wa = await req('GET', '/whoami', { cookie: A.cookie }), wb = await req('GET', '/whoami', { cookie: B.cookie });
    if (!c.check('two distinct QA users authenticated by session', wa.json && wb.json && wa.json.user && wa.json.user !== wb.json.user, `${wa.status}/${wb.status}`)) return;
    const mem = async (who, m, p, body) => req(m, p, { cookie: who.cookie, body });
    let idA, idB, tidA, cleanup = [];
    try {
      for (const [n, who] of [['A', A], ['B', B]]) {
        const s = await mem(who, 'GET', '/v1/settings');
        if (s.status !== 200) { c.fail(`user ${n}: GET /v1/settings`, s.status); return; }
        if (s.json && s.json.system && s.json.system.memory_available === false) { c.skip('memory not available on this server', 'GW memory.enabled is off here'); return; }
        const p = await mem(who, 'PATCH', '/v1/settings', { memory: { enabled: true }, ui: { memory_intro_seen: true } });
        c.check(`user ${n}: memory switched on for the QA user (PATCH /v1/settings)`, p.status === 200, p.status + ' ' + p.text.slice(0, 120));
      }
      const addA = await mem(A, 'POST', '/v1/memory/items', { kind: 'fact', text: `QA release canary ${CA}` });
      const addB = await mem(B, 'POST', '/v1/memory/items', { kind: 'fact', text: `QA release canary ${CB}` });
      if (!c.check('A and B can each add their own memory item (201)', addA.status === 201 && addB.status === 201, `${addA.status}/${addB.status} ${addA.text.slice(0, 100)}`)) return;
      idA = addA.json.id; idB = addB.json.id;
      const ga = await mem(A, 'GET', '/v1/memory'), gb = await mem(B, 'GET', '/v1/memory');
      c.check('A sees own canary and not B\'s', ga.text.includes(CA) && !ga.text.includes(CB));
      c.check('B sees own canary and not A\'s', gb.text.includes(CB) && !gb.text.includes(CA));
      const ea = await mem(A, 'GET', '/v1/memory/export'), eb = await mem(B, 'GET', '/v1/memory/export');
      c.check('export: A has only A, B has only B', ea.status === 200 && eb.status === 200 && ea.text.includes(CA) && !ea.text.includes(CB) && eb.text.includes(CB) && !eb.text.includes(CA));
      const ids = r => (r.json && r.json.items || []).map(i => i.id);
      c.check('item ids do not overlap between users', !ids(ga).some(i => ids(gb).includes(i)));
      // cross-user write attempts by id
      const pa = await mem(B, 'PATCH', `/v1/memory/items/${idA}`, { text: `tampered ${CB}` });
      c.check('B cannot PATCH A\'s item (404/403, not 2xx)', pa.status === 404 || pa.status === 403, pa.status);
      const da = await mem(B, 'DELETE', `/v1/memory/items/${idA}`);
      c.check('B cannot DELETE A\'s item (404/403, not 2xx)', da.status === 404 || da.status === 403, da.status);
      const ga2 = await mem(A, 'GET', '/v1/memory');
      c.check('A\'s item is intact after B\'s attempts (text unchanged)', ga2.text.includes(CA) && !ga2.text.includes('tampered'));
      // B wipes ALL of B's memory; A must be untouched
      const wipe = await mem(B, 'DELETE', '/v1/memory');
      c.check('B deletes all of B\'s memory (2xx)', wipe.status >= 200 && wipe.status < 300, wipe.status + ' ' + wipe.text.slice(0, 100));
      c.check('after B\'s wipe-all A still has the item', (await mem(A, 'GET', '/v1/memory')).text.includes(CA));
      // API key boundary
      if (L.apiKey()) {
        for (const [m, p, b] of [['GET', '/v1/memory'], ['GET', '/v1/memory/export'], ['POST', '/v1/memory/items', { kind: 'fact', text: 'x' }], ['DELETE', '/v1/memory']]) {
          const r = await req(m, p, { key: true, csrf: true, body: b });
          c.check(`Bearer API key ${m} ${p} -> 403 scope_denied (memory is browser-session only)`, r.status === 403 && /scope_denied/.test(r.text), `${r.status} ${r.text.slice(0, 80)}`);
        }
        const rk = await req('PATCH', '/v1/settings', { key: true, csrf: true, body: { memory: { enabled: true } } });
        c.check('Bearer API key cannot PATCH /v1/settings (403)', rk.status === 403, rk.status);
      } else c.skip('API key boundary', 'no EVE_TEST_API_KEY');
      // threads + search isolation (positive control for A)
      const now = new Date().toISOString();
      const imp = await mem(A, 'POST', '/v1/threads/import', { threads: [{ client_id: `qa-release-iso-${T}`, title: `QA iso ${T}`, created_at: now, messages: [{ role: 'user', content: `please remember ${CA}`, created_at: now }, { role: 'assistant', content: `noted ${CA}`, created_at: now }] }] });
      c.check('A imports a QA thread containing the canary', imp.status === 200 && imp.json && imp.json.results && imp.json.results[0].thread_id, `${imp.status} ${imp.text.slice(0, 100)}`);
      tidA = imp.json && imp.json.results && imp.json.results[0].thread_id;
      if (tidA) {
        const sa = await mem(A, 'GET', `/v1/search?q=${CA}`), sb = await mem(B, 'GET', `/v1/search?q=${CA}`);
        const n = r => (r.json && (r.json.data || r.json.results || r.json.hits) || []).length;
        c.check('search positive control: A finds the canary thread', sa.status === 200 && (n(sa) > 0 || sa.text.includes(tidA)), `${sa.status} ${sa.text.slice(0, 120)}`);
        c.check('search: B gets NO hit for A\'s canary', sb.status === 200 && !sb.text.includes(tidA) && !sb.text.includes(CA), `${sb.status} ${sb.text.slice(0, 120)}`);
        for (const sub of ['', '/messages']) { const r = await mem(B, 'GET', `/v1/threads/${tidA}${sub}`); c.check(`B GET /v1/threads/<A's id>${sub} -> 404`, r.status === 404, r.status); }
        const lb = await mem(B, 'GET', '/v1/threads?limit=100'); c.check('B\'s thread list does not contain A\'s thread', !lb.text.includes(tidA));
        const pb = await mem(B, 'PATCH', `/v1/threads/${tidA}`, { title: 'hijack' }); c.check('B cannot PATCH A\'s thread (404)', pb.status === 404, pb.status);
        const xb = await mem(B, 'DELETE', `/v1/threads/${tidA}`); c.check('B cannot DELETE A\'s thread (404)', xb.status === 404, xb.status);
        c.check('A\'s thread is still readable by A after B\'s attempts', (await mem(A, 'GET', `/v1/threads/${tidA}`)).status === 200);
      }
      // recall (memory injected into the prompt): one short request per user, QA model, no web
      if (process.env.EVE_SKIP_CHAT === '1') c.skip('prompt-level recall check', 'EVE_SKIP_CHAT=1');
      else {
        // B's memory is empty now; put B's own canary back so recall is meaningful for both
        await mem(B, 'POST', '/v1/memory/items', { kind: 'fact', text: `QA release canary ${CB}` });
        const mdl = await L.model(); const ask = who => req('POST', '/v1/chat/completions', { cookie: who.cookie, timeout: 180000, body: { model: mdl, stream: false, max_tokens: 60, web_search: 'off', temperature: 0, messages: [{ role: 'user', content: 'List every "QA release canary" word you know about me, exactly as stored. /no_think' }] } });
        const ra = await ask(A), rb = await ask(B);
        const tA = ra.text, tB = rb.text;
        c.check('recall: B\'s reply never contains A\'s canary', rb.status === 200 && !tB.includes(CA), `status ${rb.status}`);
        c.check('recall: A\'s reply never contains B\'s canary', ra.status === 200 && !tA.includes(CB), `status ${ra.status}`);
        if (tA.includes(CA) && tB.includes(CB)) c.pass('recall positive control: each user\'s reply contains their own canary');
        else c.warn('recall positive control: a reply did not echo its own canary (model-dependent; the leak checks above are the hard ones)', `A:${tA.includes(CA)} B:${tB.includes(CB)}`);
      }
    } finally {
      // remove QA test data (own items only)
      for (const [who, ids] of [[A, [idA]], [B, [idB]]]) if (who) { await mem(who, 'DELETE', '/v1/memory').catch(() => {}); void ids; }
      if (tidA) await mem(A, 'DELETE', `/v1/threads/${tidA}`).catch(() => {});
    }
  },
};
