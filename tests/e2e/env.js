// Shared test env. Never hardcode secrets; never target prod (8000) or gw-mt (8001).
const fs = require('fs');
const BASE_URL = (process.env.EVE_BASE_URL || 'http://127.0.0.1:8002').replace(/\/$/, '');
const port = new URL(BASE_URL).port;
if (['8000', '8001'].includes(port) && process.env.EVE_ALLOW_PROTECTED_PORT !== '1') {
  throw new Error(`Refusing to run against protected port ${port}. Use the test instance (8002).`);
}
function apiKey() {
  if (process.env.EVE_TEST_API_KEY) return process.env.EVE_TEST_API_KEY.trim();
  const f = process.env.EVE_TEST_API_KEY_FILE;
  if (f && fs.existsSync(f)) return fs.readFileSync(f, 'utf8').trim();
  return '';
}
const ORIGIN = process.env.EVE_ORIGIN || BASE_URL;           // must be in GW_ALLOWED_ORIGINS of the test instance
const MODEL = process.env.EVE_MODEL || 'mlx-community/Qwen3-30B-A3B-4bit';
// Browser identity injection (Tailscale identity) does not exist yet; set when available.
const BROWSER_IDENTITY = process.env.EVE_BROWSER_IDENTITY === '1';
const EXPECT_UI_VERSION = process.env.EVE_EXPECT_UI_VERSION || '1.1.3';
module.exports = { BASE_URL, ORIGIN, MODEL, apiKey, BROWSER_IDENTITY, EXPECT_UI_VERSION };
