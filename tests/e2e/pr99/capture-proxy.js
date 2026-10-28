'use strict';
// QA-only capture/stub upstream for PR #99 tests. Listens on 127.0.0.1:EVE_PROXY_PORT (default 8090), forwards everything to the real model server (EVE_REAL_UPSTREAM, default 127.0.0.1:8080)
// except /v1/chat/completions when mode=stub. Keeps every chat request body the gateway sent upstream IN MEMORY (served at GET /__captured, loopback only); nothing is written to disk.
//   POST /__mode {mode:'stub'|'pass', tool?:'memory_propose'|'web_search'|null, args?:{...}}   -> how the next requests are answered
//   stub: if `tool` is set and the conversation has no tool result yet, answer with that tool call; otherwise answer "STUB-OK". No model load at all.
const http = require('http');
const PORT = +(process.env.EVE_PROXY_PORT || 8090), REAL = new URL(process.env.EVE_REAL_UPSTREAM || 'http://127.0.0.1:8080');
if (![8090, 8091, 8092].includes(PORT)) throw new Error('refusing odd port');
let mode = { mode: 'stub', tool: null, args: {} }; const captured = [];
const send = (res, code, obj) => { const b = JSON.stringify(obj); res.writeHead(code, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(b) }); res.end(b); };
http.createServer((req, res) => {
  const chunks = []; req.on('data', d => chunks.push(d)); req.on('end', () => {
    const raw = Buffer.concat(chunks); let rec_ = null;
    if (req.url.startsWith('/__captured')) return send(res, 200, captured);
    if (req.url === '/__reset') { captured.length = 0; return send(res, 200, { ok: true }); }
    if (req.url === '/__mode' && req.method === 'POST') { mode = JSON.parse(raw.toString()); return send(res, 200, mode); }
    if (req.url.startsWith('/v1/chat/completions') && req.method === 'POST') {
      let body = {}; try { body = JSON.parse(raw.toString()); } catch {}
      const rec = { seq: captured.length + 1, at: Date.now(), mode: mode.mode, body }; captured.push(rec); rec_ = rec;
      if (mode.mode === 'stub') {
        const msgs = body.messages || [], lastUser = msgs.map(m => m.role).lastIndexOf('user'), hasToolResult = msgs.slice(lastUser + 1).some(m => m.role === 'tool');   // tool result produced in THIS turn
        const call = mode.tool && !hasToolResult ? { id: 'call_stub1', type: 'function', function: { name: mode.tool, arguments: JSON.stringify(mode.args || {}) } } : null;
        const msg = call ? { role: 'assistant', content: '', tool_calls: [call] } : { role: 'assistant', content: 'STUB-OK' };
        const fin = call ? 'tool_calls' : 'stop';
        if (body.stream) {
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          const ch = (delta, f) => `data: ${JSON.stringify({ id: 'stub', object: 'chat.completion.chunk', model: body.model, choices: [{ index: 0, delta, finish_reason: f }] })}\n\n`;
          if (call) res.write(ch({ role: 'assistant', content: '', tool_calls: [{ index: 0, ...call }] }, null)); else res.write(ch({ role: 'assistant', content: 'STUB-OK' }, null));
          res.write(ch({}, fin)); res.end('data: [DONE]\n\n'); return;
        }
        return send(res, 200, { id: 'stub', object: 'chat.completion', model: body.model, choices: [{ index: 0, finish_reason: fin, message: msg }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } });
      }
    }
    const up = http.request({ host: REAL.hostname, port: REAL.port, method: req.method, path: req.url, headers: { ...req.headers, host: REAL.host } }, r => { if (typeof rec_ !== 'undefined' && rec_) { rec_.upstream_ttfb_ms = Date.now() - rec_.at; r.once('data', () => { rec_.upstream_first_data_ms = Date.now() - rec_.at; }); } res.writeHead(r.statusCode, r.headers); r.pipe(res); });
    up.on('error', e => send(res, 502, { error: String(e.message) })); up.end(raw);
  });
}).listen(PORT, '127.0.0.1', () => console.log(`capture-proxy on 127.0.0.1:${PORT} -> ${REAL.host}`));
