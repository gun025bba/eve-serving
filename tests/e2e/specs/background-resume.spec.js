// iPhone PWA bug: answer disappears after sending then going to home screen.
// Expectation (UI >= 1.1.3): UI resumes via GET /v1/threads/{id}/messages/{mid}/stream and the final answer survives.
const { test, expect } = require('@playwright/test');
const { auth } = require('./helpers');
const { BROWSER_IDENTITY, apiKey } = require('../env');
const PROMPT = 'List the numbers 1 to 40, one per line, then write the word FINISHED.';
const STREAM_RE = /\/v1\/threads\/[^/]+\/messages\/[^/]+\/stream/;

test.beforeEach(() => test.skip(!BROWSER_IDENTITY, 'needs browser identity injection (EVE_BROWSER_IDENTITY=1) — not available yet'));

async function send(page) {
  const meta = {};
  page.on('response', async r => {
    if (r.url().includes('/v1/chat/completions')) {
      const t = await r.text().catch(() => ''); const m = t.match(/"thread_id":"([^"]+)","message_id":"([^"]+)"/);
      if (m) { meta.thread = m[1]; meta.msg = m[2]; }
    }
  });
  await page.goto('/ui/?new=1');
  await page.locator('textarea').first().fill(PROMPT);
  await page.keyboard.press('Enter');
  await expect(page.getByText('1', { exact: false }).last()).toBeVisible();   // first tokens arrived
  return meta;
}
const setVisibility = (page, state) => page.evaluate(s => {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => s });
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => s === 'hidden' });
  document.dispatchEvent(new Event('visibilitychange'));
  if (s === 'hidden') window.dispatchEvent(new Event('pagehide'));
}, state);

async function assertSurvives(page, request, meta, resumeReqs) {
  await expect(page.getByText('FINISHED').last()).toBeVisible({ timeout: 150_000 });
  expect(resumeReqs.length, 'UI must request /messages/{mid}/stream').toBeGreaterThan(0);
  if (apiKey() && meta.thread) {
    const j = await (await request.get(`/v1/threads/${meta.thread}/messages`, { headers: auth() })).json();
    const a = j.data.find(m => m.id === meta.msg);
    expect(a?.status).toBe('complete'); expect(JSON.stringify(a.content)).toContain('FINISHED');
  }
}
function trackResume(page) { const r = []; page.on('request', q => { if (STREAM_RE.test(q.url())) r.push(q.url()); }); return r; }

test('visibility hidden -> visible mid-stream', async ({ page, request }) => {
  const resume = trackResume(page); const meta = await send(page);
  await setVisibility(page, 'hidden'); await page.waitForTimeout(5000); await setVisibility(page, 'visible');
  await assertSurvives(page, request, meta, resume);
});
test('offline -> online mid-stream', async ({ page, context, request }) => {
  const resume = trackResume(page); const meta = await send(page);
  await context.setOffline(true); await page.waitForTimeout(5000); await context.setOffline(false);
  await assertSurvives(page, request, meta, resume);
});
test('stream request aborted mid-flight', async ({ page, request }) => {
  const resume = trackResume(page);
  await page.route('**/v1/chat/completions', async route => {
    // let the gateway start the detached generation, then drop the connection to the client
    const resp = await route.fetch({ timeout: 3000 }).catch(() => null);
    if (resp) await route.fulfill({ response: resp, body: (await resp.text()).slice(0, 400) }); else await route.abort('connectionreset');
  });
  const meta = await send(page).catch(() => ({}));
  await page.unroute('**/v1/chat/completions');
  await assertSurvives(page, request, meta, resume);
});
test('reload mid-generation', async ({ page, request }) => {
  const resume = trackResume(page); const meta = await send(page);
  await page.reload();
  await assertSurvives(page, request, meta, resume);
});
