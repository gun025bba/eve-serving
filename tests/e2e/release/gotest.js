'use strict';
// Runs the repo's own Go tests for a ref in a throw-away worktree (no network, no services). Opt out with --no-go.
const cp = require('child_process'), fs = require('fs'), os = require('os'), path = require('path');
const { REPO } = require('./lib');

function sh(cmd, args, o = {}) { return cp.spawnSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 << 20, ...o }); }

function goTests(c, { ref, run, pkgs, label, extraFiles = [] }) {
  if (!c.opts.go) return c.skip(`go tests: ${label}`, '--no-go');
  if (sh('go', ['version']).status !== 0) return c.skip(`go tests: ${label}`, 'go toolchain not found');
  if (sh('git', ['-C', REPO, 'rev-parse', '--verify', '-q', ref + '^{commit}']).status !== 0) return c.skip(`go tests: ${label}`, `ref ${ref} not found (git fetch origin)`);
  const wt = fs.mkdtempSync(path.join(os.homedir(), '.eve-qa-rel-wt-')), tmp = fs.mkdtempSync(path.join(os.homedir(), '.eve-qa-rel-tmp-'));
  try {
    const a = sh('git', ['-C', REPO, 'worktree', 'add', '-q', '--detach', path.join(wt, 'src'), ref]);
    if (a.status !== 0) return c.fail(`go tests: ${label}`, 'worktree add failed: ' + a.stderr.slice(0, 160));
    const src = path.join(wt, 'src');
    for (const [from, to] of extraFiles) fs.copyFileSync(path.join(REPO, from), path.join(src, to));
    const sha = sh('git', ['-C', src, 'rev-parse', '--short=9', 'HEAD']).stdout.trim();
    const r = sh('go', ['test', '-v', '-count=1', '-run', run, ...pkgs], { cwd: src, timeout: 900000, env: { ...process.env, TMPDIR: tmp, GOFLAGS: '-mod=mod' } });
    const out = (r.stdout || '') + (r.stderr || '');
    const failed = [...out.matchAll(/^--- FAIL: (\S+)/gm)].map(m => m[1]);
    const oks = [...out.matchAll(/^ok\s+\S+/gm)].length;
    const n = [...out.matchAll(/^--- PASS: /gm)].length;
    if (r.status === 0 && n === 0) c.warn(`go tests @${sha}: ${label}`, `0 tests matched -run '${run}' (rename drift?)`);
    else if (r.status === 0) c.pass(`go tests @${sha}: ${label}`, `${n} test(s) passed in ${oks} package(s)`);
    else c.fail(`go tests @${sha}: ${label}`, failed.length ? 'failed: ' + failed.slice(0, 6).join(', ') : out.trim().split('\n').slice(-4).join(' | '));
  } finally {
    sh('git', ['-C', REPO, 'worktree', 'remove', '--force', path.join(wt, 'src')]);
    fs.rmSync(wt, { recursive: true, force: true }); fs.rmSync(tmp, { recursive: true, force: true });
  }
}
module.exports = { goTests };
