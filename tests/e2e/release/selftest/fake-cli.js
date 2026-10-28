#!/usr/bin/env node
// Fake `eve-gw` for the selftest. Contains the marker string "one-time pairing code" so the bundle's capability probe accepts it.
const fs = require('fs'), crypto = require('crypto');
const ST = process.env.MOCK_STATE || '/tmp/eve-release-selftest/state.json'; const a = process.argv.slice(2);
const load = () => { try { return JSON.parse(fs.readFileSync(ST, 'utf8')); } catch { return { codes: {}, sessions: [], n: 0 }; } };
const st = load();
if (a[0] === 'pair') { const code = crypto.randomBytes(4).toString('hex'); st.codes[code] = a[2]; fs.writeFileSync(ST, JSON.stringify(st)); console.error('one-time pairing code'); console.log(code); }
else if (a[0] === 'sessions' && a[1] === 'list') { console.log('ID  USER  CREATED  LAST_SEEN  STATE'); for (const s of st.sessions) console.log([s.id, s.user, '2026-10-28T00:00:00Z', '2026-10-28T00:00:00Z', s.state].join('  ')); }
else if (a[0] === 'sessions' && a[1] === 'revoke') { const s = st.sessions.find(x => String(x.id) === a[2]); if (s) s.state = 'revoked'; fs.writeFileSync(ST, JSON.stringify(st)); console.log('revoked session ' + a[2]); }
else process.exit(2);
