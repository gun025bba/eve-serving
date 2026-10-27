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
| `EVE_EXPECT_UI_VERSION` | expected UI version in settings (default `1.1.3`) |
| `EVE_SEED_NOREPLY_THREAD` | thread id ending with a user turn (no-reply retry spec) |
| `EVE_BROWSER_IDENTITY=1` | enable browser-identity tests (CSRF matrix, WebKit resume) once identity injection exists |

Never commit keys. Tests that need a key/identity are skipped with a reason when absent.

## Run
```sh
npx playwright test --list          # list
npx playwright test --project=api   # API regression + smoke
npx playwright test --project=iphone-webkit   # ui-*.spec.js (iPhone 15 WebKit)
npx playwright show-report
```

## Specs
- `api-smoke` — /health, /ready, /ui/ version
- `api-chat` — non-stream, stream (eve.meta … [DONE]), stream replay, cancel
- `api-search-proactive` — /v1/search shape, /v1/actions, VAPID key
- `api-csrf` — 403 matrix for browser identity; API key bypass
- `api-retry` — cancel → regenerate in place (replace_message_id), user message not duplicated
- `api-websearch` — chat web_search=force events/sources (skips if disabled)
- `ui-stop-retry` — stop→cancel+중지됨, failure+다시 시도→answer, no duplicate user message
- `ui-settings-proactive` — version (EVE_EXPECT_UI_VERSION), 선톡 card, back badge
- `ui-background-resume` — iPhone PWA "answer disappears" bug: visibility (shows 답변 불러오는 중), offline, cut stream, reload. Selectors from eve-chat-ui PR #4.
  Expected to fail on UI 1.1.1 (no resume) and pass once UI resumes via `/messages/{mid}/stream`.

## Memory P1
Plan: `docs/memory-p1-test-plan.md`. API: `EVE_MEMORY_PROFILE=off|on|readonly|auditfail npx playwright test specs/api-memory.spec.js` (needs GW ≥ 4.1.0 on 8002, backend sets the profile). Go: `scripts/memory-go-checks.sh [ref]`. FS: `scripts/memory-fs-check.sh <data_dir> [--log gateway-test.log]`.
