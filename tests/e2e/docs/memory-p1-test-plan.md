# Memory P1 test plan (eve-serving PR #25, GW 4.1.0)

Sources: PR #25 diff (`internal/memory/*`, `internal/handlers/memory.go`), docs PR #5/#10/#14 (`docs/architecture/memory.md`).
Target: **test instance 8002 only**, API-key identity, throwaway data dir. Never 8000/8001, never real user data.

## Surface under test
| Endpoint | Expect |
|---|---|
| `GET /v1/memory` | 200 list, `Cache-Control: no-store` |
| `POST /v1/memory/items` `{"kind","text"}` | 201 / 422 `{"error_code":"secret_detected"}` / `cap_exceeded` / `invalid` |
| `PATCH /v1/memory/items/{id}` `{"pinned","text"}` | 200 / 404 foreign / 409 conflict |
| `DELETE /v1/memory/items/{id}`, `DELETE /v1/memory` | 204 |
| `GET /v1/memory/export` | 200 |
| `POST /v1/proposals/{id}/revise` `{"item_ids"}` (잊어줘 card) | 200 / 409 `item_gone` — **card API in a follow-up PR; API cases are test.fixme** |
| Errors | unsafe file → 503 `memory_read_only`; audit append failure → 500 `internal_error` (write refused) |

Config: `memory.enabled` / `GW_MEMORY_ENABLED` (default false), `memory.data_dir`, `memory.audit_key_file`, `memory.inject_cap_tokens` (0..2000).
Enabled requires data_dir + audit_key_file, else startup error.

## Cases
Legend: **A** = API spec on 8002 (`specs/api-memory.spec.js`), **G** = Go test (`scripts/memory-go-checks.sh`), **F** = filesystem script (`scripts/memory-fs-check.sh`), **B** = needs backend to restart 8002 with config profile below.

| # | Case | How | Profile |
|---|---|---|---|
| M1 | Default off: `/v1/memory*` return 404 identical to unknown route | A + G `TestMemoryDisabled_NoRoutesNoPromptChange` | off |
| M2 | Off: chat prompt untouched (no memory preamble) | G (upstream prompt not observable black-box) | — |
| M3 | Off: no memory dir/files created | F | off |
| M4 | users.id file key: negative, `+1`, `01`, non-numeric, `..`, `../1`, `1/2`, `/etc/passwd`, NUL, fullwidth digit, overflow rejected without disk access | G `TestMemoryFile_InvalidUserIDRejectedNoDiskAccess`, `_ValidNameRejectsTraversal`, `_NameIsNumericUserIDOnly` | — |
| M5 | File-internal `user_id` ≠ file name → rejected (→ 503 memory_read_only) | G `TestMemoryFile_UserIDMismatchRejected`; A in profile `readonly` | readonly |
| M6 | Perms: data dir 0700, `<uid>.json`/`.lock` 0600, names numeric only | F + G `TestMemory_FilePerms0600`, `TestMemoryAPI_FileNamedByNumericUserID` | on |
| M7 | Unsafe entries at startup (0644 file, symlink, foreign name, subdir, symlinked parent) refuse to start | G `TestMemoryOpen_RefusesUnsafeEntriesAtStartup`, `_RefusesSymlinkInParentPath` | B (startup) |
| M8 | Audit write failure → 500 fail-closed, nothing stored | A (profile `auditfail`) + G `TestMemoryAudit_FailureRefusesWrite` | auditfail |
| M9 | Audit stores HMAC only, no plaintext | G `TestMemory_AuditNoPlaintextHMAC` | — |
| M10 | Read-only: unsafe file at runtime → 503 `memory_read_only`, `no-store` | A (profile `readonly`) | readonly |
| M11 | HMAC key accepted: 64 hex, 64 hex + `\n`, uppercase | G `TestMemoryKey_ParseKeyCriteria`; B startup check | key-ok, key-ok-nl |
| M12 | HMAC key rejected: 32-hex, raw binary, inner/trailing space, CRLF, 2 newlines, odd length, all-zero, repeating, <12 distinct bytes; key file not 0600 / not owned | G `TestMemoryKey_*`; B startup refuses | key-bad-* |
| M13 | Memory block is the very end of the (last) system prompt; no user/tool text after; client system msgs unchanged; web mode too | G `TestMemoryInject_BlockIsEndOfSystemPrompt_NonWeb/_Web`, `_Order`, `_JSONEscapedWithNonce` | — |
| M14 | Injection cap (`inject_cap_tokens`), cap error injects nothing | G `TestMemory_InjectionCap`, `_CapErrorInjectsNothing` | — |
| M15 | CRUD round trip; `no-store` on all responses | A | on |
| M16 | Secret rejection: `내 비밀번호: hunter22`, API-key-like, high-entropy, URL userinfo → 422 secret_detected, not stored; Korean sentence no false positive | A + G `TestMemorySecret_*` | on |
| M17 | Isolation: other identity cannot read/patch/delete (404) | G (black-box needs 2 identities) | — |
| M18 | 잊어줘 card: forget needs delete card bound to item ids (payload hash); revise subset; deleted item → 409 item_gone; denied in tainted thread / scheduler | G `TestMemory_Forget*`, `TestMemoryRevise_*`; A revise 409 when a card exists | on |
| M19 | CSRF on memory writes (browser identity) | G `TestMemoryAPI_OriginCSRF`; A skipped until identity injection | — |
| M20 | Per-user `memory.enabled` (PR #40), ANDed with server flag. Architect decision 2026-10-27: default **off**; settings expose `system.memory_available` only, no `memory_read_only` field (read-only = 503 `memory_read_only`); user off ⇒ POST items / approve **403 `kill_switch`**; `memory_propose` is **hidden from tool definitions**, and if the model calls it anyway the tool result is a `kill_switch` error (not `unknown tool`) with nothing stored; GET / PATCH item / DELETE / export work (final decision, corrected 15:07 KST) | API spec `M20` (on profile; enables the test user once in beforeEach) + Go `qa_memory_peruser_test.go`, `qa_memory_killswitch_tool_test.go` (needs #38+#40) | see below |
| M21 | Delete-all / export | A | on |

## Profiles needed on 8002 (backend restarts via `scripts/qa-mem-mode.sh <profile>`; QA never edits config)
Specs gate on `EVE_MEMORY_PROFILE` + whether `/v1/memory` exists, not on version.
All use a throwaway dir, e.g. `/tmp/eve-qa-mem` (not under backups), and a dedicated test key file (0600, owned by gateway user, generated with `openssl rand -hex 32`). Set `EVE_MEMORY_PROFILE=<name>` when running specs.

| Profile | Config | Expected |
|---|---|---|
| `off` | `GW_MEMORY_ENABLED=false` (default) | M1/M3 |
| `on` | `GW_MEMORY_ENABLED=true`, `memory.data_dir=/tmp/eve-qa-mem`, `memory.audit_key_file=<qa key>`, `inject_cap_tokens` default | M6/M15/M16/M18/M21 |
| `readonly` | `on` + after start, operator sets the test user's `<uid>.json` to 0644 **or** rewrites its internal `user_id` | M5/M10: memory calls → 503 `memory_read_only` |
| `auditfail` | `on` with audit ledger made unwritable (e.g. audit DB role without INSERT on audit table, or audit path read-only) | M8: POST → 500, GET list unchanged |
| `key-ok` / `key-ok-nl` | key file = 64 hex without / with one trailing `\n` | gateway starts |
| `key-bad-short` | 32 hex | startup fails "32 hex chars" |
| `key-bad-binary` | 32 raw random bytes | "not hex" |
| `key-bad-space` | hex with inner space | "whitespace" |
| `key-bad-distinct` | 64 hex with ≤11 distinct bytes | "distinct bytes" |
| `key-bad-perm` | valid key, file 0644 | "must be a 0600 regular file" |

For key-* profiles the check is "starts / refuses to start" + error text in `gateway-test.log` (no key material in log). Backend reports the result; QA verifies log via `scripts/memory-fs-check.sh --log`.

## Not black-box testable
Prompt position/content (M2, M13, M14), per-file disk-access checks (M4), audit content (M9), two-identity isolation (M17) → Go tests on the merged commit.

## Go run result (2026-10-27 14:18 KST, origin/main 1c2921b)
50/50 memory Go tests pass with `scripts/memory-go-checks.sh` after the TMPDIR fix.
First run failed 27 tests: on macOS `t.TempDir()` lives under `/var/folders` (symlink → `/private/var`) and `memory.Open` refuses symlinked parents ("data dir path contains a symlink"). Test-env issue, not a product bug; but **ops note**: `memory.data_dir` must be a symlink-free path (e.g. `/private/tmp/...`, not `/tmp/...`) or the gateway refuses to start.

## Gaps of Go tests vs plan
- M2: covered (upstream messages unchanged when off) for one shape (system+user); no web-mode/off check.
- M4: numeric-ID checks only via int64 (0, -1, -42) at store level and file-name validation list; no test that a users.id lookup failure (memory_user_id_failed) blocks memory at the handler level.
- M9: plaintext check only for `memory_add`; edit/delete/delete-all/forget events not checked.
- M13: single block in a system message and at end of the system prompt; ordering vs. later user/tool messages is checked only by the fixtures used (no fuzz/multi-turn tool-call history in web mode beyond fixtures).
- M14: covered.
- M17: read/patch/delete across identities covered; export, delete-all and injection into the other user's prompt not covered.

## 8002 run (2026-10-27 14:20–14:26 KST, build 3.1.1-5-g1c2921b)
| Mode | Result |
|---|---|
| off | api-memory: 5 pass / 12 skip. `/v1/memory*` 404 identical to unknown route; version pinned OK |
| on | api-memory: 8 pass / 9 skip (CRUD, export, delete-all, no-store, 4× secret_detected 422, Korean sentence accepted; empty text accepted as 400/422 `invalid`). fs-check on `mem/`: dir 0700, `4.json`/`4.lock` 0600, numeric names |
| readonly (runtime) | after creating 1 item: `chmod 644 4.json` → GET/POST 503 `memory_read_only` + no-store (spec 2 pass); internal `user_id` changed → 503; restored → 200 |
| readonly (script restart) | **gateway refuses to start** (`data file has mode -rw-r--r--, want regular 0600`); script reports expect=start got=refuse, exit 1 |
| auditfail (runtime) | `REVOKE INSERT ON tool_audit FROM llmchat_test` (llmchat_test DB): POST and DELETE → 500 `internal_error` + no-store, nothing persisted/deleted, log `audit_write_failed … permission denied`; GRANT restored → 201 |
| auditfail (script restart) | gateway refuses to start (observed behavior) |
| key-ok / key-ok-nl | start |
| key-bad-short / binary / space / distinct / perm | refuse with "32 hex chars", "not hex", "contains whitespace", "only 11 distinct bytes", "must be a 0600 regular file" |
| key leak | no key hex found in gateway-test logs for any fixture |
End state: off, `/v1/memory` 404, GRANT INSERT verified (`has_table_privilege = t`).

## Round-4 run (2026-10-27 15:1x KST, qa/memory-p1 Go QA)

Heads verified: #38 3fa8aa1, #40 cb233e6, #34 aa843e1, #39 80f84cc, #41 c54065d.

| Tree | Test | Result |
|---|---|---|
| #38 alone | approve QA (tagged, reissue, TTL, expiry via clock, API key 403, non-pending 409) + PR tests, go test ./internal/... | PASS |
| #38+#40 merge | TestQA_PerUserMemoryIsolation incl. POST /v1/memory/items while off → 403 kill_switch | PASS (fixed; earlier FAIL row was stale) |
| #38+#40 merge | TestQA_KillSwitch_ProposeDenied: tool hidden, called anyway → kill_switch, nothing stored | PASS |
| #38+#40 merge | TestQA_KillSwitch_ApproveDenied → 403 kill_switch | PASS |
| #38+#40 merge | TestQA_MemoryDefaultOff, TestQA_SettingsFieldsContract | PASS |
| #38+#40 merge | TestQA_Approve_NonPending409 (replay 409 conflict, foreign 404) | PASS |
| #34 | corpus V1 40/25, unseen 12/15, PR V2/DenyList | PASS (FP 0, FN 0) |
| #34 | fresh (round-3 FP: HIV 기사, 성당 바자회; FN: 동료가 게이래, 여권 원거리 번호) | all fixed |
| #34 | fresh round-4 politics/health: benign 6 FP 0; sensitive 6 FN 1 | FN "지난 선거에 2번 찍었어" → now **REQUIRED** (see below) |
| #34 | TestQA_SensitiveCategory422 (422 sensitive_category, no echo, secret → secret_detected, nothing stored) | PASS |
| #39 | go test ./internal/... | PASS |

Known, not a failure: before #34 merges, #38 memory_propose with sensitive text is denied as secret_detected (sensitive_category arrives with #34).


## Required before memory default-on (architect decisions, 2026-10-27 15:18 KST)

| # | Requirement | QA test | Status |
|---|---|---|---|
| R1 | Indirect vote preference is political and always blocked ("지난 선거에 2번 찍었어", "이번 대선은 1번 뽑을 거야", "나는 항상 보수 쪽 후보 찍어", third-party "우리 남편은 매번 민주당 찍어"); benign controls pass ("선거 날 투표소 위치 알려줘", "사전투표 기간 언제야?", "후보 2번 공약 요약해줘", "기호 1번 후보 토론 영상 찾아줘") | `TestQA_VotePreferenceRequired` (strict, no QA_STRICT needed) + controls in `TestQA_SensitiveFresh` | closed (#45 e5003dd): not run against heads yet (compile-only) |
| R2 | Tool results use `error_code` only: called-anyway propose while user off returns `{"status":"denied","error_code":"kill_switch"}`, no `error` key | `TestQA_KillSwitch_ProposeDenied` | open: not run against heads yet (compile-only) |

## Round-6 run (2026-10-27, main 556ea59 = #34/#39/#40 merged)

| PR | Head | Tree | Result |
|---|---|---|---|
| #38 | e4a3017 | alone (#40 already in main) | PASS except `TestQA_KillSwitch_ProposeDenied`: tool result still `{"error":"kill_switch"}` (fixed by #46) |
| #46 | f5963c6 | on #38, + #45 | PASS all; R2 error_code-only **closed** |
| #45 | e5003dd | alone (base main) | PASS; `TestQA_VotePreferenceRequired` 4/4, fresh FP=0 FN=0; R1 **closed** |
| #46+#45+#43 | — | merged | QA PASS; only `TestListMessages_LastNOldestToNewest` fails, flaky on main too (5/10) |
| #43 | cd2ed14 | alone (base #40) | `TestQA_SettingsPatchBearer403` PASS; PerUserIsolation POST fails only because #38 not in base |
| #44 | 49bd5b2 | alone (base #38 3fa8aa1) | `go test ./internal/...` PASS |
| #41 | c79b7e7 | docs | line-153 stale text removed; error_code only. Still stale: memory.md §9.1 L237-238 ("후속 사용자별", "approve·propose 연결은 후속 PR") |

Note: after #43, `api-memory.spec.js` beforeEach enabling memory via Bearer PATCH /v1/settings gets 403; on-profile E2E needs a browser-session setup step (or pre-enabled test user).
