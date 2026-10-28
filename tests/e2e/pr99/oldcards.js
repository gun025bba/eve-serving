'use strict';
// Approve every still-pending memory.propose card of qa-a that was created by an older build (hash computed over the producer's key order).
// Run right after deploying the fixed build, BEFORE anything else. QA only. Prints counts per outcome; never prints tokens.
process.env.EVE_TARGET = process.env.EVE_TARGET || 'qa';
const L = require('../release/lib'); const { req } = L;
if (String(L.PORT) !== '8002') throw new Error('QA 8002 only');
(async () => {
  let st = L.stateRead('pr99-A.json'); if (!st || (await L.sessionValid(st.cookie)).status !== 200) { st = { login: 'qa-a@test.invalid', cookie: await L.pairQA('qa-a@test.invalid') }; L.stateWrite('pr99-A.json', st); }
  const cookie = st.cookie, cutoff = Date.parse(process.argv[2] || '') || Date.now();
  const ls = await req('GET', '/v1/actions', { cookie }); const cards = (ls.json && ls.json.data || []).filter(c => c.tool === 'memory.propose' && c.status === 'pending' && Date.parse(c.created_at) < cutoff);
  console.log(`pending memory.propose cards created before ${new Date(cutoff).toISOString()}: ${cards.length}`);
  const out = {}; const ex = [];
  for (const c of cards) {
    const ri = await req('POST', `/v1/actions/${c.action_id}/reissue`, { cookie, csrf: true });
    if (ri.status !== 200) { out[`reissue ${ri.status}`] = (out[`reissue ${ri.status}`] || 0) + 1; continue; }
    const ap = await req('POST', `/v1/actions/${c.action_id}/approve`, { cookie, csrf: true, body: { confirm_token: ri.json.confirm_token, payload_hash: c.payload_hash } });
    const k = `approve ${ap.status}${ap.json && ap.json.error_code ? ' ' + ap.json.error_code : ''}`; out[k] = (out[k] || 0) + 1;
    if (ex.length < 3) ex.push(`${c.created_at} op=${c.payload_view && c.payload_view.op} -> ${k}`);
  }
  console.log(JSON.stringify(out)); ex.forEach(e => console.log('  e.g.', e));
  const m = await req('GET', '/v1/memory', { cookie }); console.log('items now in A memory:', ((m.json && (m.json.items || m.json.data)) || []).length);
})();
