// 4.0.0 auth boundary (PR #23): on TCP every Tailscale-User-* header is stripped; only the API key authenticates.
// Run only against a 4.0.0+ build: EVE_EXPECT_AUTH_BOUNDARY=1. EVE_TS_LOGIN = an allowlisted login on that gateway
// (synthetic on 8002). See tests/e2e/docs/cutover-4.0.0.md.
const { test, expect } = require('@playwright/test');
const { auth, requireKey } = require('./helpers');
const LOGIN = process.env.EVE_TS_LOGIN || 'qa-user@example.invalid';
test.beforeEach(() => test.skip(!process.env.EVE_EXPECT_AUTH_BOUNDARY, 'set EVE_EXPECT_AUTH_BOUNDARY=1 on a 4.0.0+ gateway'));

test('TCP: identity header alone is 401', async ({ request }) => {
  const r = await request.get('/whoami', { headers: { 'Tailscale-User-Login': LOGIN, 'Tailscale-User-Name': 'x' } });
  expect(r.status()).toBe(401);
});
test('TCP: identity header plus key resolves to api-key, not the header login', async ({ request }) => {
  requireKey();
  const r = await request.get('/whoami', { headers: { ...auth(), 'Tailscale-User-Login': LOGIN } });
  expect(r.status()).toBe(200);
  expect((await r.json()).user).toBe('api-key');
});
test('TCP: any-case / extra Tailscale-User-* headers do not grant identity', async ({ request }) => {
  const r = await request.post('/v1/chat/completions', { headers: { 'tailscale-user-login': LOGIN, 'TAILSCALE-USER-PROFILE-PIC': 'x',
    'X-Eve-Request': '1', 'Content-Type': 'application/json' }, data: { model: 'x', messages: [] } });
  expect(r.status()).toBe(401);
});
test('default build ignores X-Eve-Test-Identity', async ({ request }) => {
  requireKey(); test.skip(!!process.env.EVE_E2E_BUILD, 'e2e build injects identity by design');
  const r = await request.get('/whoami', { headers: { ...auth(), 'X-Eve-Test-Identity': LOGIN } });
  expect((await r.json()).user).toBe('api-key');
});
