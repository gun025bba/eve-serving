// Memory P1 (GW >= 4.1.0). Profile chosen by backend config on 8002; set EVE_MEMORY_PROFILE=off|on|readonly|auditfail.
// See tests/e2e/docs/memory-p1-test-plan.md.
const { test, expect } = require('@playwright/test');
const { auth, requireKey } = require('./helpers');
// Profiles: off (server off) | on (server on + user on) | useroff (server on + user off) | readonly | auditfail
const P = process.env.EVE_MEMORY_PROFILE || '';
const only = (...ps) => test.skip(!ps.includes(P), `needs 8002 restarted with memory profile ${ps.join('|')} (EVE_MEMORY_PROFILE=${P || 'unset'})`);
const TAG = `qa-mem-${Date.now()}`;

// Gate on the memory routes existing (not on version: 8002 may be built from main without a 4.x version bump).
// Profile 'off' expects them absent; other profiles expect them present. Optional EVE_EXPECT_GW_VERSION pins /health.version.
let routesPresent;
test.beforeEach(async ({ request }) => {
  requireKey();
  test.skip(!P, 'set EVE_MEMORY_PROFILE=off|on|useroff|readonly|auditfail (backend switches 8002 via scripts/qa-mem-mode.sh)');
  if (routesPresent === undefined) {
    const r = await request.get('/v1/memory', { headers: auth() });
    routesPresent = r.status() !== 404;
  }
  if (P !== 'off') test.skip(!routesPresent, 'memory endpoints not present on this gateway (P1 not deployed or memory disabled)');
  // Per-user memory.enabled is set out of band (PATCH /v1/settings is browser-session only since #43):
  //   QA_USER=api-key scripts/qa-mem-mode.sh user-on|user-off   (test DB llmchat_test only)
});
test('gateway version matches EVE_EXPECT_GW_VERSION', async ({ request }) => {
  const want = process.env.EVE_EXPECT_GW_VERSION; test.skip(!want, 'EVE_EXPECT_GW_VERSION not set');
  expect((await (await request.get('/health')).json()).version).toBe(want);
});
const hdr = () => auth();

test.describe('M1 default off', () => {
  for (const [m, p] of [['GET', '/v1/memory'], ['POST', '/v1/memory/items'], ['GET', '/v1/memory/export'], ['DELETE', '/v1/memory']]) {
    test(`${m} ${p} is 404 like an unknown route`, async ({ request }) => {
      only('off');
      const r = await request.fetch(p, { method: m, headers: hdr(), data: m === 'POST' ? { kind: 'fact', text: 'x' } : undefined });
      const u = await request.get('/v1/definitely-not-a-route', { headers: hdr() });
      expect(r.status()).toBe(404); expect((await r.body()).length).toBe((await u.body()).length);
    });
  }
});

test.describe('M15/M21 CRUD (on)', () => {
  test('add → list → patch → delete → delete-all, all no-store', async ({ request }) => {
    only('on');
    const a = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'preference', text: `${TAG} 아침 브리핑은 짧게` } });
    expect(a.status()).toBe(201); expect(a.headers()['cache-control']).toBe('no-store');
    const id = (await a.json()).id; expect(id).toBeTruthy();
    const l = await request.get('/v1/memory', { headers: hdr() });
    expect(l.status()).toBe(200); expect(l.headers()['cache-control']).toBe('no-store'); expect(await l.text()).toContain(TAG);
    const p = await request.patch(`/v1/memory/items/${id}`, { headers: hdr(), data: { pinned: true } });
    expect(p.status()).toBe(200); expect((await p.json()).pinned).toBe(true);
    expect((await request.get('/v1/memory/export', { headers: hdr() })).status()).toBe(200);
    expect((await request.delete(`/v1/memory/items/${id}`, { headers: hdr() })).status()).toBe(204);
    expect((await request.delete(`/v1/memory/items/${id}`, { headers: hdr() })).status()).toBe(404);
    expect((await request.delete('/v1/memory', { headers: hdr() })).status()).toBe(204);
    expect(await (await request.get('/v1/memory', { headers: hdr() })).text()).not.toContain(TAG);
  });
  test('invalid kind / empty text → 400/422 invalid', async ({ request }) => {
    only('on');
    const r = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'fact', text: '' } });
    expect([400, 422]).toContain(r.status()); expect(await r.text()).toContain('invalid');
  });
});

test.describe('M16 secret rejection (on)', () => {
  // Synthetic, non-real secret-shaped strings only.
  for (const t of ['내 비밀번호: hunter22', 'api key sk-TESTTESTTESTTESTTESTTESTTESTTEST', 'https://user:pass@example.invalid/x', 'token 3f9a1c7e5b2d8a4f6c0e9b1d7a3f5c8e2b4d6f0a']) {
    test(`rejects: ${t.slice(0, 20)}…`, async ({ request }) => {
      only('on');
      const r = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'fact', text: t } });
      expect(r.status()).toBe(422); expect(await r.text()).toBe('{"error_code":"secret_detected"}');
      expect(await (await request.get('/v1/memory', { headers: hdr() })).text()).not.toContain(t.slice(-8));
    });
  }
  test('Korean plain sentence is accepted (no false positive)', async ({ request }) => {
    only('on');
    const r = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'fact', text: `${TAG} 고양이를 키운다` } });
    expect(r.status()).toBe(201);
    await request.delete(`/v1/memory/items/${(await r.json()).id}`, { headers: hdr() });
  });
});

test.describe('M18 잊어줘 card (on)', () => {
  test.fixme('forget card: revise with deleted item → 409 item_gone; payload hash binds item ids', async () => {
    // card API comes in a follow-up PR (P1 has no propose wiring). Go TestMemory_Forget*/TestMemoryRevise_* cover logic.
  });
});

// Read-only is reachable only at RUNTIME: start in 'on', create one item, then make <uid>.json 0644 or change its user_id.
// Restarting with an unsafe file (qa-mem-mode.sh readonly) makes the gateway refuse to start instead.
test('M10/M5 read-only: unsafe/mismatched file → 503 memory_read_only, no-store', async ({ request }) => {
  only('readonly');
  for (const [m, p, d] of [['GET', '/v1/memory'], ['POST', '/v1/memory/items', { kind: 'fact', text: `${TAG} x` }]]) {
    const r = await request.fetch(p, { method: m, headers: hdr(), data: d });
    expect(r.status()).toBe(503); expect(r.headers()['cache-control']).toBe('no-store');
    expect(await r.text()).toContain('memory_read_only');
  }
});

// Audit failure is reachable only at RUNTIME (REVOKE INSERT ON tool_audit in the test DB while 'on'); restarting in that state refuses startup.
test('M8 audit write failure → 500 fail-closed, item not stored', async ({ request }) => {
  only('auditfail');
  const r = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'fact', text: `${TAG} audit` } });
  expect(r.status()).toBe(500); expect(await r.text()).toContain('internal_error');
  const l = await request.get('/v1/memory', { headers: hdr() });
  if (l.status() === 200) expect(await l.text()).not.toContain(`${TAG} audit`);
});

// M20 per architect decision: default off; settings expose system.memory_available only
// (read-only is signaled solely by 503 memory_read_only); user off => POST items / propose / approve 403 kill_switch,
// GET / PATCH item / DELETE / export still work. PATCH /v1/settings with Bearer => 403 scope_denied (#43/#52).
// propose and approve need the model / a browser session: Go tests cover them.
test.describe('M20 per-user memory.enabled', () => {
  test('settings: memory_available, no memory_read_only field, Bearer PATCH 403', async ({ request }) => {
    only('on', 'useroff', 'off');
    const r = await request.get('/v1/settings', { headers: hdr() });
    expect(r.status()).toBe(200); expect(r.headers()['cache-control']).toBe('no-store');
    const j = await r.json();
    expect(j.system.memory_available).toBe(P !== 'off');
    expect(j.memory?.enabled ?? false).toBe(P === 'on'); // key may be absent when server off
    expect(JSON.stringify(j)).not.toContain('memory_read_only');
    for (const data of [{ memory: { enabled: P !== 'on' } }, { system: { memory_read_only: true } }]) {
      const p = await request.patch('/v1/settings', { headers: hdr(), data });
      expect(p.status()).toBe(403); expect(await p.text()).toContain('scope_denied');
    }
    expect((await (await request.get('/v1/settings', { headers: hdr() })).json()).memory?.enabled ?? false).toBe(P === 'on');
  });
  test('user off: POST item 403 kill_switch; list/export/patch/delete not kill-switched', async ({ request }) => {
    only('useroff');
    const w = await request.post('/v1/memory/items', { headers: hdr(), data: { kind: 'fact', text: `${TAG} off write` } });
    expect(w.status()).toBe(403); expect(await w.text()).toContain('kill_switch');
    const l = await request.get('/v1/memory', { headers: hdr() });
    expect(l.status()).toBe(200); expect(await l.text()).not.toContain(`${TAG} off write`);
    expect((await request.get('/v1/memory/export', { headers: hdr() })).status()).toBe(200);
    const ghost = '01QAQAQAQAQAQAQAQAQAQAQAQA';
    expect((await request.patch(`/v1/memory/items/${ghost}`, { headers: hdr(), data: { pinned: true } })).status()).toBe(404);
    expect((await request.delete(`/v1/memory/items/${ghost}`, { headers: hdr() })).status()).toBe(404);
  });
});
test('M19 CSRF on memory writes (browser identity)', async () => {
  test.skip(process.env.EVE_BROWSER_IDENTITY !== '1', 'needs browser identity injection — covered by Go TestMemoryAPI_OriginCSRF');
});
