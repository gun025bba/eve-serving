'use strict';
// Case 6: calendar adapter (eve-pim helper + gateway pim tools, Jarvis M1 PRs #75-#78 + fix/pim-review-lows).
// EVE_CALENDAR_MODE=read-only (default since the 2026-10-28 22:03 KST deploy) | off (the pre-deploy expectation: nothing enabled).
//  read-only = GW_PIM_ENABLED may be true; GW_PIM_WRITES_ENABLED must NOT be true; no write/mutation path may be reachable; reads must behave.
// Flags: GW_PIM_ENABLED, GW_PIM_WRITES_ENABLED (default off), GW_BRIEFING_ENABLED. Read tools: calendar_list_events, reminders_list, calendar_list_calendars.
// Write verbs (only offered with writes on, and executed only through an approve card): calendar_add_event, reminders_add, reminders_complete.
// Mutation HTTP paths: POST /v1/actions/{id}/approve|reject|reissue (card execution), POST /v1/briefing/preview (briefing, generates a message).
// On prod-ro only GET/HEAD is sent: a write/mutation route must answer a non-2xx to GET (404/405/401/403); the real POST rejection is proven by the Go tests
// (TestPimWrite_OffByDefault, _FlagOffAfterProposalBlocksApprove, _NonOwnerHiddenAndDenied) and, on QA, by live POSTs with fake ids (nothing is created).
const fs = require('fs'), cp = require('child_process');
const { goTests } = require('../gotest');
const WRITE_TOOLS = ['calendar_add_event', 'reminders_add', 'reminders_complete'];
const READ_TOOLS = ['calendar_list_events', 'reminders_list', 'calendar_list_calendars'];
const REJECT = new Set([401, 403, 404, 405]);
const sh = cmd => cp.spawnSync('bash', ['-c', cmd], { encoding: 'utf8', timeout: 20000 });
const on = v => /^(1|true|yes|on)$/i.test(String(v).replace(/["',]/g, ''));

module.exports = {
  id: 6, name: 'calendar adapter: read-only (writes/mutations rejected, reads behave) or off', readonly: true,
  async run(c) {
    const L = c.lib, { req } = L, MODE = (process.env.EVE_CALENDAR_MODE || 'read-only').toLowerCase(), RO = L.RO();
    c.info('mode', `${MODE} (set EVE_CALENDAR_MODE=off for "nothing enabled")`);
    // 1. Flags of the running gateway: QA plist, or prod via EVE_PIM_ENV_CMD, or (prod, read-only) `launchctl print` filtered to GW_PIM/GW_BRIEFING names
    let envText = '';
    if (process.env.EVE_PIM_ENV_CMD) envText = sh(process.env.EVE_PIM_ENV_CMD).stdout || '';
    else if (!RO && L.qaPlist()) envText = Object.entries(L.qaPlist().env).map(([k, v]) => `${k}=${v}`).join('\n');
    else { const r = sh("launchctl print system/com.llmserving.gateway 2>/dev/null | grep -E 'GW_(PIM|BRIEFING)[A-Z_]* *=>' || true"); envText = (r.stdout || '').replace(/=>/g, '='); if (envText) c.info('flags read via launchctl print (names GW_PIM*/GW_BRIEFING* only)'); }
    const flag = n => { const m = new RegExp(`GW_${n}\\W+(\\S+)`).exec(envText); return m ? on(m[1]) : null; };
    let pimOn = null, writesOn = null;
    if (envText || !RO) {
      pimOn = flag('PIM_ENABLED'); writesOn = flag('PIM_WRITES_ENABLED'); const brOn = flag('BRIEFING_ENABLED');
      c.info('flags', `PIM_ENABLED=${pimOn === null ? 'unset(off)' : pimOn} PIM_WRITES_ENABLED=${writesOn === null ? 'unset(off)' : writesOn} BRIEFING_ENABLED=${brOn === null ? 'unset(off)' : brOn}`);
      c.check('GW_PIM_WRITES_ENABLED is not true (no write verb offered or executable)', !writesOn);
      if (MODE === 'off') c.check('GW_PIM_ENABLED / GW_BRIEFING_ENABLED are not true (adapter off)', !pimOn && !brOn);
      else if (pimOn) c.pass('GW_PIM_ENABLED=true (read-only mode as announced)');
      else c.warn('GW_PIM_ENABLED is not true on this gateway', 'announced as read-only; either the flag is set another way (config.yaml) or this build has no pim code');
    } else c.skip('gateway flags', 'not readable without sudo: set EVE_PIM_ENV_CMD (see doc) or check GW_PIM_WRITES_ENABLED by hand');
    // 2. HTTP surface. Unauthenticated: 401 everywhere is expected and proves nothing about routes, so use the key/session when we have one.
    const st = RO ? null : L.stateRead('pairing-A.json'), key = !!L.apiKey();
    const auth = st ? { cookie: st.cookie } : key ? { key: true } : {};
    const who = st ? 'session' : key ? 'api key' : 'unauthenticated';
    const mutPaths = ['/v1/actions/qa-nonexistent/approve', '/v1/actions/qa-nonexistent/reject', '/v1/actions/qa-nonexistent/reissue', '/v1/briefing/preview'];
    const absent = ['/v1/calendar', '/v1/calendar/events', '/v1/pim', '/v1/pim/calendars', '/v1/reminders', '/v1/briefing'];
    for (const p of absent) {
      const r = await req('GET', p, auth);
      c.check(`GET ${p} (${who}) is not a 2xx/5xx (no calendar endpoint exposes data)`, REJECT.has(r.status), r.status);
    }
    for (const p of mutPaths) {
      const r = await req('GET', p, auth);
      c.check(`mutation path GET ${p} (${who}) rejected, not 2xx`, REJECT.has(r.status), r.status);
    }
    if (!RO) {   // QA only: live POST attempts with fake ids; nothing can be created
      for (const p of mutPaths) {
        const r = await req('POST', p, { ...auth, csrf: true, body: p.includes('briefing') ? {} : undefined });
        const okc = st ? [404, 409, 410, 422, 403, 400].includes(r.status) : [401, 403, 404].includes(r.status);
        c.check(`POST ${p} (${who}, fake id) rejected without effect`, okc && r.status < 500, r.status + ' ' + r.text.slice(0, 80));
      }
    } else c.info('POST attempts not sent on prod-ro', 'rejection of writes is proven by the Go tests below + GET-method probes above');
    // reads behave: the read-only scope boundary stays intact
    if (st) { const a = await req('GET', '/v1/actions', { cookie: st.cookie }); c.check('read: session GET /v1/actions -> 200 (pending-action list, read-only)', a.status === 200, a.status); }
    if (key) { const a = await req('GET', '/v1/actions', { key: true }); c.check('API key GET /v1/actions -> 403 scope_denied (interactive-only)', a.status === 403 || a.status === 404, a.status); }
    const w = await req('GET', '/whoami', key ? { key: true } : {});
    if (w.status === 200 && w.json) { const caps = JSON.stringify(w.json.capabilities || {}); c.info('whoami capabilities', caps.slice(0, 160)); if (WRITE_TOOLS.some(t => caps.includes(t))) c.fail('whoami advertises a write tool', caps); }
    const oa = await req('GET', '/openapi.json', auth);
    if (oa.status === 200) c.check('openapi.json lists no calendar/reminders write path', !/calendar|reminders|\/pim/i.test(oa.text), 'pim path in openapi');
    // 3. Helper process / socket (read-only: helper may run; it must be the only thing that talks to EventKit)
    const lc = sh('launchctl list 2>/dev/null | grep -c "com.eve.pim" || true').stdout.trim();
    const ps = sh('pgrep -fl "eve-pim" | grep -v pgrep || true').stdout.trim();
    if (MODE === 'off') { c.check('launchd agent com.eve.pim is not loaded', lc === '0', `matches=${lc}`); c.check('no eve-pim helper process running', ps === '', ps.split('\n')[0]); }
    else c.info('eve-pim helper', `${lc !== '0' || ps ? 'running' : 'not running on this host'} (read-only mode allows it)`);
    if (process.env.EVE_PIM_SOCKET) { const s = process.env.EVE_PIM_SOCKET; if (fs.existsSync(s)) { const m = (fs.statSync(s).mode & 0o777).toString(8); c.check(`pim socket ${s} has no world access (mode ${m}; group _evepim is the intended gateway path)`, (parseInt(m, 8) & 0o007) === 0, m); } else c.info('pim socket not present', s); }
    // 4. Binaries
    for (const [label, env] of [['5.0.0', 'EVE_BIN_500'], ['5.0.1', 'EVE_BIN_501'], ['running QA', 'EVE_GW_CLI']]) {
      const f = process.env[env] || (env === 'EVE_GW_CLI' && !RO ? L.GW_CLI : '');
      if (!f || !fs.existsSync(f)) { c.skip(`binary ${label}: pim symbols`, `${env} not set / missing`); continue; }
      const n = sh(`strings -a "${f}" | grep -c -E "${WRITE_TOOLS.concat(READ_TOOLS.slice(0, 2)).join('|')}|GW_PIM_ENABLED" || true`).stdout.trim();
      if (n === '0') c.pass(`binary ${label} contains no pim tool/flag strings`); else c.info(`binary ${label} contains pim code (${n} string hits)`, MODE === 'off' ? 'allowed only while flags are off' : 'read-only mode: fine as long as GW_PIM_WRITES_ENABLED is not true');
    }
    // 5. Nothing WRITES: log has no write-verb activity (reads are fine). QA log by default; prod: EVE_GW_LOG
    const log = process.env.EVE_GW_LOG || (RO ? '' : require('path').join(L.REPO, 'gateway-test.log'));
    if (log && fs.existsSync(log)) {
      const pat = MODE === 'off' ? 'pim_|eve-pim|calendar_|reminders_|briefing' : WRITE_TOOLS.join('|') + '|pim_write';
      const n = sh(`tail -n 20000 "${log}" | grep -c -i -E "${pat}" || true`).stdout.trim();
      c.check(`gateway log (last 20000 lines) has no ${MODE === 'off' ? 'pim/briefing' : 'calendar/reminder write'} activity`, n === '0', `${n} matching lines`);
    } else c.skip('gateway log write-activity scan', 'EVE_GW_LOG not set / log unreadable (prod log is root-owned)');
    // 6. UI side: 1.9.0 has no calendar cards (they arrive with 1.10.0)
    const app = await req('GET', '/ui/app.js');
    if (app.status === 200) c.check('deployed UI has no calendar/briefing card code (1.10.0 is not part of this release)', !/calendar_list_events|calendar-card|briefing-card|reminders_list/i.test(app.text), 'UI contains calendar card code');
    // 7. optional live chat probe (QA only; never prod): asking for a calendar write must not produce a write tool call
    if (process.env.EVE_CASE6_CHAT === '1' && !RO && key) {
      const body = { model: await L.model(), stream: true, max_tokens: 64, web_search: 'off', thread_id: 'new', messages: [{ role: 'user', content: '내일 오후 3시에 "QA 테스트" 일정을 내 캘린더에 추가해줘. /no_think' }] };
      const r = await req('POST', '/v1/chat/completions', { key: true, csrf: true, body, timeout: 120000 });
      c.check('chat "add a calendar event" prompt: no write tool_call event', r.status === 200 && !WRITE_TOOLS.some(t => r.text.includes(t)), `status ${r.status}`);
    } else c.info('chat probe skipped', 'EVE_CASE6_CHAT=1, QA only');
    goTests(c, { ref: process.env.EVE_REF_PIM || 'origin/fix/pim-review-lows', run: 'TestPimTools_OffByDefault|TestPimWrite_OffByDefault|TestPimWrite_FlagOffAfterProposalBlocksApprove|TestPimWrite_NonOwnerHiddenAndDenied|TestPimWritesConfig|TestBriefingPreview_OffIs404', pkgs: ['./internal/handlers/', './internal/config/'], label: 'pim write gating (off by default, flag-off blocks approve, non-owner denied) on the pim ref' });
  },
};
