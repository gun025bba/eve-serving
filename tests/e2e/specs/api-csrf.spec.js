const { test, expect } = require('@playwright/test');
const { auth, requireKey, csrf, body } = require('./helpers');
const { BROWSER_IDENTITY } = require('../env');
// CSRF applies to non-API-key identities (browser/Tailscale). Rejections must be 403 {"error_code":"invalid"}.
const cases = [
  ['missing X-Eve-Request', h => { delete h['X-Eve-Request']; }],
  ['missing Origin', h => { delete h.Origin; }],
  ['Origin null', h => { h.Origin = 'null'; }],
  ['foreign Origin', h => { h.Origin = 'https://evil.example'; }],
  ['Sec-Fetch-Site cross-site', h => { h['Sec-Fetch-Site'] = 'cross-site'; }],
  ['text/plain body', h => { h['Content-Type'] = 'text/plain'; }],
];
test.describe('CSRF (browser identity)', () => {
  test.beforeEach(() => test.skip(!BROWSER_IDENTITY, 'needs browser identity injection (EVE_BROWSER_IDENTITY=1) — not available yet'));
  for (const [name, mut] of cases) {
    test(`${name} -> 403`, async ({ request }) => {
      const h = csrf(); mut(h);
      const r = await request.post('/v1/threads', { headers: h, data: JSON.stringify({ title: 'csrf-test' }) });
      expect(r.status()).toBe(403);
      expect(await r.json()).toEqual({ error_code: 'invalid' });
    });
  }
});
test('API key bypasses CSRF even with foreign Origin', async ({ request }) => {
  requireKey();
  const r = await request.post('/v1/chat/completions', { headers: { ...auth(), Origin: 'https://evil.example', 'Sec-Fetch-Site': 'cross-site' }, data: body('ok') });
  expect(r.status()).toBe(200);
});
