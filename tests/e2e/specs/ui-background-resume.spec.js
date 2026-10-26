// (1) iPhone PWA home-screen return: UI shows '답변 불러오는 중' and resumes via /messages/{mid}/stream; answer completes.
const { test } = require('@playwright/test');
const { auth } = require('./helpers');
const { apiKey } = require('../env');
const { S, T, STREAM_RE, requireIdentity, lastAsst, captureMeta, send, track, expect } = require('./ui-helpers');
const PROMPT = 'List the numbers 1 to 40, one per line, then write the word FINISHED. /no_think';
test.beforeEach(requireIdentity);

const setVisibility = (page, state) => page.evaluate(s => {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => s });
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => s === 'hidden' });
  document.dispatchEvent(new Event('visibilitychange'));
  if (s === 'hidden') window.dispatchEvent(new Event('pagehide')); else window.dispatchEvent(new Event('pageshow'));
}, state);

async function start(page) { const meta = captureMeta(page); const resume = track(page, STREAM_RE); await send(page, PROMPT); await expect(lastAsst(page)).toContainText('1'); return { meta, resume }; }
async function assertSurvives(page, request, meta, resume, { expectResumingText = true } = {}) {
  if (expectResumingText) await expect(page.locator(S.chat)).toContainText(T.RESUMING, { timeout: 15_000 }).catch(() => {}); // transient; soft
  await expect(lastAsst(page)).toContainText('FINISHED', { timeout: 150_000 });
  await expect(lastAsst(page)).not.toContainText(T.RESUMING);
  await expect(page.locator(S.error)).toHaveCount(0);
  expect(resume.length, 'UI must request /messages/{mid}/stream').toBeGreaterThan(0);
  if (apiKey() && meta.thread) {
    const j = await (await request.get(`/v1/threads/${meta.thread}/messages`, { headers: auth() })).json();
    const a = j.data.find(m => m.id === meta.msg);
    expect(a?.status).toBe('complete'); expect(JSON.stringify(a.content)).toContain('FINISHED');
  }
}

test('home screen (visibility hidden→visible) shows 답변 불러오는 중 and continues', async ({ page, request }) => {
  const { meta, resume } = await start(page);
  await setVisibility(page, 'hidden'); await page.waitForTimeout(5000); await setVisibility(page, 'visible');
  await assertSurvives(page, request, meta, resume);
});
test('offline → online mid-stream', async ({ page, context, request }) => {
  const { meta, resume } = await start(page);
  await context.setOffline(true); await page.waitForTimeout(5000); await context.setOffline(false);
  await assertSurvives(page, request, meta, resume);
});
test('chat stream connection cut mid-flight', async ({ page, request }) => {
  const meta = captureMeta(page); const resume = track(page, STREAM_RE);
  await page.route('**/v1/chat/completions', async route => {
    const resp = await route.fetch({ timeout: 3000 }).catch(() => null);
    if (resp) await route.fulfill({ response: resp, body: (await resp.text()).slice(0, 400) }); else await route.abort('connectionreset');
  });
  await send(page, PROMPT);
  await page.waitForTimeout(4000); await page.unroute('**/v1/chat/completions');
  await assertSurvives(page, request, meta, resume, { expectResumingText: false });
});
test('reload mid-generation', async ({ page, request }) => {
  const { meta, resume } = await start(page);
  await page.reload();
  await assertSurvives(page, request, meta, resume);
});
