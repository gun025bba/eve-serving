'use strict';
// Shared helpers for the 5.0.x post-deploy bundle (tests/e2e/release). Never prints secrets; never targets 8001;
// 8000 only with EVE_TARGET=prod-ro + EVE_ALLOW_PROTECTED_PORT=1, and then only GET/HEAD is allowed.
const fs = require('fs'), os = require('os'), path = require('path'), http = require('http'), cp = require('child_process');

const E2E = path.resolve(__dirname, '..');
const REPO = path.resolve(E2E, '..', '..');
const TARGET = process.env.EVE_TARGET || 'qa';               // qa | prod-ro
const BASE = (process.env.EVE_BASE_URL || 'http://127.0.0.1:8002').replace(/\/$/, '');
const U = new URL(BASE);
const PORT = U.port || '80';
const HOME = os.homedir();

function guard() {
  if (!['qa', 'prod-ro'].includes(TARGET)) throw new Error(`EVE_TARGET must be qa or prod-ro, got ${TARGET}`);
  if (PORT === '8001') throw new Error('refusing port 8001 (gw-mt)');
  if (PORT === '8000' && !(TARGET === 'prod-ro' && process.env.EVE_ALLOW_PROTECTED_PORT === '1'))
    throw new Error('refusing port 8000 (prod). Read-only prod subset needs EVE_TARGET=prod-ro EVE_ALLOW_PROTECTED_PORT=1 and an explicit go-ahead.');
  if (TARGET === 'prod-ro' && PORT !== '8000') throw new Error('EVE_TARGET=prod-ro is only for the prod port 8000');
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(U.hostname) && process.env.EVE_ALLOW_REMOTE !== '1')
    throw new Error(`refusing non-loopback host ${U.hostname} (set EVE_ALLOW_REMOTE=1 for a tailnet name)`);
}
const RO = () => TARGET === 'prod-ro';

function apiKey() {
  if (process.env.EVE_TEST_API_KEY) return process.env.EVE_TEST_API_KEY.trim();
  const f = process.env.EVE_TEST_API_KEY_FILE || (TARGET === 'qa' ? path.join(REPO, '.test_api_key') : '');
  if (f && fs.existsSync(f)) return fs.readFileSync(f, 'utf8').trim();
  return '';
}

// Low-level request (node:http so Host can be overridden; redirects are never followed).
function req(method, p, o = {}) {
  method = method.toUpperCase();
  if (RO() && !['GET', 'HEAD'].includes(method)) throw new Error(`prod-ro: ${method} ${p} is not allowed`);
  const headers = { ...(o.headers || {}) };
  if (o.key) headers.Authorization = `Bearer ${o.key === true ? apiKey() : o.key}`;
  if (o.cookie) headers.Cookie = `eve_session=${o.cookie}`;
  if (o.csrf || (o.cookie && method !== 'GET' && method !== 'HEAD')) Object.assign(headers, { 'X-Eve-Request': '1', Origin: BASE, 'Sec-Fetch-Site': 'same-origin' });
  let data = o.body;
  if (data !== undefined && typeof data !== 'string') { data = JSON.stringify(data); headers['Content-Type'] = headers['Content-Type'] || 'application/json'; }
  if (data !== undefined) headers['Content-Length'] = Buffer.byteLength(data);
  return new Promise((resolve, reject) => {
    const r = http.request({ host: U.hostname === '[::1]' ? '::1' : U.hostname, port: PORT, method, path: p, headers, agent: false }, res => {
      const chunks = [];
      const onChunk = d => { chunks.push(d); if (o.stream && o.stream(d, res) === false) { res.destroy(); } };
      res.on('data', onChunk);
      const done = () => { const text = Buffer.concat(chunks).toString('utf8'); let json; try { json = JSON.parse(text); } catch {}
        resolve({ status: res.statusCode, headers: res.headers, text, json, setCookie: res.headers['set-cookie'] || [] }); };
      res.on('end', done); res.on('close', done);
    });
    r.setTimeout(o.timeout || 30000, () => r.destroy(new Error('timeout')));
    r.on('error', reject);
    if (data !== undefined) r.write(data);
    r.end();
  });
}

function parseSSE(text) {
  const out = [];
  for (const block of text.split(/\n\n+/)) {
    let ev = 'message'; const d = [];
    for (const l of block.split('\n')) { if (l.startsWith('event:')) ev = l.slice(6).trim(); else if (l.startsWith('data:')) d.push(l.slice(5).trim()); }
    if (d.length) { const data = d.join('\n'); let json; try { json = JSON.parse(data); } catch {} out.push({ event: ev, data, json }); }
  }
  return out;
}

// ---------- QA-only state (cookies etc.), 0700 dir / 0600 files, gitignored ----------
const STATE_DIR = process.env.EVE_STATE_DIR || path.join(E2E, '.release-state');
function stateRead(name) { try { return JSON.parse(fs.readFileSync(path.join(STATE_DIR, name), 'utf8')); } catch { return null; } }
function stateWrite(name, obj) {
  fs.mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
  const f = path.join(STATE_DIR, name); fs.writeFileSync(f, JSON.stringify(obj, null, 1), { mode: 0o600 }); fs.chmodSync(f, 0o600);
}

// ---------- QA instance (launchd agent) ----------
const QA_PLIST = process.env.EVE_QA_PLIST || path.join(HOME, 'Library/LaunchAgents/com.llmserving.gateway-test.plist');
const QA_LABEL = process.env.EVE_QA_LABEL || 'com.llmserving.gateway-test';
let _plist;
function qaPlist() {                 // {env, args} or null. Env values (DB URL etc.) stay in memory, never logged.
  if (_plist !== undefined) return _plist;
  try {
    const j = JSON.parse(cp.execFileSync('plutil', ['-convert', 'json', '-o', '-', QA_PLIST], { encoding: 'utf8' }));
    _plist = { env: j.EnvironmentVariables || {}, args: j.ProgramArguments || [], label: j.Label };
  } catch { _plist = null; }
  return _plist;
}
const GW_CLI = process.env.EVE_GW_CLI || path.join(REPO, 'bin', 'gateway-test');
function cliSupportsPair() {         // builds before the final 4.0.0 have no `pair` subcommand: running them with args would start a gateway!
  try { return cp.spawnSync('bash', ['-c', `strings -a "${GW_CLI}" | grep -c "one-time pairing code"`], { encoding: 'utf8' }).stdout.trim() !== '0'; } catch { return false; }
}
function cli(args, o = {}) {
  if (RO()) throw new Error('prod-ro: the admin CLI is never run against prod');
  if (!cliSupportsPair()) throw new Error(`${GW_CLI} has no pair/sessions subcommands (build predates 4.0.0 final)`);         // host-side admin CLI against the QA instance DB
  const p = qaPlist(); if (!p) throw new Error(`cannot read QA plist ${QA_PLIST}`);
  const env = { ...process.env }; for (const [k, v] of Object.entries(p.env)) if (k.startsWith('GW_')) env[k] = String(v);
  const r = cp.spawnSync(GW_CLI, args, { env, encoding: 'utf8', timeout: o.timeout || 40000 });
  return { code: r.status, out: (r.stdout || '').trim(), err: (r.stderr || '').trim() };
}
function sessionsList() {            // [{id,user,created,lastSeen,state}]
  const cmd = process.env.EVE_SESSIONS_CMD;     // prod/operator hook: any command that prints `gateway sessions list`
  let out;
  if (cmd) { const r = cp.spawnSync('bash', ['-c', cmd], { encoding: 'utf8', timeout: 40000 }); if (r.status !== 0) throw new Error('EVE_SESSIONS_CMD failed'); out = r.stdout; }
  else { if (RO()) throw new Error('prod-ro: set EVE_SESSIONS_CMD (the QA CLI must never be pointed at prod)'); const r = cli(['sessions', 'list']); if (r.code !== 0) throw new Error('sessions list: ' + r.err.slice(0, 160)); out = r.out; }
  return out.split('\n').slice(1).filter(Boolean).map(l => { const f = l.trim().split(/\s+/); return { id: f[0], user: f[1], created: f[2], lastSeen: f[3], state: f[4] }; });
}
function pgEnv() {
  const p = qaPlist(); const url = p && p.env.GW_DATABASE_URL; if (!url) return null;
  const u = new URL(url);
  return { ...process.env, PGHOST: u.hostname, PGPORT: u.port || '5432', PGUSER: decodeURIComponent(u.username), PGPASSWORD: decodeURIComponent(u.password), PGDATABASE: u.pathname.slice(1) };
}
function gooseVersion() {            // read-only; null when not readable
  const cmd = process.env.EVE_GOOSE_CMD;        // prod/operator hook printing the integer
  if (cmd) { const r = cp.spawnSync('bash', ['-c', cmd], { encoding: 'utf8' }); const n = parseInt(r.stdout, 10); return Number.isFinite(n) ? n : null; }
  if (RO()) return null;                        // never read the QA DB while checking prod
  const env = pgEnv(); if (!env) return null;
  const r = cp.spawnSync('psql', ['-X', '-Atc', 'select coalesce(max(version_id),0) from goose_db_version where is_applied'], { env, encoding: 'utf8', timeout: 15000 });
  const n = parseInt(r.stdout, 10); return r.status === 0 && Number.isFinite(n) ? n : null;
}

// ---------- running version: authenticated /whoami + X-Eve-Version (since 4.0.0 /health has no version) ----------
async function runningVersion() {
  const r = await req('GET', '/whoami', { key: true });
  if (r.status === 401 && RO() && !apiKey()) return binaryVersion();   // no read-only prod key supplied: fall back to the on-disk binary
  if (r.status !== 200) return { ok: false, why: `whoami ${r.status}` };
  const hv = r.headers['x-eve-version'], bv = r.json && r.json.version;
  return { ok: !!bv && hv === bv, version: bv, header: hv, why: bv === hv ? '' : `body ${bv} != header ${hv}` };
}

// prod-ro without a key: version of the binary the service runs (path from `launchctl print`, or EVE_GW_BINARY) + whether the process started after it was written.
// Weaker than /whoami (reports "ok: true, weak: true"): proves what is on disk and that a process started from it, not what the process answers.
function binaryVersion() {
  const sh = c => cp.spawnSync('bash', ['-c', c], { encoding: 'utf8', timeout: 20000 }).stdout.trim();
  const bin = process.env.EVE_GW_BINARY || sh("launchctl print system/com.llmserving.gateway 2>/dev/null | awk '/program =/{print $3; exit}'") || '/opt/eve-gw/bin/gateway';
  const m = /\b(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?\+[0-9a-f]{7,})\b/.exec(sh(`strings -a "${bin}" | grep -E '^[0-9]+\\.[0-9]+\\.[0-9]+.*\\+[0-9a-f]{7}$' | head -3`));
  if (!m) return { ok: false, why: `whoami 401 and no version string in ${bin}` };
  const pid = sh(`pgrep -f "${bin}" | head -1`), started = pid ? sh(`ps -o lstart= -p ${pid}`) : '', mt = sh(`stat -f '%Sm' "${bin}"`);
  const fresh = pid && Date.parse(started) >= Date.parse(mt) - 60000;
  return { ok: !!fresh, weak: true, version: m[1], header: '', why: fresh ? '' : `process started ${started} before binary mtime ${mt}`, via: `${bin} (binary mtime ${mt}, process started ${started})` };
}

// ---------- paired QA browser session ----------
async function pairQA(login) {
  const c = cli(['pair', '--user', login]); if (c.code !== 0) throw new Error(`pair CLI failed: ${c.err.slice(0, 160)}`);
  const code = c.out.split('\n').pop().trim();
  const r = await req('POST', '/v1/pair', { csrf: true, body: { code } });
  if (r.status !== 204) throw new Error(`POST /v1/pair -> ${r.status}`);
  const sc = r.setCookie.find(x => x.startsWith('eve_session=')); if (!sc) throw new Error('no eve_session cookie');
  return sc.split(';')[0].slice('eve_session='.length);
}
async function sessionValid(cookie) {
  const r = await req('GET', '/whoami', { cookie }); return { status: r.status, json: r.json };
}

const sha = s => require('crypto').createHash('sha256').update(s).digest('hex').slice(0, 16);
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function model() {            // EVE_MODEL or the first id of GET /v1/models (QA key)
  if (process.env.EVE_MODEL) return process.env.EVE_MODEL;
  const r = await req('GET', '/v1/models', { key: true }); const d = r.json && (r.json.data || []);
  return (d[0] && d[0].id) || 'default';
}
module.exports = { model, cliSupportsPair, E2E, REPO, TARGET, BASE, PORT, HOME, guard, RO, apiKey, req, parseSSE, stateRead, stateWrite, qaPlist, QA_LABEL, QA_PLIST, GW_CLI, cli, sessionsList,
  pgEnv, gooseVersion, runningVersion, pairQA, sessionValid, sha, sleep };
