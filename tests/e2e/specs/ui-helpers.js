// Selectors/strings from eve-chat-ui PR #4 (UI 1.1.3).
const { test, expect } = require('@playwright/test');
const { BROWSER_IDENTITY } = require('../env');
const S = {
  input: '#input', send: '#sendBtn', chat: '#chatView', convList: '#convList',
  asst: '.msg.assistant', error: '.msg.error', retry: 'button.retry', failedNote: '.msg-note.failed',
  resuming: '.msg-note.resuming', version: '#appVersion', openSettings: '#openSettings', settings: '#settings',
  backBtn: '#backBtn', backBadge: '#backBadge', proactive: '.proactive',
};
const T = { RESUMING: '답변 불러오는 중', STOPPED: '중지됨', NO_ANSWER: '답변을 받지 못했어요', RETRY: '다시 시도' };
const STREAM_RE = /\/v1\/threads\/[^/]+\/messages\/[^/]+\/stream/;
const CANCEL_RE = /\/v1\/threads\/([^/]+)\/messages\/([^/]+)\/cancel$/;
function requireIdentity() { test.skip(!BROWSER_IDENTITY, 'needs browser identity injection (EVE_BROWSER_IDENTITY=1) — not available yet'); }
const lastAsst = page => page.locator(S.asst).last();
// capture thread/message ids from eve.meta of the chat stream
function captureMeta(page) {
  const meta = {};
  page.on('response', async r => {
    if (!r.url().includes('/v1/chat/completions')) return;
    const t = await r.text().catch(() => ''); const m = t.match(/"thread_id":"([^"]+)","message_id":"([^"]+)"/) || t.match(/"message_id":"([^"]+)".*?"thread_id":"([^"]+)"/);
    if (m) Object.assign(meta, m[0].startsWith('{"thread') || m[0].startsWith('"thread') ? { thread: m[1], msg: m[2] } : { msg: m[1], thread: m[2] });
  });
  return meta;
}
async function send(page, text) {
  await page.goto('/ui/?new=1');
  await page.fill(S.input, text);
  await page.click(S.send);
}
function track(page, re) { const r = []; page.on('request', q => { if (re.test(q.url())) r.push({ url: q.url(), method: q.method(), headers: q.headers(), body: q.postData() }); }); return r; }
module.exports = { S, T, STREAM_RE, CANCEL_RE, requireIdentity, lastAsst, captureMeta, send, track, expect };
