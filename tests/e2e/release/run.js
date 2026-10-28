'use strict';
// Post-deploy bundle for the 5.0.x release (gateway 5.0.0 -> 5.0.1, chat-ui 1.9.0). See ../docs/release-5.0.md.
const fs = require('fs'), path = require('path');
const lib = require('./lib');

const CASES = [
  require('./cases/c1_pairing'), require('./cases/c2_memory_isolation'), require('./cases/c3_root_redirect'),
  require('./cases/c4_source_chips'), require('./cases/c5_rollback'), require('./cases/c6_calendar_off'), require('./cases/c7_ui_regressions'),
];

function parseArgs(a) {
  const o = { cases: null, phase: 'all', list: false, strict: false, go: true, allowRestart: false, json: '' };
  for (let i = 0; i < a.length; i++) {
    const k = a[i];
    if (k === '--case' || k === '--cases') o.cases = a[++i].split(',').map(s => parseInt(s, 10));
    else if (k === '--phase') o.phase = a[++i];
    else if (k === '--list') o.list = true;
    else if (k === '--strict') o.strict = true;
    else if (k === '--no-go') o.go = false;
    else if (k === '--allow-restart') o.allowRestart = true;
    else if (k === '--json') o.json = a[++i];
    else if (k === '-h' || k === '--help') o.help = true;
    else { console.error('unknown arg ' + k); process.exit(2); }
  }
  if (!['pre', 'post', 'all'].includes(o.phase)) { console.error('--phase must be pre|post|all'); process.exit(2); }
  return o;
}
const USAGE = `usage: release/run.sh [--case 1,3] [--phase pre|post|all] [--allow-restart] [--no-go] [--strict] [--json out.json] [--list]
  --phase pre    snapshots only (pairing sessions, goose, QA marker data); run BEFORE the deploy
  --phase post   (default for most cases) compare / verify; run right AFTER the deploy
  --allow-restart  case 5 may swap the QA gateway binary and restart it (QA instance only)
  --strict       SKIP counts as failure
env: see tests/e2e/docs/release-5.0.md`;

function makeCtx(def, opts, results) {
  const checks = [];
  const log = (tag, name, detail) => { checks.push({ tag, name, detail }); console.log(`  ${tag.padEnd(11)} ${name}${detail ? '  — ' + String(detail).slice(0, 300) : ''}`); };
  return {
    lib, opts, def, checks,
    pass: (n, d) => log('PASS', n, d), fail: (n, d) => log('FAIL', n, d), skip: (n, d) => log('SKIP', n, d), warn: (n, d) => log('WARN', n, d), info: (n, d) => log('INFO', n, d),
    check(n, ok, d) { ok ? log('PASS', n) : log('FAIL', n, d); return !!ok; },
    device(n, steps) { log('REAL-DEVICE', n, steps); },
  };
}

(async () => {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) { console.log(USAGE); return; }
  if (opts.list) { for (const c of CASES) console.log(`${c.id}\t${c.name}\t${c.readonly ? 'read-only-safe' : 'QA data/process'}${c.needsDevice ? '\tneeds real device' : ''}`); return; }
  try { lib.guard(); } catch (e) { console.error('GUARD: ' + e.message); process.exit(2); }
  console.log(`# Eve release 5.0.x post-deploy bundle  target=${lib.TARGET}  base=${lib.BASE}  phase=${opts.phase}  ${new Date().toISOString()}`);
  const results = [];
  for (const def of CASES) {
    if (opts.cases && !opts.cases.includes(def.id)) continue;
    console.log(`\n== Case ${def.id}: ${def.name} ==`);
    const ctx = makeCtx(def, opts, results);
    if (lib.RO() && !def.readonly) { ctx.skip('prod-ro target', 'case writes QA data / restarts a process; not allowed on prod'); }
    else {
      try { await def.run(ctx); } catch (e) { ctx.fail('case crashed', (e && e.stack || String(e)).split('\n').slice(0, 3).join(' | ')); }
    }
    const f = ctx.checks.filter(c => c.tag === 'FAIL').length, p = ctx.checks.filter(c => c.tag === 'PASS').length, d = ctx.checks.filter(c => c.tag === 'REAL-DEVICE').length;
    const status = f ? 'FAIL' : p ? 'PASS' : 'SKIP';
    console.log(`  => CASE ${def.id} ${status}${status === 'PASS' && d ? ' (automated part; real-device confirmation pending)' : ''}  [pass=${p} fail=${f} skip=${ctx.checks.filter(c => c.tag === 'SKIP').length} warn=${ctx.checks.filter(c => c.tag === 'WARN').length}]`);
    results.push({ id: def.id, name: def.name, status, pass: p, fail: f, device: d, checks: ctx.checks });
  }
  console.log('\n== SUMMARY ==');
  for (const r of results) console.log(`  case ${r.id}  ${r.status.padEnd(5)} ${r.device ? '[DEVICE] ' : '         '}${r.name}`);
  if (opts.json) fs.writeFileSync(opts.json, JSON.stringify({ at: new Date().toISOString(), target: lib.TARGET, base: lib.BASE, results }, null, 1));
  const bad = results.some(r => r.status === 'FAIL' || (opts.strict && r.status === 'SKIP'));
  console.log(bad ? 'RESULT: FAIL' : 'RESULT: PASS (SKIPs and real-device items still need a human)');
  process.exit(bad ? 1 : 0);
})();
