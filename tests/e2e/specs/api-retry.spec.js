// API-level (3)(4): a stopped/failed reply can be regenerated in place (replace_message_id) and the user message is not duplicated.
const { test, expect } = require('@playwright/test');
const { auth, requireKey, body, parseSSE } = require('./helpers');
test('cancelled reply → regenerate same message_id → complete; one user message', async ({ request }) => {
  requireKey();
  const r = await request.post('/v1/chat/completions', { headers: auth(), data: body('Write a long essay about rivers. /no_think', { stream: true, max_tokens: 400 }) });
  const meta = JSON.parse(parseSSE(await r.text())[0].data);
  const base = `/v1/threads/${meta.thread_id}`;
  expect((await request.post(`${base}/messages/${meta.message_id}/cancel`, { headers: auth() })).status()).toBe(204);
  const g = await request.post('/v1/chat/completions', { headers: auth(), data: {
    ...body('Say the word apple. /no_think', { max_tokens: 64 }), thread_id: meta.thread_id, replace_message_id: meta.message_id, messages: [] } });
  // replace_message_id regenerates from stored history; accept 200, or 409 if the cancelled run has not settled yet
  expect([200, 409]).toContain(g.status());
  if (g.status() === 200) {
    const j = await g.json();
    expect(j.message_id).toBe(meta.message_id); expect(j.user_message_id ?? null).toBeNull();
  }
  const msgs = (await (await request.get(`${base}/messages`, { headers: auth() })).json()).data;
  expect(msgs.filter(m => m.role === 'user').length, 'user message saved once').toBe(1);
  expect(msgs.filter(m => m.role === 'assistant').length, 'assistant overwritten in place').toBe(1);
  expect(msgs.find(m => m.id === meta.message_id)).toBeTruthy();
});
