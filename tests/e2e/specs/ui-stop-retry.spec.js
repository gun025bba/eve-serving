// (2) stop → POST .../cancel (X-Eve-Request, no body) + 중지됨
// (3) failed / no-answer thread → failure text + 다시 시도 → answer
// (4) retry does not save the user message twice
const { test } = require('@playwright/test');
const { auth } = require('./helpers');
const { apiKey } = require('../env');
const { S, T, CANCEL_RE, requireIdentity, lastAsst, captureMeta, send, track, expect } = require('./ui-helpers');
test.beforeEach(requireIdentity);

async function messages(request, tid) { return (await (await request.get(`/v1/threads/${tid}/messages`, { headers: auth() })).json()).data; }

test('stop mid-generation sends cancel and shows 중지됨', async ({ page, request }) => {
  const meta = captureMeta(page); const cancels = track(page, CANCEL_RE);
  await send(page, 'Write a long essay about rivers. /no_think');
  await expect(lastAsst(page)).not.toBeEmpty();
  await page.click(S.send);                                   // send button acts as stop while generating
  await expect(lastAsst(page)).toContainText(T.STOPPED);
  await expect(page.locator(S.error)).toHaveCount(0);
  expect(cancels.length).toBeGreaterThan(0);
  expect(cancels[0].method).toBe('POST'); expect(cancels[0].headers['x-eve-request']).toBe('1'); expect(cancels[0].body || '').toBe('');
  await page.reload(); await expect(lastAsst(page)).toContainText(T.STOPPED);   // persisted cancelled row
  if (apiKey() && meta.thread) { const a = (await messages(request, meta.thread)).find(m => m.id === meta.msg); expect(['failed', 'cancelled', 'incomplete']).toContain(a?.status); }
});

test('failed reply shows failure text + 다시 시도; retry yields an answer without duplicating the user message', async ({ page, request }) => {
  const meta = captureMeta(page);
  // produce a cancelled/failed assistant row by stopping right away, then reload so the UI renders it from the server
  await send(page, 'Say the word apple. /no_think');
  await page.click(S.send); await page.reload();
  const note = lastAsst(page);
  await expect(note).toContainText(new RegExp(`${T.STOPPED}|${T.NO_ANSWER}|못했어요`));
  const btn = page.locator(`${S.asst} ${S.retry}, ${S.error} ${S.retry}`).last();
  await expect(btn).toBeVisible(); await expect(btn).toHaveText(T.RETRY);
  const regen = track(page, /\/v1\/chat\/completions/);
  await btn.click();
  await expect(lastAsst(page)).toContainText(/apple/i, { timeout: 120_000 });
  await expect(page.locator(S.error)).toHaveCount(0);
  if (meta.msg && regen[0]?.body) expect(JSON.parse(regen[0].body).replace_message_id).toBe(meta.msg);
  test.skip(!apiKey() || !meta.thread, 'API key needed for server-side duplicate check');
  const msgs = await messages(request, meta.thread);
  expect(msgs.filter(m => m.role === 'user').length, 'user message saved once').toBe(1);
  expect(msgs.filter(m => m.role === 'assistant').length).toBe(1);
});

test('thread ending with a user turn and no reply shows 답변을 받지 못했어요 + 다시 시도', async ({ page }) => {
  // Needs a seeded thread whose last message is a user turn (EVE_SEED_NOREPLY_THREAD); the API cannot create that state directly.
  const tid = process.env.EVE_SEED_NOREPLY_THREAD;
  test.skip(!tid, 'set EVE_SEED_NOREPLY_THREAD to a thread id ending with a user turn');
  await page.goto(`/ui/#/c/${tid}`);
  const e = page.locator(S.error);
  await expect(e).toHaveCount(1); await expect(e).toContainText(T.NO_ANSWER);
  await e.locator(S.retry).click();
  await expect(page.locator(S.error)).toHaveCount(0, { timeout: 120_000 });
  await expect(lastAsst(page)).not.toBeEmpty();
});
