'use strict';
// Case 5: rollback chain 5.0.1 -> 5.0.0 -> previous (4.x) -> 5.0.1 again, same shape as qa/cutover-4.0.0 (tests/e2e/docs/cutover-4.0.0.md): binary swap + launchd
// restart, with after EVERY step: the health check READS the running version (authed /whoami == X-Eve-Version, /health is only {"status":"ok"} since 4.0.0),
// goose version checked, QA data intact (marker thread hash, memory hash, paired session still valid, session ids unchanged).
// Mutates ONLY the QA agent (EVE_QA_LABEL, default the QA launchd label) and only with --allow-restart; the original binary is always restored in `finally`.
// Always-on part (no restart): preconditions + marker seed + baseline + plan. Prod (8000) is never touched: its rollback is a paper checklist in docs/release-5.0.md
// because it needs sudo launchctl on /opt/eve-gw (operator hooks only).
const fs = require('fs'), cp = require('child_process'), path = require('path');
const sh = (cmd, o = {}) => cp.spawnSync('bash', ['-c', cmd], { encoding: 'utf8', timeout: o.timeout || 60000 });
const GW_VER = b => { const r = cp.spawnSync(b, ['-version'], { encoding: 'utf8', timeout: 10000 }); return (r.stdout || r.stderr || '').trim().split(/\s+/)[0]; };

module.exports = {
  id: 5, name: 'rollback 5.0.1 -> 5.0.0 -> previous -> 5.0.1 (version via health, goose, data intact)', readonly: false,
  async run(c) {
    const L = c.lib, { req } = L;
    const bins = [['5.0.1', process.env.EVE_BIN_501], ['5.0.0', process.env.EVE_BIN_500], ['previous', process.env.EVE_BIN_PREV || path.join(L.REPO, 'bin', 'gateway-test-4.0.0')]];
    const [b501, b500, bprev] = bins.map(b => b[1]);
    if (!b501 || !b500) { c.skip('rollback chain', 'EVE_BIN_501 / EVE_BIN_500 not set: build them with scripts/release-build.sh (prints the exports); previous defaults to bin/gateway-test-4.0.0'); return planOnly(c); }
    const ver = {};
    for (const [n, b] of bins) {
      if (!fs.existsSync(b)) { c.fail(`binary for ${n} exists`, b); return; }
      ver[n] = GW_VER(b);
    }
    c.check('binary versions are distinct and match their label (5.0.1 / 5.0.0 / 4.x)', ver['5.0.1'].startsWith('5.0.1') && ver['5.0.0'].startsWith('5.0.0') && ver['previous'].startsWith(process.env.EVE_PREV_PREFIX || '4.') && new Set(Object.values(ver)).size === 3, JSON.stringify(ver));
    const p = L.qaPlist();
    if (!p) { c.skip('rollback chain', `QA launchd plist ${L.QA_PLIST} not readable`); return; }
    const target = p.args[0], label = L.QA_LABEL;
    c.check('QA launchd target binary lives under the repo bin/ (never /opt/eve-gw)', path.resolve(target).startsWith(path.join(L.REPO, 'bin') + path.sep), target);
    c.check(`QA instance port ${L.PORT} is a QA port (8002/8003)`, [8002, 8003].includes(Number(L.PORT)) && String(p.env.GW_PORT || '').endsWith(String(L.PORT)), `plist GW_PORT=${p.env.GW_PORT}`);
    const uid = process.getuid();
    const loaded = sh(`launchctl print gui/${uid}/${label} >/dev/null 2>&1; echo $?`).stdout.trim() === '0';
    c.check(`launchd label ${label} is loaded in gui/${uid}`, loaded);
    const v0 = await L.runningVersion().catch(e => ({ ok: false, why: e.message }));
    c.check('QA instance answers authenticated /whoami with a matching X-Eve-Version', v0.ok, v0.why || v0.version);
    const goose0 = L.gooseVersion(); c.info('goose version now', goose0 === null ? 'unreadable (set EVE_GOOSE_CMD)' : goose0);
    if (!v0.ok || !loaded) return;
    // ---- QA marker data (idempotent; removed by nobody: it is QA data and stays as the canary of every run) ----
    const now = new Date('2026-10-28T00:00:00Z').toISOString();
    const marker = { threads: [{ client_id: 'qa-release-rollback-marker', title: 'QA rollback marker', created_at: now, messages: [{ role: 'user', content: '롤백 마커 질문', created_at: now }, { role: 'assistant', content: '롤백 마커 답변 ✓', created_at: now }] }] };
    const imp = await req('POST', '/v1/threads/import', { key: true, csrf: true, body: marker });
    const tid = imp.json && imp.json.results && imp.json.results[0].thread_id;
    if (!c.check('QA marker thread present (import is idempotent)', imp.status === 200 && tid, `${imp.status} ${imp.text.slice(0, 100)}`)) return;
    const snapshot = async () => {
      const t = await req('GET', `/v1/threads/${tid}/messages`, { key: true });
      const out = { marker: L.sha(JSON.stringify((t.json && (t.json.data || t.json.messages) || []).map(m => [m.role, m.content]))), goose: L.gooseVersion() };
      const A = L.stateRead('pairing-A.json');
      if (A) { const v = await L.sessionValid(A.cookie); out.sessionOK = v.status === 200 && v.json.provider === 'session'; const m = await req('GET', '/v1/memory', { cookie: A.cookie }); out.mem = m.status === 200 ? L.sha(JSON.stringify((m.json.items || []).map(i => [i.id, i.text]).sort())) : `status${m.status}`; }
      try { out.sess = L.sessionsList().map(s => `${s.id}:${s.state}`).join(','); } catch { out.sess = null; }
      return out;
    };
    const base = await snapshot(); c.pass('baseline snapshot (marker hash, goose, session, memory hash)', JSON.stringify({ ...base, marker: base.marker.slice(0, 8), mem: base.mem && base.mem.slice(0, 8), sess: base.sess ? base.sess.split(',').length + ' sessions' : null }));
    const plan = `${ver['5.0.1']} -> ${ver['5.0.0']} -> ${ver['previous']} -> ${ver['5.0.1']}, restore ${v0.version}`;
    if (!c.opts.allowRestart) { c.skip('swap + restart chain', `plan: ${plan}. Re-run with --allow-restart to execute on the QA agent ${label} (port ${L.PORT}).`); return; }
    // ---- execute ----
    const backup = `${target}.bak-release-${Date.now()}`; fs.copyFileSync(target, backup); fs.chmodSync(backup, 0o755);
    const kick = () => sh(`launchctl kickstart -k gui/${uid}/${label}`);
    const waitVersion = async want => { const t0 = Date.now(); let last; while (Date.now() - t0 < 60000) { await L.sleep(1000); last = await L.runningVersion().catch(e => ({ ok: false, why: e.message })); if (last.ok && last.version === want) return last; } return last; };
    const step = async (label2, bin, want) => {
      fs.copyFileSync(bin, target + '.new'); fs.chmodSync(target + '.new', 0o755); fs.renameSync(target + '.new', target);
      kick();
      const v = await waitVersion(want);
      c.check(`[${label2}] health check reads running version == ${want}`, v && v.ok && v.version === want, v && (v.version || v.why));
      const h = await req('GET', '/health'); c.check(`[${label2}] /health 200 {"status":"ok"}`, h.status === 200 && h.json && h.json.status === 'ok', h.text.slice(0, 80));
      const s = await snapshot();
      c.check(`[${label2}] goose version unchanged (${base.goose})`, s.goose === base.goose || base.goose === null, `${base.goose} -> ${s.goose}`);
      c.check(`[${label2}] marker thread content identical`, s.marker === base.marker);
      if (base.sessionOK !== undefined) c.check(`[${label2}] paired QA session still valid (no re-pair)`, s.sessionOK === true);
      if (base.mem !== undefined) c.check(`[${label2}] user memory identical`, s.mem === base.mem, `${base.mem} -> ${s.mem}`);
      if (base.sess) c.check(`[${label2}] session table identical (ids + states)`, s.sess === base.sess);
      const un = await req('GET', '/whoami'); c.check(`[${label2}] unauthenticated whoami still 401`, un.status === 401, un.status);
      const ch = await req('POST', '/v1/chat/completions', { key: true, csrf: true, timeout: 120000, body: { model: await L.model(), stream: false, max_tokens: 8, web_search: 'off', messages: [{ role: 'user', content: 'ping /no_think' }] } });
      c.check(`[${label2}] chat smoke (8 tokens) 200`, ch.status === 200, ch.status);
    };
    try {
      await step('5.0.1 (start)', b501, ver['5.0.1']);
      await step('rollback -> 5.0.0', b500, ver['5.0.0']);
      await step(`rollback -> previous ${ver.previous}`, bprev, ver.previous);
      await step('roll forward -> 5.0.1', b501, ver['5.0.1']);
    } finally {
      fs.copyFileSync(backup, target + '.new'); fs.chmodSync(target + '.new', 0o755); fs.renameSync(target + '.new', target); kick();
      const v = await waitVersion(v0.version);
      c.check(`QA agent restored to its original version ${v0.version}`, v && v.ok && v.version === v0.version, v && (v.version || v.why));
    }
    c.info('backup of the original binary kept at', backup);
  },
};

function planOnly(c) {
  c.info('plan', 'swap target binary -> launchctl kickstart -k -> poll /whoami until X-Eve-Version == expected -> compare /health, goose, marker thread, session, memory, session table -> next step; finally restore');
  c.info('prod', 'rollback on 8000 is NOT executed by this bundle; see docs/release-5.0.md section "Prod rollback checklist"');
}
