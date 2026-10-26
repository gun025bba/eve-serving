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
