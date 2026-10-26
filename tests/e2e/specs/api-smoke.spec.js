const { test, expect } = require('@playwright/test');
test.describe('smoke', () => {
  test('/health ok with semver version', async ({ request }) => {
    const r = await request.get('/health'); expect(r.status()).toBe(200);
    const j = await r.json(); expect(j.status).toBe('ok'); expect(j.version).toMatch(/^\d+\.\d+\.\d+/);
  });
  test('/ready reports ready', async ({ request }) => {
    const j = await (await request.get('/ready')).json(); expect(j.ready).toBe(true);
  });
  test('/ui/ serves the app with a version', async ({ request }) => {
    const r = await request.get('/ui/'); expect(r.status()).toBe(200);
    expect(await r.text()).toMatch(/id="appVersion"[^>]*>v\d+\.\d+\.\d+/);
  });
});
