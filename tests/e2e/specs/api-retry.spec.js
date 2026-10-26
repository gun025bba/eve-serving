// API-level (3)(4): a stopped/failed reply can be regenerated in place (replace_message_id) and the user message is not duplicated.
const { test, expect } = require('@playwright/test');
const { auth, requireKey, body, parseSSE } = require('./helpers');
test('cancelled reply → regenerate same message_id → complete; one user message', async ({ request }) => {
  requireKey();
  const { streamUntilMeta, waitSettled } = require('./helpers');
  const prompt = 'Write a detailed multi-paragraph essay about rivers of the world. /no_think';
  const { meta, drain } = await streamUntilMeta(prompt, { max_tokens: 256 });
  const base = `/v1/threads/${meta.thread_id}`;
  expect((await request.post(`${base}/messages/${meta.message_id}/cancel`, { headers: auth() })).status()).toBe(204);
  await drain(10000);
  const c = await waitSettled(request, meta.thread_id, meta.message_id);
  expect(['cancelled', 'failed', 'incomplete'], 'cancelled before regenerate').toContain(c?.status);
  // messages must be non-empty (400 otherwise); like the UI, resend the conversation up to the user turn.
  const g = await request.post('/v1/chat/completions', { headers: auth(), data: {
    ...body(prompt, { max_tokens: 64 }), thread_id: meta.thread_id, replace_message_id: meta.message_id } });
  expect(g.status()).toBe(200);
  const j = await g.json();
  expect(j.message_id).toBe(meta.message_id); expect(j.user_message_id ?? null).toBeNull();
  const msgs = (await (await request.get(`${base}/messages`, { headers: auth() })).json()).data;
  expect(msgs.filter(m => m.role === 'user').length, 'user message saved once').toBe(1);
  expect(msgs.filter(m => m.role === 'assistant').length, 'assistant overwritten in place').toBe(1);
  expect(msgs.find(m => m.id === meta.message_id)?.status).toBe('complete');
});

test.fixme('re-send without replace_message_id does not duplicate the user message', async ({ request }) => {
  // Known issue (not a new bug): re-sending the same user turn without replace_message_id saves the user message twice.
  // Gateway patch is coming; un-fixme once deployed.
  requireKey();
  const a = await (await request.post('/v1/chat/completions', { headers: auth(), data: body('Say ok. /no_think', { max_tokens: 32 }) })).json();
  await request.post('/v1/chat/completions', { headers: auth(), data: { ...body('Say ok. /no_think', { max_tokens: 32 }), thread_id: a.thread_id } });
  const msgs = (await (await request.get(`/v1/threads/${a.thread_id}/messages`, { headers: auth() })).json()).data;
  expect(msgs.filter(m => m.role === 'user').length).toBe(1);
});
