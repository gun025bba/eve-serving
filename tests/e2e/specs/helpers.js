const { test } = require('@playwright/test');
const { MODEL, ORIGIN, apiKey } = require('../env');
const auth = () => ({ Authorization: `Bearer ${apiKey()}` });
function requireKey() { test.skip(!apiKey(), 'EVE_TEST_API_KEY / EVE_TEST_API_KEY_FILE not set'); }
// browser-style CSRF headers
const csrf = (o = {}) => ({ 'X-Eve-Request': '1', Origin: ORIGIN, 'Sec-Fetch-Site': 'same-origin', 'Content-Type': 'application/json', ...o });
const body = (content, extra = {}) => ({ model: MODEL, thread_id: 'new', max_tokens: 64,
  messages: [{ role: 'user', content }], ...extra });
function parseSSE(text) {
  const events = [];
  for (const block of text.split(/\n\n+/)) {
    let ev = 'message', data = [];
    for (const l of block.split('\n')) { if (l.startsWith('event:')) ev = l.slice(6).trim(); else if (l.startsWith('data:')) data.push(l.slice(5).trim()); }
    if (data.length) events.push({ event: ev, data: data.join('\n') });
  }
  return events;
}
module.exports = { auth, requireKey, csrf, body, parseSSE };

// Start a streaming chat with Node fetch, read until eve.meta, return {meta, drain}.
// drain(ms) keeps reading until the stream ends (or ms), returning {chunks, contentChunks, done, ended}.
const { BASE_URL } = require('../env');
async function streamUntilMeta(content, extra = {}) {
  const res = await fetch(BASE_URL + '/v1/chat/completions', { method: 'POST',
    headers: { ...auth(), 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify(body(content, { stream: true, ...extra })) });
  if (res.status !== 200) throw new Error(`chat status ${res.status}: ${await res.text()}`);
  const reader = res.body.getReader(); const dec = new TextDecoder(); let buf = ''; let meta = null;
  while (!meta) {
    const { value, done } = await reader.read(); if (done) throw new Error('stream ended before eve.meta');
    buf += dec.decode(value, { stream: true });
    const ev = parseSSE(buf).find(e => e.event === 'eve.meta'); if (ev) meta = JSON.parse(ev.data);
  }
  async function drain(ms) {
    let tail = ''; let ended = false; const deadline = Date.now() + ms;
    while (Date.now() < deadline) {
      const r = await Promise.race([reader.read(), new Promise(res => setTimeout(() => res({ timeout: true }), deadline - Date.now()))]);
      if (r.timeout) break; if (r.done) { ended = true; break; } tail += dec.decode(r.value, { stream: true });
    }
    if (!ended) await reader.cancel().catch(() => {});
    const ev = parseSSE(tail);
    const contentChunks = ev.filter(e => { try { const d = JSON.parse(e.data).choices?.[0]?.delta; return d && (d.content || d.reasoning || d.reasoning_content); } catch { return false; } }).length;
    return { events: ev, contentChunks, done: ev.some(e => e.data === '[DONE]'), ended };
  }
  return { meta, drain };
}
async function waitSettled(request, tid, mid, ms = 20000) {
  const end = Date.now() + ms; let m;
  while (Date.now() < end) {
    const j = await (await request.get(`/v1/threads/${tid}/messages`, { headers: auth() })).json();
    m = j.data.find(x => x.id === mid); if (m && m.status !== 'generating') return m;
    await new Promise(r => setTimeout(r, 500));
  }
  return m;
}
module.exports.streamUntilMeta = streamUntilMeta; module.exports.waitSettled = waitSettled;
