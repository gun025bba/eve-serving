# eve-web 1.10.1 QA (PR #26, head c350ed7, merged as 4506c93)

QA instance 127.0.0.1:8002 only. The UI under test is a local checkout (`EVE_UI_DIR`, default `~/eve-qa/fe/eve-web` at c350ed7). The page is opened at the real gateway origin and
Playwright `route.fulfill`s every `/ui/**` request from that directory, so `/v1` and `/whoami` hit the real QA gateway with a paired QA session cookie (qa-a / qa-b), same origin, no header
rewriting, no deploy. The memory UI is behind `localStorage eve.flag.memory=1` (set by the harness).

```
node tests/e2e/fe1101/run.js [--only s1,s2,s3,s4,s5,s6]     # all scenarios, ~6 min (s6 sends 3 short prompts to MLX)
node tests/e2e/fe1101/s4_builds.js                            # PATCH probe on 5.0.0 / 5.0.1 / 5.0.2-d045b10 QA builds, rolls forward to 5.0.0-qa at the end
EVE_UI_DIR=<other checkout> EVE_EXPECT_UI_VERSION=1.10.0 node tests/e2e/fe1101/run.js --only s6   # baseline comparison
```

| id | what |
|---|---|
| s1 | happy path: PATCH 200 -> localStorage -> reload -> no sheet; server flag persists across a new context; per-user flag |
| s2 | PATCH 500 / 400 / network failure: sheet stays, in-sheet error (role=alert), localStorage unset, 3 retries, reload asks again, recovery; scrim/설정에서 끄기 while failing; slow PATCH no double send |
| s3 | local seen + server empty: exactly one silent PATCH per load, no flash (MutationObserver), no loop on failure, no retry when memory off / settings GET fails / local flag off |
| s4 | gateway: does `ui.memory_intro_seen` get 400? (+ control keys) |
| s5 | fixed cases: standalone-emulated top area (computed backdrop-filter none incl. pseudo-elements and `.navbar.collapsed`), Chromium CDP `display-mode: standalone` + `setSafeAreaInsetsOverride(47px)` numbers, composer outside `<form>` + autofill attributes |
| s6 | smoke on the real gateway: send, streaming (>=3 distinct lengths), hidden->visible mid-stream; offline->online is informational (see below) |

Limits (real-device items): iOS contact-suggestion bar, real notch safe-area, SW update on a home-screen PWA.

Known/pre-existing (same on 1.10.0 = 6f88ab7, so not a 1.10.1 regression): offline -> online while the answer is still generating (300-line prompt) leaves the bubble on "답변 불러오는 중…"
and it never shows the answer (WebKit and Chromium emulation, 4/4 runs on both builds while MLX was slow, ~20 s answers). When the model is fast enough that the answer is finished before the network returns (12/12 runs with a 120-line prompt, and the last full run) the UI recovers, so the informational variants in s6 are timing dependent and are not counted.
4.0.0+81f4292 (`qa-env.sh rollback`) answers `PATCH ui.memory_intro_seen` with 400 `{"error_code":"invalid"}`; 5.0.0-qa.a419ef9, 5.0.1-qa.f3d59e0 and 5.0.2-qa.d045b10 answer 200 (unknown ui keys and wrong types are 400 on all of them).

## 1.10.2 (PR #28, head 40c69c1)
- `s7_resume.js [--reps N]`: offline 5 s / 2 s and hidden→visible while a 300-line answer is generating, WebKit + Chromium iPhone emulation. Counted checks: server message `generating` at the moment the network returns, GET `.../stream` with `Last-Event-ID`, live bubble DOM node kept, one assistant bubble/message, numbers 1..300 exactly once in order, UI text == server text. Samples where the model itself stops early (UI == server, < 300 numbers) are discarded and rerun (up to 3).
- `run.js` s2 adds non-JSON 200 (html/plain), empty 200 and 204 PATCH → not saved, plus a layout-shift check (rects of sheet/title/좋아요/설정에서 끄기/error line identical before vs after the error appears; measured after the slide-in has settled).
- Run: `EVE_UI_DIR=<checkout of 40c69c1> EVE_EXPECT_UI_VERSION=1.10.2 node tests/e2e/fe1101/run.js` and `node tests/e2e/fe1101/s7_resume.js --reps 3`.

## 2.0.0 (PR #37, head cc686c4) — automatic tool selection
- `s8_autotools.js [--only A,B,C]`
  - A: request bodies (new chat, 2nd message, regenerate, retry after 503, resume after offline, Today composer) contain only `model, messages, stream, stream_options, thread_id, replace_message_id`; no `web*` in any /v1 URL or body; legacy `llmchat.webSearch`/`llmchat.autoHintSeen`/extra keys in `llmchat.settings` are harmless (the two keys are removed).
  - B: no `#plusBtn #plusMenu #webBtn #tPlus #tMenu #tWeb`, no web-toggle copy, composer has no empty gap.
  - C: the QA gateway is 5.0.0 (no auto tools). The 6.0.0 contract (eve-serving PR #158) is injected by rewriting the REAL gateway SSE (tool_call/tool_result before the first token, `fresh`+`tools[]` on `chat.completion.persisted`) and the real GET messages (`metadata{fresh,tools}`, `sources`). Checks: status line gone after first token, source chips, `.tq` query line, failure notes by `fresh`/`tools[]`, same after reload and after leaving/reopening the thread, unknown tool ignored.
  - Known failing check (UI bug, Low): `sources[].published` is dropped by `citeMap()`, so no date is shown.
- A live 6.0.0 gateway could not be run on QA: migration 00009 needs the owner of `tool_calls` (the QA DB role is not), so the QA DB was left untouched. See the report.
- Run: `EVE_EXPECT_UI_VERSION=2.0.0 EVE_UI_DIR=<checkout of cc686c4> node tests/e2e/fe1101/{run.js,s7_resume.js,s8_autotools.js}`.
