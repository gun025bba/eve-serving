const { test, expect } = require('@playwright/test');
const { auth, requireKey } = require('./helpers');
test.beforeEach(() => requireKey());
test('/v1/search returns {data:[...]} shape', async ({ request }) => {
  const r = await request.get('/v1/search?q=hello', { headers: auth() });
  expect(r.status()).toBe(200);
  const j = await r.json(); expect(Array.isArray(j.data)).toBe(true);
  for (const h of j.data) expect(h).toHaveProperty('thread_id');
});
test('proactive /v1/actions lists', async ({ request }) => {
  const r = await request.get('/v1/actions', { headers: auth() });
  expect(r.status()).toBe(200);
  const j = await r.json(); expect(Array.isArray(j.data ?? j)).toBe(true);
});
test('push VAPID key endpoint responds', async ({ request }) => {
  const r = await request.get('/v1/push/vapid-public-key', { headers: auth() });
  expect([200, 404, 501]).toContain(r.status());
});
