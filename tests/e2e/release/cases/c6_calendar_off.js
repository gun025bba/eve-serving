'use strict';
// Case 6: calendar adapter (eve-pim helper + gateway pim tools, Jarvis M1 PRs #75/#76/#78/#77) is OFF.
// Flags (feat/jarvis-m1-*): GW_PIM_ENABLED (default false), GW_PIM_WRITES_ENABLED, GW_BRIEFING_ENABLED, GW_PIM_SOCKET_PATH, GW_PIM_OWNER_USER_ID.
// Routes: the only new HTTP route is POST /v1/briefing/preview (interactive scope; 404 when briefing is off). The tools are chat tool-loop tools
// (calendar_list_events, reminders_list, + write verb), not routes. ASSUMPTION: release/5.0.x binaries contain no pim code at all; if they do, flags must be off.
const fs = require('fs'), cp = require('child_process');
const { goTests } = require('../gotest');
const TOOLS = ['calendar_list_events', 'reminders_list', 'calendar_add_event', 'reminders_add'];
module.exports = {
  id: 6, name: 'calendar adapter off: no endpoints, not configured, nothing calls it', readonly: true,
  async run(c) {
    const { req, qaPlist } = c.lib;
    // 1. HTTP surface. Key-only probes can only prove 404 or "exists but scope-gated"; a session cookie (case 1 state) is definitive.
    const st = c.lib.stateRead('pairing-A.json');
    const probes = [['GET', '/v1/calendar'], ['GET', '/v1/calendar/events'], ['GET', '/v1/pim'], ['GET', '/v1/pim/calendars'], ['GET', '/v1/reminders'], ['GET', '/v1/briefing']];
    if (!c.lib.RO()) probes.push(['POST', '/v1/briefing/preview']);
    for (const [m, p] of probes) {
      const r = await req(m, p, { key: c.lib.apiKey() ? true : undefined, csrf: m !== 'GET', body: m === 'POST' ? {} : undefined });
      if (r.status === 404) c.pass(`${m} ${p} -> 404`);
      else if (r.status === 401 && !c.lib.apiKey()) c.skip(`${m} ${p}`, 'no API key to probe with (got 401)');
      else if (r.status === 403) c.warn(`${m} ${p} -> 403 scope_denied for the API key (route exists, gated by scope)`, 'needs a browser session to prove it is disabled; see session probe below');
      else c.fail(`${m} ${p} must be 404 while the adapter is off`, `status ${r.status}`);
    }
    if (st && !c.lib.RO()) {
      const r = await req('POST', '/v1/briefing/preview', { cookie: st.cookie, body: {} });
      c.check('session: POST /v1/briefing/preview -> 404 (briefing off)', r.status === 404, `status ${r.status}`);
    } else c.skip('session probe of /v1/briefing/preview', 'no QA session (run case 1 --phase pre first) or prod-ro');
    const oa = await req('GET', '/openapi.json', { key: c.lib.apiKey() ? true : undefined });
    if (oa.status === 200) c.check('openapi.json lists no calendar/reminders/briefing path', !/calendar|reminders|briefing|\/pim/i.test(oa.text), 'found a pim-related path');
    // 2. Configuration of the running instance (QA: launchd plist; prod: set EVE_PIM_ENV_CMD to print the gateway env, e.g. via sudo plutil -p)
    let envText = '';
    if (process.env.EVE_PIM_ENV_CMD) envText = cp.spawnSync('bash', ['-c', process.env.EVE_PIM_ENV_CMD], { encoding: 'utf8' }).stdout || '';
    else if (qaPlist() && !c.lib.RO()) envText = Object.entries(qaPlist().env).map(([k, v]) => `${k}=${v}`).join('\n');
    if (envText) {
      const on = [...envText.matchAll(/GW_(PIM_ENABLED|PIM_WRITES_ENABLED|BRIEFING_ENABLED)\W+(\S+)/g)].filter(m => /^(1|true|yes|on)$/i.test(m[2].replace(/["',]/g, '')));
      c.check('no GW_PIM_ENABLED / GW_PIM_WRITES_ENABLED / GW_BRIEFING_ENABLED set to true in the gateway env', on.length === 0, on.map(m => m[1]).join(','));
    } else c.skip('gateway env flags', 'prod env not readable without sudo: set EVE_PIM_ENV_CMD (see doc) or check by hand');
    // 3. Helper process / socket not present
    const lc = cp.spawnSync('bash', ['-c', 'launchctl list 2>/dev/null | grep -c "com.eve.pim" || true'], { encoding: 'utf8' }).stdout.trim();
    c.check('launchd agent com.eve.pim is not loaded (user domain)', lc === '0', `matches=${lc}`);
    const ps = cp.spawnSync('bash', ['-c', 'pgrep -fl "eve-pim" | grep -v pgrep || true'], { encoding: 'utf8' }).stdout.trim();
    c.check('no eve-pim helper process running', ps === '', ps.split('\n')[0]);
    const sock = process.env.EVE_PIM_SOCKET || '';
    if (sock) c.check(`pim socket ${sock} does not exist`, !fs.existsSync(sock));
    // 4. Binaries: no pim code in the release builds (or flags off, checked above)
    for (const [label, env] of [['5.0.0', 'EVE_BIN_500'], ['5.0.1', 'EVE_BIN_501'], ['running QA', 'EVE_GW_CLI']]) {
      const f = process.env[env] || (env === 'EVE_GW_CLI' && !c.lib.RO() ? c.lib.GW_CLI : '');
      if (!f || !fs.existsSync(f)) { c.skip(`binary ${label}: pim symbols`, `${env} not set / missing`); continue; }
      const n = cp.spawnSync('bash', ['-c', `strings -a "${f}" | grep -c -E "${TOOLS.slice(0, 2).join('|')}|GW_PIM_ENABLED" || true`], { encoding: 'utf8' }).stdout.trim();
      if (n === '0') c.pass(`binary ${label} contains no pim tool/flag strings`);
      else c.warn(`binary ${label} contains pim code (${n} string hits)`, 'allowed only if the flags are off (checked above) and no helper runs');
    }
    // 5. Nothing calls it: gateway log since start has no pim activity (QA log; prod: EVE_GW_LOG)
    const log = process.env.EVE_GW_LOG || (c.lib.RO() ? '' : require('path').join(c.lib.REPO, 'gateway-test.log'));
    if (log && fs.existsSync(log)) {
      const tail = cp.spawnSync('bash', ['-c', `tail -n 20000 "${log}" | grep -c -i -E "pim_|eve-pim|calendar_list_events|reminders_list|briefing" || true`], { encoding: 'utf8' }).stdout.trim();
      c.check('gateway log (last 20000 lines) has no pim/briefing activity', tail === '0', `${tail} matching lines`);
    } else c.skip('gateway log has no pim activity', 'EVE_GW_LOG not set / log unreadable');
    // UI side: 1.9.0 has no calendar cards (they arrive with 1.10.0, not tonight)
    const app = await req('GET', '/ui/app.js');
    if (app.status === 200) c.check('deployed UI has no calendar/briefing card code (1.10.0 is not part of tonight)', !/calendar_list_events|calendar-card|briefing-card|reminders_list/i.test(app.text), 'UI contains calendar card code');
    // 6. optional: a real prompt must not produce a pim tool call (light MLX load: one short request, QA only)
    if (process.env.EVE_CASE6_CHAT === '1' && !c.lib.RO() && c.lib.apiKey()) {
      const body = { model: await L.model(), stream: true, max_tokens: 48, web_search: 'off', thread_id: 'new', messages: [{ role: 'user', content: '내일 내 캘린더 일정 알려줘. /no_think' }] };
      const r = await req('POST', '/v1/chat/completions', { key: true, csrf: true, body, timeout: 120000 });
      c.check('chat "calendar?" prompt: no pim tool_call event', r.status === 200 && !TOOLS.some(t => r.text.includes(t)), `status ${r.status}`);
    } else c.info('chat probe skipped', 'set EVE_CASE6_CHAT=1 (QA only, one short request)');
    goTests(c, { ref: process.env.EVE_REF_501 || 'origin/release/5.0.1', run: 'TestRouteAuth|TestPim|TestConfig', pkgs: ['./internal/handlers/', './internal/config/'], label: 'route/auth matrix + config defaults on the release ref' });
  },
};
