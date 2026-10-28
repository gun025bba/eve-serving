'use strict';
// Does PATCH /v1/settings {ui:{memory_intro_seen:true}} return 400 on other QA gateway builds? Swaps the 8002 QA binary (QA only, ~2 s downtime per hop)
// via ~/eve-qa/qa-env.sh and ALWAYS rolls forward to the standard QA build (5.0.0-qa) at the end.  node tests/e2e/fe1101/s4_builds.js [bin/gateway-test-5.0.1 bin/gateway-test-pr99d ...]
const cp = require('child_process'), path = require('path');
const H = require('./lib'), { L } = H;
const builds = process.argv.slice(2).length ? process.argv.slice(2) : ['bin/gateway-test-5.0.0', 'bin/gateway-test-5.0.1', 'bin/gateway-test-pr99d'];
const env = path.join(L.HOME, 'eve-qa/qa-env.sh');
const sh = a => cp.spawnSync('bash', [env, ...a], { encoding: 'utf8', timeout: 90000 });
(async () => {
  const B = await H.session('qa-b@test.invalid', 'fe-B.json'); const orig = await H.getSettings(B);
  try {
    for (const b of builds) {
      const r = sh(['use', path.join(L.REPO, b)]); if (r.status !== 0) { H.chk(`${b}: switch`, false, (r.stdout + r.stderr).slice(-200)); continue; }
      const w = await L.runningVersion(); const rows = [];
      for (const [label, body] of [['true', { ui: { memory_intro_seen: true } }], ['false', { ui: { memory_intro_seen: false } }], ['bogus key', { ui: { bogus_key: true } }]]) { const x = await H.api(B, 'PATCH', '/v1/settings', body); rows.push(`${label}=${x.status}`); if (label === 'true') H.chk(`gateway ${w.version}: PATCH ui.memory_intro_seen=true is accepted (200), not 400`, x.status === 200, x.status + ' ' + x.text.slice(0, 120)); }
      const g = await H.getSettings(B); H.info(`gateway ${w.version}`, rows.join('  ') + `  GET ui=${JSON.stringify(g.ui)}`);
    }
  } finally {
    const f = sh(['forward']); const v = sh(['verify']);
    console.log((f.stdout.split('\n').slice(-3).join('\n')) + '\n' + v.stdout.split('\n').filter(l => /RESULT|FAIL/.test(l)).join('\n'));
    await H.setIntro(B, !!(orig.ui && orig.ui.memory_intro_seen)); await H.setMemory(B, orig.memory.enabled);
  }
  const t = H.totals(); console.log(`pass=${t.pass} fail=${t.fail}`); process.exit(t.fail ? 1 : 0);
})();
