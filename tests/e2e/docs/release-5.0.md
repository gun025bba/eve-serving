# Release 5.0.x post-deploy bundle

One entry point for the 19:00 KST deploy: gateway **5.0.0** (calendar adapter off) → chat-ui **1.9.0** → gateway **5.0.1**.
Prepared against QA only. It never deploys, never targets 8001, never touches the prod DB or anyone's threads.

```
tests/e2e/release/run.sh --list
tests/e2e/release/run.sh --phase pre              # BEFORE the deploy: snapshots (sessions, goose, QA session)
tests/e2e/release/run.sh --phase post             # AFTER each deploy step: verify
tests/e2e/release/run.sh --case 3,7 --no-go       # a subset; --no-go skips the Go unit tests
tests/e2e/release/run.sh --case 5 --allow-restart # rollback chain on the QA agent (swaps the QA binary, restores it)
tests/e2e/release/run.sh --json out.json --strict # SKIP counts as failure
```
Output is `PASS/FAIL/SKIP/WARN/INFO/REAL-DEVICE` per check, `=> CASE n PASS|FAIL|SKIP` per case, a SUMMARY and `RESULT:`. Exit code 1 if any case FAILs (or SKIPs with `--strict`).
Needs `node_modules/@playwright` (`npm i` in tests/e2e, `npx playwright install webkit chromium`), `go`, `psql` (read-only goose check) and the QA plist/key on the machine that runs the QA instance.

## What exists in the release (checked 2026-10-28, KST)
| item | ref | note |
|---|---|---|
| 5.0.0 | `a419ef9` | no `v5.0.0` tag exists, so this SHA is an **assumption** |
| 5.0.1 | `release/5.0.1` (04c6c60 at the time of writing) | = 5.0.0 + #79 + #80 + #91 (root redirect) + #90/#93 (passwordless deploy) |
| chat-ui 1.9.0 | eve-chat-ui `main` (c23915e, PR #20 source chips) | SW cache `llmchat-1.9.0` |
| calendar adapter | `eve-pim` helper, draft PRs #75–#78 | **not in release/5.0.1**; flags `GW_PIM_ENABLED`, `GW_PIM_WRITES_ENABLED`, `GW_BRIEFING_ENABLED` default off; only HTTP route `POST /v1/briefing/preview` |
| auth since 4.0.0 | – | `eve_session` cookie or Bearer key; pairing via `eve-gw pair --user` + `POST /v1/pair`; `/health` has no version → read `/whoami`.version == `X-Eve-Version` |
| 5.0.x schema | – | no migration, goose stays 8 |

## Cases
| # | case | status | how it checks | assumptions / prerequisites |
|---|---|---|---|---|
| 1 | pairing + sessions survive the upgrade | ready with assumption | `--phase pre` records `sessions list`, goose, running version and pairs one QA session; `--phase post` compares: same active sessions, same goose, QA cookie still 200 without re-pair, a fresh code still redeems (then revoked), UI opens the list not the pairing screen, Go pairing tests | needs a QA instance on a 4.0.0+ build (DB goose 8) and `EVE_QA_LOGIN_A` in `allowed_users`. **Prod**: read-only via `EVE_SESSIONS_CMD` / `EVE_GOOSE_CMD` hooks (sudo needed, operator runs them). Real iPhone: the PWA opens signed in |
| 2 | memory/threads/search per user | ready with assumption | two paired QA sessions (A, B): own canary items, cross-read/patch/delete by id, wipe-all by B leaves A, Bearer key 403 `scope_denied`, imported thread + `/v1/search` positive control for A and zero hits/404 for B, optional prompt-level recall (2 tiny chats), then removes its QA data. Plus 21 in-process Go isolation tests | **blocker for live run**: QA needs two allowlisted logins (`EVE_QA_LOGIN_A/B`, default `qa-a@test.invalid`/`qa-b@test.invalid`), memory on (`GW_MEMORY_ENABLED` not false) and a 4.0.0+ build. Without them the live part SKIPs and only the Go tests run. Bearer is memory-off in 5.0.x, so API-key identities cannot be used |
| 3 | `/` → `/ui/` | ready | 302, `Location: /ui/`, no-store, HEAD, 421 on foreign Host, `/ui` 307 unchanged, other paths 404 not redirected; **since 5.0.1+f3d59e0 (#96) the raw query is carried: `/?a=b` → `/ui/?a=b`** (printable ASCII, ≤ 2048 bytes, else plain `/ui/`); no open redirect / header injection with a hostile query. `EVE_EXPECT_QUERY_PRESERVED=0` restores the pre-#96 expectation (query dropped) | prod-ro safe (GET/HEAD only). QA builds older than f3d59e0 fail the query checks by design |
| 4 | source chips | ready | one forced web-search chat (200 tokens): SSE `chat.completion.sources` + persisted `message.sources`, URL validity (http(s), public host, no creds), `[n]` consistency; then WebKit UI 1.9.0 renders `.sources .sc` chips (≤2 + `+N`), expand `.src-list`, hrefs == payload, `rel=noopener` | needs web search (SearXNG) and the QA key. `EVE_CHECK_LINK_REACHABLE=1` also GETs the links from the box. Real iPhone for look/feel |
| 5 | rollback 5.0.1 → 5.0.0 → previous → 5.0.1 | ready with assumption | preconditions + QA marker thread + baseline always; with `--allow-restart` swaps the QA binary through the chain, after each step: version read from `/whoami`, `/health`, goose, marker hash, session, memory hash, session table, chat smoke; restores the original binary in `finally` | build binaries with `release/build.sh` and export `EVE_BIN_500/501` (+`EVE_BIN_PREV`, default `bin/gateway-test-4.0.0`). QA only. **Prod rollback is a paper checklist below** (needs sudo) |
| 6 | calendar adapter **read-only** (`EVE_CALENDAR_MODE=read-only`, default; `off` = old expectation) | ready | `GW_PIM_WRITES_ENABLED` not true (read from `launchctl print` names GW_PIM*/GW_BRIEFING* only, or `EVE_PIM_ENV_CMD`); no calendar/reminders/pim route answers 2xx; mutation paths (`/v1/actions/{id}/approve\|reject\|reissue`, `/v1/briefing/preview`) are rejected (GET probes on prod, fake-id POSTs on QA, nothing can be created); session reads `/v1/actions` 200, API key 403; whoami/openapi advertise no write tool; helper socket has no world access; Go tests on the pim ref (`TestPimTools_OffByDefault`, `TestPimWrite_OffByDefault`, `_FlagOffAfterProposalBlocksApprove`, `_NonOwnerHiddenAndDenied`, `TestPimWritesConfig`, `TestBriefingPreview_OffIs404`) | prod-ro safe. WARNs when `GW_PIM_ENABLED` is not true: then reads cannot be exercised. pim code is **not in f3d59e0** (only on feat/jarvis-m1-* + fix/pim-review-lows), so a gateway built from f3d59e0 cannot expose calendar reads regardless of the flag |
| 7 | fixed UI regressions (every run) | ready, needs real device | WebKit with emulated standalone (JS side) light+dark: computed `backdrop-filter` none on `.navbar`, `.navbar.collapsed` (after scrolling the list), `#topbar`, plus a whole-page sweep; Chromium CDP `display-mode: standalone`; composer textarea not in a `<form>`, `autocomplete=eve-composer`, `name`, `autocorrect/autocapitalize=off`, `spellcheck=false`; version 1.9.0 | verified to FAIL on a pre-fix UI (36ccda2^). **Real iPhone required**: WebKit cannot emulate the notch safe-area, the CSS display-mode media feature or the iOS contact-autofill bar |

## Environment
| var | meaning |
|---|---|
| `EVE_BASE_URL` | default `http://127.0.0.1:8002`; 8001 refused; 8000 only with the prod-ro flags |
| `EVE_TARGET=prod-ro` + `EVE_ALLOW_PROTECTED_PORT=1` | read-only prod subset (GET/HEAD only; cases without read-only paths SKIP). Needs an explicit go-ahead |
| `EVE_TEST_API_KEY` / `EVE_TEST_API_KEY_FILE` | QA key (default `.test_api_key` on qa) |
| `EVE_GW_CLI` | admin CLI for the QA instance (`pair`, `sessions`): a 4.0.0+ `gateway` or `eve-gw` binary |
| `EVE_QA_LOGIN_A/B` | QA-only logins (must be in the QA `allowed_users`) |
| `EVE_UI_DIR` | serve the UI from a directory (e.g. an eve-chat-ui 1.9.0 worktree) instead of the gateway; UI cases then use a mocked backend |
| `EVE_EXPECT_UI_VERSION` (1.9.0), `EVE_EXPECT_GW_VERSION`, `EVE_EXPECT_COMPOSER_AUTOCOMPLETE` | expectations |
| `EVE_REF_501` | ref for the Go tests (default `origin/release/5.0.1`) |
| `EVE_BIN_500/501/PREV`, `EVE_PREV_PREFIX` | case 5 binaries |
| `EVE_SESSIONS_CMD`, `EVE_GOOSE_CMD`, `EVE_PIM_ENV_CMD`, `EVE_GW_LOG` | operator hooks for prod (commands printing `eve-gw sessions list`, the goose integer, the gateway env, a log path) |
| `EVE_SKIP_CHAT=1`, `EVE_CASE6_CHAT=1`, `EVE_CHECK_LINK_REACHABLE=1`, `EVE_STATE_DIR` | optional switches |
| `EVE_CALENDAR_MODE`, `EVE_PIM_ENV_CMD`, `EVE_PIM_SOCKET`, `EVE_GW_LOG`, `EVE_REF_PIM`, `EVE_GW_BINARY` | case 6 / prod-ro: mode, how to read the gateway flags, helper socket path, gateway log, ref for the pim Go tests, on-disk binary used for the version fallback when no prod key is given (weak: reports WARN) |
QA cookies live in `tests/e2e/.release-state/` (0700/0600, gitignored); nothing secret is printed.

## Suggested run tonight (KST)
1. Before 19:00: `release/build.sh` (QA binaries), `run.sh --phase pre`, `run.sh --case 7` as the UI baseline.
2. After gateway 5.0.0 (adapter off): `--case 1,6 --phase post` (+ 2 on QA).
3. After chat-ui 1.9.0: `--case 4,7`, then the iPhone checklist.
4. After 5.0.1: `--phase post` (all), `--case 3`.
5. Rollback drill, QA only: `--case 5 --allow-restart`.

## Prod rollback checklist (paper, run by the operator with sudo)
LaunchDaemon `system/com.llmserving.gateway`, `/opt/eve-gw`. For each hop (5.0.1 → 5.0.0 → previous → 5.0.1): keep the previous binary beside the live one; swap + `launchctl kickstart -k`; then check `curl -s :8000/health` is `{"status":"ok"}` **and** the authed `/whoami` version/`X-Eve-Version` equals the target; `goose` max version unchanged (8); `eve-gw sessions list` identical; the phone still opens without re-pairing; a thread you own still reads. Stop and roll forward/back on any difference. None of this is automated against 8000.

## Real-device checklist (iPhone, home-screen PWA; cannot be automated)
- Force-quit and reopen: opens signed in (no pairing screen), settings footer reads v1.9.0, SW 1.9.0.
- Header/status-bar area: solid, no blur, also after scrolling the list and a long thread; header sits below the notch with no double padding.
- Composer: tap it → no contact/autofill suggestion bar above the keyboard.
- Web question: chips under the answer, `+N` expands, link opens in Safari.

## Known gaps
- Cases 1, 2, 5 live paths could not run on 8002 (old pre-pairing build, DB at goose 7); their logic is covered by `release/selftest/run-selftest.sh` (mock gateway on 8003, cases 1–2) and by the Go tests. Case 5's swap loop is exercised only with `--allow-restart` on a 4.0.0+ QA instance.
- `release/build.sh` stamps QA builds `5.0.x-qa.<sha>`; a real deploy's version string differs, so `EVE_EXPECT_GW_VERSION` should be set from the deploy output.
