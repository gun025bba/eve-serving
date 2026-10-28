'use strict';
// Case 1: pairing + browser sessions survive the upgrade (4.0.0 model: eve_session cookie, `sessions` table, HMAC with the session pepper).
// How pairing works (release/5.0.1): host CLI `gateway|eve-gw pair --user <login>` prints an 8-char one-time code (10 min); the browser POSTs it to
// POST /v1/pair (unauthenticated, CSRF headers needed) -> 204 + Set-Cookie eve_session. Sessions: 30 d idle / 180 d absolute; 5 wrong codes lock pairing 10 min
// (this case never sends a wrong code). 5.0.0/5.0.1 add NO migration; the pepper file and `sessions` table are unchanged, so no re-pairing is expected.
// Run `--phase pre` before the deploy and `--phase post` after. QA: uses a QA-only login. Prod: only the read-only `sessions list` hook (EVE_SESSIONS_CMD).
const { goTests } = require('../gotest');
const LOGIN_A = process.env.EVE_QA_LOGIN_A || 'qa-a@test.invalid';
const active = s => s.filter(x => x.state === 'active');
const index = s => Object.fromEntries(s.map(x => [x.id, x]));

module.exports = {
  id: 1, name: 'pairing and sessions survive the upgrade (no re-pairing)', readonly: true, needsDevice: true,
  async run(c) {
    const L = c.lib, { req } = L, phase = c.opts.phase, qa = !L.RO();
    let sessions = null;
    if (!process.env.EVE_SESSIONS_CMD && !L.cliSupportsPair() ) c.skip('session snapshot', `${L.GW_CLI} predates pairing (no pair/sessions subcommands) and EVE_SESSIONS_CMD is not set`);
    else { try { sessions = L.sessionsList(); } catch (e) { c.skip('session snapshot', e.message); } }
    const goose = L.gooseVersion(), ver = await L.runningVersion().catch(e => ({ ok: false, why: String(e.message) }));
    const pre = L.stateRead('pairing-pre.json');

    if (phase === 'pre' || phase === 'all') {
      const snap = { at: new Date().toISOString(), version: ver.version || null, goose, sessions };
      if (sessions) c.pass('pre: sessions snapshot taken', `${sessions.length} sessions, ${active(sessions).length} active, goose=${goose}, running=${ver.version}`);
      if (qa && sessions && L.cliSupportsPair()) {
        let st = L.stateRead('pairing-A.json'), ok = false;
        if (st) { const v = await L.sessionValid(st.cookie); ok = v.status === 200 && v.json && v.json.provider === 'session'; }
        if (!ok) {
          try { const cookie = await L.pairQA(LOGIN_A); st = { login: LOGIN_A, cookie, at: new Date().toISOString() }; L.stateWrite('pairing-A.json', st); ok = true; c.pass(`pre: paired a new QA browser session for ${LOGIN_A} (cookie kept in .release-state, 0600)`); }
          catch (e) { c.fail(`pre: pair a QA session for ${LOGIN_A}`, e.message + ' — is the login in allowed_users of the QA instance?'); }
        } else c.pass('pre: existing QA session is valid and reused');
        if (ok) { const v = await L.sessionValid(st.cookie); snap.qaSession = { login: st.login, user: v.json && v.json.user }; c.check('pre: QA session authenticates as the paired login (provider session)', v.status === 200 && v.json.provider === 'session', JSON.stringify(v.json)); }
      }
      L.stateWrite('pairing-pre.json', snap);
      if (phase === 'pre') { c.device('iPhone: note that the home-screen PWA is signed in (opens straight to the conversation list)', 'after the deploy it must open without the pairing screen'); return; }
    }
    // ----- post -----
    const base = phase === 'all' ? L.stateRead('pairing-pre.json') : pre;
    if (!base) c.warn('no pre snapshot found (.release-state/pairing-pre.json): comparing nothing, only post checks', 'run --phase pre before the deploy');
    if (ver.ok) c.pass('post: running version read from authenticated /whoami == X-Eve-Version', ver.version); else c.fail('post: running version readable', ver.why);
    if (process.env.EVE_EXPECT_GW_VERSION) c.check(`post: running version == ${process.env.EVE_EXPECT_GW_VERSION}`, ver.version === process.env.EVE_EXPECT_GW_VERSION, ver.version);
    const un = await req('GET', '/whoami');
    c.check('unauthenticated /whoami is 401 (auth boundary intact)', un.status === 401, un.status);
    if (sessions && base && base.sessions) {
      const was = index(base.sessions), now = index(sessions);
      const lost = active(base.sessions).filter(s => !now[s.id] || now[s.id].state !== 'active');
      c.check('every session active before the deploy is still active (none revoked/expired/missing)', lost.length === 0, 'lost ids: ' + lost.map(s => s.id).join(','));
      c.check('no session was rewritten to another user', active(base.sessions).every(s => !now[s.id] || now[s.id].user === s.user));
      c.check('active session count did not drop', active(sessions).length >= active(base.sessions).length, `${active(base.sessions).length} -> ${active(sessions).length}`);
      void was;
    }
    if (goose !== null && base && base.goose !== null) c.check('goose version unchanged by the upgrade (5.0.x has no migration)', goose === base.goose, `${base.goose} -> ${goose}`);
    else c.info('goose version', goose === null ? 'not readable (set EVE_GOOSE_CMD on prod)' : `${goose}`);
    const st = L.stateRead('pairing-A.json');
    if (qa && st) {
      const v = await L.sessionValid(st.cookie);
      c.check('QA browser session from before the deploy still works WITHOUT re-pairing (whoami 200, provider session)', v.status === 200 && v.json.provider === 'session' && v.json.authenticated, `status ${v.status} ${JSON.stringify(v.json)}`);
      for (const p of ['/v1/settings', '/v1/threads?limit=1']) { const r = await req('GET', p, { cookie: st.cookie }); c.check(`session GET ${p} -> 200`, r.status === 200, r.status); }
      const x = await req('GET', '/v1/actions', { cookie: st.cookie }); c.check('session reaches interactive-only route GET /v1/actions (scope intact)', x.status === 200 || x.status === 404, x.status);
      const k = await req('GET', '/v1/actions', { key: true }); if (L.apiKey()) c.check('API key still gets 403 scope_denied on the same route', k.status === 403, k.status);
      // new pairing works too (QA only; revokes the extra session afterwards)
      if (sessions && L.cliSupportsPair()) {
        const before = new Set(L.sessionsList().map(s => s.id));
        try {
          const cookie = await L.pairQA(LOGIN_A); const v2 = await L.sessionValid(cookie);
          c.check('a fresh pairing code still redeems to a working session', v2.status === 200 && v2.json.provider === 'session', v2.status);
          const extra = L.sessionsList().filter(s => !before.has(s.id)).map(s => s.id);
          for (const id of extra) L.cli(['sessions', 'revoke', id]);
          c.check('the extra QA session was revoked afterwards and no longer works', extra.length >= 1 && (await L.sessionValid(cookie)).status === 401, `extra=${extra.join(',')}`);
        } catch (e) { c.fail('fresh pairing', e.message); }
      }
      // UI: no pairing screen with the old cookie
      if (process.env.EVE_UI_DIR) c.skip('UI shows the list, not the pairing screen', 'EVE_UI_DIR mode has no backend');
      else {
        const { openPage, uiBase } = require('../ui'); const ui = await uiBase();
        try {
          const s = await openPage({ engine: 'webkit', cookie: st.cookie, base: ui.base });
          try {
            const state = await s.page.evaluate(() => ({ pair: !!document.querySelector('#pairForm') && !document.querySelector('#pairForm').closest('[hidden]') && !document.querySelector('#pairForm').hidden, list: !!document.querySelector('#listView') && document.querySelector('#listView').offsetParent !== null }));
            c.check('UI with the pre-deploy session cookie opens the conversation list, pairing form hidden', !state.pair && state.list, JSON.stringify(state));
          } finally { await s.close(); }
        } catch (e) { c.fail('UI open with session cookie', String(e.message).split('\n')[0]); }
        ui.close();
      }
    } else if (qa) c.skip('QA session checks', 'no .release-state/pairing-A.json (run --phase pre first) or no pairing-capable CLI');
    goTests(c, { ref: process.env.EVE_REF_501 || 'origin/release/5.0.1', run: 'TestPair|TestSession|TestLogout', pkgs: ['./internal/handlers/', './internal/session/...'], label: 'pairing + session unit tests on the release ref' });
    c.device('iPhone home-screen PWA (건영 phone): open after the deploy', 'opens straight to the conversation list; no pairing screen; a message sends. If it shows the pairing screen, STOP and report (rollback criterion).');
  },
};
