# Eve E2E tests (Playwright)

Targets the **test instance** only. Default `EVE_BASE_URL=http://127.0.0.1:8002`.
Ports 8000 (prod) and 8001 (gw-mt) are refused by `env.js`.

## Setup
```sh
cd tests/e2e
npm install
npx playwright install webkit
```

## Env
| var | meaning |
|---|---|
| `EVE_BASE_URL` | gateway base URL (default `http://127.0.0.1:8002`) |
| `EVE_TEST_API_KEY` | test API key (Bearer). Or: |
| `EVE_TEST_API_KEY_FILE` | path to a file containing the key |
| `EVE_ORIGIN` | Origin allowed by the test instance (`GW_ALLOWED_ORIGINS`) |
| `EVE_MODEL` | model id (default Qwen3-30B-A3B-4bit) |
| `EVE_BROWSER_IDENTITY=1` | enable browser-identity tests (CSRF matrix, WebKit resume) once identity injection exists |

Never commit keys. Tests that need a key/identity are skipped with a reason when absent.

## Run
```sh
npx playwright test --list          # list
npx playwright test --project=api   # API regression + smoke
npx playwright test --project=iphone-webkit   # background-resume (iPhone 15 WebKit)
npx playwright show-report
```

## Specs
- `api-smoke` — /health, /ready, /ui/ version
- `api-chat` — non-stream, stream (eve.meta … [DONE]), stream replay, cancel
- `api-search-proactive` — /v1/search shape, /v1/actions, VAPID key
- `api-csrf` — 403 matrix for browser identity; API key bypass
- `background-resume` — iPhone PWA "answer disappears" bug: visibility, offline, aborted stream, reload.
  Expected to fail on UI 1.1.1 (no resume) and pass once UI resumes via `/messages/{mid}/stream`.
