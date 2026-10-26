const { test, expect } = require('@playwright/test');
const { auth, requireKey, body, parseSSE } = require('./helpers');
test.describe('chat', () => {
  test.beforeEach(() => requireKey());
  test('non-stream completion persists to thread', async ({ request }) => {
    const r = await request.post('/v1/chat/completions', { headers: auth(), data: body('Reply with the single word: pong') });
    expect(r.status()).toBe(200);
    const j = await r.json();
    expect(j.thread_id).toBeTruthy(); expect(j.message_id).toBeTruthy();
    expect(j.choices[0].message.content.length).toBeGreaterThan(0);
    const m = await (await request.get(`/v1/threads/${j.thread_id}/messages`, { headers: auth() })).json();
    expect(m.data.some(x => x.id === j.message_id && x.role === 'assistant')).toBe(true);
  });
  test('stream starts with eve.meta and ends with [DONE]', async ({ request }) => {
    const r = await request.post('/v1/chat/completions', { headers: auth(), data: body('Count 1 to 5', { stream: true }) });
    expect(r.status()).toBe(200);
    const ev = parseSSE(await r.text());
    expect(ev[0].event).toBe('eve.meta');
    const meta = JSON.parse(ev[0].data); expect(meta.thread_id && meta.message_id).toBeTruthy();
    expect(ev[ev.length - 1].data).toBe('[DONE]');
  });
  test('stream replay endpoint returns the finished answer', async ({ request }) => {
    const r = await request.post('/v1/chat/completions', { headers: auth(), data: body('Say hi', { stream: true }) });
    const meta = JSON.parse(parseSSE(await r.text())[0].data);
    const s = await request.get(`/v1/threads/${meta.thread_id}/messages/${meta.message_id}/stream?after=0`, { headers: auth() });
    expect(s.status()).toBe(200);
    expect(parseSSE(await s.text()).pop().data).toBe('[DONE]');
  });
  test('cancel is 204 and idempotent; unknown message is 404', async ({ request }) => {
    const r = await request.post('/v1/chat/completions', { headers: auth(), data: body('Write a long essay about rivers', { stream: true, max_tokens: 2000 }) });
    const meta = JSON.parse(parseSSE(await r.text())[0].data);
    const url = `/v1/threads/${meta.thread_id}/messages/${meta.message_id}/cancel`;
    expect((await request.post(url, { headers: auth() })).status()).toBe(204);
    expect((await request.post(url, { headers: auth() })).status()).toBe(204);
    expect((await request.post(`/v1/threads/${meta.thread_id}/messages/01J0000000000000000000000/cancel`, { headers: auth() })).status()).toBe(404);
  });
});
