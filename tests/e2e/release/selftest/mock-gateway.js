'use strict';
// Minimal stand-in for the 5.0.x gateway auth/memory/thread surface, used ONLY to self-test the release bundle logic (cases 1 and 2) without touching any real
// instance. Listens on loopback 8003 (QA port, normally down). State shared with fake-cli.js via a temp file. NOT a security test of the real gateway.
const http = require('http'), fs = require('fs'), crypto = require('crypto');
const ST = process.env.MOCK_STATE || '/tmp/eve-release-selftest/state.json', VERSION = process.env.MOCK_VERSION || '5.0.1', KEY = 'selftest-key';
const load = () => { try { return JSON.parse(fs.readFileSync(ST, 'utf8')); } catch { return { codes: {}, sessions: [], n: 0 }; } };
const save = s => fs.writeFileSync(ST, JSON.stringify(s));
const data = { settings: {}, memory: {}, threads: {} };   // per user
http.createServer((req, res) => {
  let body = ''; req.on('data', d => body += d); req.on('end', () => {
    const st = load(), u = new URL(req.url, 'http://x'), p = u.pathname; let j; try { j = body ? JSON.parse(body) : {}; } catch { j = {}; }
    const out = (code, o, h = {}) => { res.writeHead(code, { 'content-type': 'application/json', 'x-eve-version': VERSION, ...h }); res.end(o === undefined ? '' : JSON.stringify(o)); };
    const ck = /eve_session=([^;]+)/.exec(req.headers.cookie || ''); let who = null, prov = null;
    if (ck) { const s = st.sessions.find(x => x.tok === ck[1] && x.state === 'active'); if (s) { who = s.user; prov = 'session'; } }
    else if ((req.headers.authorization || '') === `Bearer ${KEY}`) { who = 'api:qa'; prov = 'api-key'; }
    if (p === '/health') return out(200, { status: 'ok' });
    if (p === '/' ) return out(404, { error_code: 'not_found' });
    if (p === '/ui/' || p === '/ui') return out(200, {});
    if (p === '/v1/pair' && req.method === 'POST') {
      const l = st.codes[j.code]; if (!l) return out(401, { error_code: 'invalid_code' }); delete st.codes[j.code];
      const tok = crypto.randomBytes(16).toString('hex'); st.n++; st.sessions.push({ id: st.n, user: l, tok, state: 'active' }); save(st);
      return out(204, undefined, { 'set-cookie': `eve_session=${tok}; Path=/; HttpOnly` });
    }
    if (!who) return out(401, { error_code: 'invalid_session' });
    if (p === '/whoami') return out(200, { authenticated: true, user: who, provider: prov, version: VERSION });
    if (p === '/v1/models') return out(200, { data: [{ id: 'mock-model' }] });
    const mine = k => (data[k][who] = data[k][who] || (k === 'threads' ? [] : k === 'memory' ? [] : {}));
    if (p === '/v1/actions') return prov === 'session' ? out(200, { data: [] }) : out(403, { error_code: 'scope_denied' });
    if (p.startsWith('/v1/memory') || (p === '/v1/settings' && req.method === 'PATCH')) { if (prov !== 'session') return out(403, { error_code: 'scope_denied' }); }
    if (p === '/v1/settings') { const s = mine('settings'); if (req.method === 'PATCH') Object.assign(s, j); return out(200, { system: { memory_available: true }, memory: { enabled: !!(s.memory && s.memory.enabled) }, ui: s.ui || {} }); }
    if (p === '/v1/memory' && req.method === 'GET') return out(200, { items: mine('memory') });
    if (p === '/v1/memory' && req.method === 'DELETE') { data.memory[who] = []; return out(204); }
    if (p === '/v1/memory/export') return out(200, { items: mine('memory') });
    if (p === '/v1/memory/items' && req.method === 'POST') { const it = { id: 'mem_' + crypto.randomBytes(4).toString('hex'), kind: j.kind, text: j.text }; mine('memory').push(it); return out(201, it); }
    let m = /^\/v1\/memory\/items\/(.+)$/.exec(p);
    if (m) { const i = mine('memory').findIndex(x => x.id === m[1]); if (i < 0) return out(404, { error_code: 'not_found' }); if (req.method === 'DELETE') { mine('memory').splice(i, 1); return out(204); } Object.assign(mine('memory')[i], { text: j.text }); return out(200, mine('memory')[i]); }
    if (p === '/v1/threads/import') { const r = j.threads.map(t => { let x = mine('threads').find(y => y.client_id === t.client_id); if (!x) { x = { id: 'thr_' + crypto.randomBytes(4).toString('hex'), ...t }; mine('threads').push(x); } return { client_id: t.client_id, thread_id: x.id, status: 'created' }; }); return out(200, { results: r }); }
    if (p === '/v1/threads') return out(200, { data: mine('threads').map(t => ({ id: t.id, title: t.title })) });
    if (p === '/v1/search') { const q = u.searchParams.get('q'); return out(200, { data: mine('threads').filter(t => JSON.stringify(t).includes(q)).map(t => ({ thread_id: t.id })) }); }
    m = /^\/v1\/threads\/([^/]+)(\/messages)?$/.exec(p);
    if (m) { const i = mine('threads').findIndex(x => x.id === m[1]); if (i < 0) return out(404, { error_code: 'not_found' }); const t = mine('threads')[i];
      if (req.method === 'DELETE') { mine('threads').splice(i, 1); return out(204); } if (req.method === 'PATCH') return out(200, t); return out(200, m[2] ? { data: t.messages } : t); }
    return out(404, { error_code: 'not_found' });
  });
}).listen(8003, '127.0.0.1', () => console.log('mock gateway on 8003'));
