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
| R1 | **Default-on gate (architect 2026-10-27 16:04 KST)**: block public-office elections (대선·총선·지방선거·재보궐선거·국민투표·사전투표, 기호 N번) and party/candidate support, plus ambiguous no-context votes ("선거에서 N번 찍었어"); **all** core cases blocked AND **0 FP** on non-public votes (반장·동아리·회의·아파트 대표·학생회·오디션·인기투표·사진·주차·서류 도장) and stance-free political topics. Not 100% of unseen sentences: deny list is best-effort, the approval card is the final safeguard; unseen recall is report-only. 도장+number without election context is policy-ambiguous (logged) | strict: `TestQA_VotePreferenceRequired`, `TestQA_NonPublicVoteAllowed`; report-only: `TestQA_PoliticalRecallReport` | open: restructured, compile-only, awaiting new #53 head |
| R2 | Tool results use `error_code` only: called-anyway propose while user off returns `{"status":"denied","error_code":"kill_switch"}`, no `error` key | `TestQA_KillSwitch_ProposeDenied` | closed: PASS on #46 f5963c6 (Round-6) |

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

## Round-7 run (2026-10-27, main 1e3d463 = #38/#43/#44 merged)

Each PR tested merged onto main 1e3d463 with all QA Go tests, `QA_STRICT=1`. Failures listed are only cross-PR dependencies.

| PR | Head | Result |
|---|---|---|
| #45 | 6633c65 | memory pkg PASS: R1 4/4, `TestQA_VoteNonElectionAllowed` 11/11, fresh FP=0 FN=0. Handlers fail only on #46 items (legacy `error` key; propose sensitive → `secret_detected`) |
| #46 | 5f7f2bd | R2 PASS; propose sensitive_category PASS for health; vote case needs #45 |
| #47 | e60a9e2 | PASS (redactor lows: per-user over-cap degrade, register after write, nested unescape) |
| #48 | 01ea49b | PASS (refuse macOS data_dir under ~/Desktop or ~/Documents) |
| #49 | 5cbedcb | PASS (secret filter homoglyph fold, 3-level decode) |
| main+#45..#49+#51 | — | **all PASS** (go test ./internal/... + all QA, strict) |
| #51 | eb06fc8 | `TestListMessages_LastNOldestToNewest`: main 1e3d463 fails 2/10 (-count=10) and 4/10 (separate runs); #51 0/10 and 0/10 |
| #41 | 90c656a | §9.1 stale per-user/approve-follow-up text gone; remaining "후속" refers only to encrypted backup (correct). Gaps: Q14 enum (contract L31) lacks `sensitive_category`; PATCH /v1/settings Bearer 403 (#43) not in ui-api-contract |
| #50 | 6a1d3c8 | script reviewed, **not run**: 8002 is gateway 3.1.1-5-g1c2921b with GW_MEMORY_ENABLED=false, and llmchat_test `users` has no `settings` column (migrations not applied). Needs a test deploy + migration first |

#50 review: DB is a hard-coded constant `llmchat_test` (the guard is a tautology but not overridable; PGHOST/PGPORT env could still redirect psql). No DELETE; it does `INSERT users(login) ON CONFLICT DO NOTHING` (creates the user if absent) outside a transaction, so a failing UPDATE leaves that row.

UI 1.2.0 is on 8002 (`/ui/` appVersion v1.2.0). With EVE_EXPECT_UI_VERSION=1.2.0: api-smoke, api-retry (resume/regenerate), api-search-proactive pass (7); all UI specs (resume, stop/retry, proactive card, settings version) skip without browser identity; login error text not checkable without identity injection.

## Round-8 run (2026-10-27, main 64a4c4c = #41/#45-#49/#51 merged)

Each PR merged onto main, `go test ./...` + all QA Go tests, `QA_STRICT=1`.

| PR | Head | Result |
|---|---|---|
| #52 | b25a1e7 | Bearer PATCH /v1/settings 403 `scope_denied`, session PATCH 200 (shared `isBrowserSession` allowlist with approve/revise/reissue); `TestQA_TombstoneNotReAdded` PASS (Execute skips IDs in `d.Tombstones`). Only fail: benign "팀 대표 뽑았어" blocked on main (fixed by #53) |
| #53 | 0125334 | **FAIL (R1 regression)**: "기호 2번 찍었어" and "기호2번 찍었어" / "기호 ２번 찍었어" / "기호 2 번 찍었어요" now pass `CheckSensitive`; all four were blocked on main. Jamo "민ㅈㅜ당 지지해", spacing, zero-width and benign controls are fine. Own tests (64-variant cap, data-dir fail-closed, over-cap gauge) pass. Tombstone QA fails alone only because #52 is not in its base |
| main+#52+#53 | — | Same R1 failure only; everything else PASS |
| #50 | 011acfc | Static: INSERT removed, `psql -1` single transaction, exit 3 if `users.settings` is missing or the user is missing (verified live: unknown login → rc 3, no row created). Still: the `[ "$DB" = llmchat_test ]` guard is always true and there is no PGHOST/PGPORT check. #53's copy adds a server-reported check (`current_database()`, local `inet_server_addr()`, port = QA_PGPORT default 5432), which fixes the PGHOST/PGPORT point. Minor: the column check does not filter `table_schema` |

### 8002 memory E2E (gateway 3.1.1-79-g64a4c4c, llmchat_test goose 7, UI v1.2.0)

Script: the 011acfc copy (#50 not merged). QA user login `api-key`. `api-memory.spec.js` no longer PATCHes settings with Bearer (403 since #43); user toggle is out of band, new profile `useroff`.

| Step | Result |
|---|---|
| Full API regression, server off (EVE_EXPECT_UI_VERSION=1.2.0) | 19 passed (before and after the memory runs) |
| `on` + user off (`useroff`) | 3/3: POST 403 `kill_switch`; list/export 200; PATCH/DELETE not kill-switched (404 for unknown id); Bearer PATCH settings 403 |
| `on` + `user-on` (`on`) | 9/9: CRUD, no-store, secret 422, settings `memory_available` true, no `memory_read_only` |
| `user-off` again (`useroff`) | 3/3 |
| server `off` + user on | 6/6; POST items 404 (server switch wins) |
| Restore | user-off → `off`; 0 users with memory on, GW_MEMORY_ENABLED=false, health OK. No data deleted except the spec's own items |

## Round-9 run (2026-10-27, main 1edcf6a = #50/#52 merged; #53 902297a)

main+#53, `go test ./...` + all QA Go tests, `QA_STRICT=1`: everything PASS except 3 new political variants.

| Item | Result |
|---|---|
| R1 기호 variants (4) + earlier must-block list | PASS (blocked) |
| New round-9 variants | **FAIL 3/9 not blocked**: "투표는 2번에 했어", "조국혁신당, 지지해" (comma), "녹색당…지지함" (ellipsis). Blocked: "기호 3번 찍을게", "정의당 지지.", "개혁신당 지지!", "진보당을 지지해요", "국민 의 힘 찍었다", "더불어 민주당 뽑았어" |
| Benign controls (party news, who are the candidates, 기호 3번 후보 이름, 팀 대표 뽑았어 …) | PASS (all allowed) |
| Re-approve executed card | `TestQA_ReapproveExecuted409` PASS: 409 `{"error_code":"conflict"}`; reissue also 409 conflict; written once |
| qa-mem-mode.sh (#53) static | DB constant; unsets PGSERVICE/PGSERVICEFILE/PGDATABASE/PGHOSTADDR/PGOPTIONS; PGHOST/PGPORT only from QA_PGHOST/QA_PGPORT; post-connect check of db/local host/port; column check filtered by `table_schema=current_schema()`. Minor: PGUSER/PGPASSFILE still inherited; a non-local QA_PGHOST is rejected only after connecting |
| qa-mem-mode.sh live (8002 llmchat_test, user `api-key`) | user-on rc 0 → user-off rc 0. PGDATABASE=llmchat, PGSERVICE, PGHOST=203.0.113.9, PGOPTIONS, DB=llmchat are ignored (still llmchat_test). Bad QA_PGPORT / QA_PGHOST → "cannot connect" rc 3. Restored: 0 users on, GW_MEMORY_ENABLED=false |

## Round-10 run (2026-10-27, main 1edcf6a; #53 adf4b72)

main+#53, `go test ./...` + all QA Go tests, `QA_STRICT=1`. Round-9 misses ("투표는 2번에 했어", "조국혁신당, 지지해", "녹색당…지지함") now blocked; all earlier tests PASS.

`TestQA_PoliticalUnseenR10` (fresh, unseen): **FAIL 10/17 not blocked**
- punctuation: "정의당·지지해", "민주당/지지함", "국민의힘~ 찍었음", "진보당? 지지자야", `"개혁신당" 지지해`, "'조국혁신당' 찍었어", "국민의힘 👍 지지" (all 7 punctuation forms)
- vote number: "2번에 도장 찍었어", "투표용지에 1번 찍었어", "사전투표 때 1번에 했어"
- blocked OK: "녹색당 지지자임", "민주당 찍었음", "나 정의당 지지함", "우리 아빠는 국민의힘 지지자야", "대선 때 3번 뽑았음", "총선에서 2번 찍음", "이번 투표는 1번에 찍었다"
- benign 9/9 allowed

Policy questions (logged only): "회의에서 투표 2번 했어" allowed; "반장 투표는 2번에 했어" **blocked**; "동아리 회장 선거에서 1번 찍었어" allowed; "투표용지에 1번" allowed.

qa-mem-mode.sh (#53 adf4b72): unsets PGUSER/PGPASSFILE/PGSSLMODE too. Remote QA_PGHOST refused with exit 2 before any psql call (fake psql/curl/nc/ssh on PATH recorded no calls) for 203.0.113.9, db.example.invalid, "localhost,203.0.113.9", "203.0.113.9,/tmp", "127.0.0.1x". **Gap (Low)**: comma host list "/tmp,203.0.113.9" passes the pre-check (matches `/*`) and psql is invoked; libpq would fall back to the remote host if the socket fails (the post-connect check still refuses). Live on 8002 llmchat_test: user-on rc 0, user-off rc 0; restored (0 users on, GW_MEMORY_ENABLED=false).

## R1 restructure (2026-10-27 16:04 KST, compile-only)

Per architect policy (see R1 row). Earlier round-8/9/10 strict tests (`TestQA_PoliticalVariants`, `TestQA_VoteNonElectionAllowed`, `TestQA_PoliticalUnseenR10`) are folded into the gate / 0-FP / report-only sets. Backend plan for #53: normalize punctuation/symbols/emoji to spaces; party + ≤~8 chars + stance verb; (투표|투표용지|사전투표|도장|기호)+number+verb with exclusions 오디션/인기투표/주차/사진. 도장+number without election context ("2번에 도장 찍었어") is policy-ambiguous (logged); "이 서류 2번에 도장 찍어야 해?" stays allowed. Compiles on main 1edcf6a (go vet OK); not run against a new #53 head yet.

## Round 11 — #53 (9e6c43a) holdout
- go test ./... QA_STRICT=1: 081a31c suite PASS, 00e5985 suite PASS (unseen recall 12/12).
- New holdout `tests/e2e/go/qa_political_holdout_test.go` (report-only, first run before commit):
  core public 16/19 blocked (miss: 새로운미래 지지해요, 자유통일당 찍었어, 민주당 후보한테 표 줬어) — gate FAIL;
  recall 4/12; non-public allow FP 10/34 (학교/아이돌×2/점심/회식/여행지/MVP 투표, 노조 위원장 선거, 정당 가입 절차 질문, 계약서 2번에 도장) — gate FAIL.
- 도장: "2번에 도장 찍었어" blocked; "도장 3번 찍었어", "이 서류 2번에 도장 찍어야 해?", "도장을 2번에 찍었어" allowed; "계약서 2번에 도장 찍었어" FP.
- QA_PGHOST "/tmp,203.0.113.9" refused pre-connect (0 calls); "localhost,/tmp" all-local → accepted.
- 8002 llmchat_test user-on/off OK, restored to off (0 users on, health ok).

## Round 12 — #53 (1af50a9)
- go test ./... QA_STRICT=1 with 2e9da67 suite: PASS.
- Committed R11 holdout (now public, report-only): core 19/19, recall 11/12, allow FP 0/34.
- Gate now uses a PRIVATE holdout kept outside the repo (never committed or shared; sentence-level failures only may be shared).
  R12 private set: 30 core / 40 allow → core blocked 25/30, allow FP 0/40 — gate FAIL (core misses).

### R13 — PR #53 @ 59169ef (merged onto main 1edcf6a), private holdout
- Public suite (2e9da67 + latest qa/memory-p1, QA_STRICT=1): all TestQA_* pass; holdout report core 19/19, FP 0/34, recall 9/12.
- Private r12 (gate per architect R1, 초성/English-mixed reclassified recall): core 26/27, FP 0/40, recall 0/3.
- Private r13 (new): core 17/20, FP 0/40, recall 2/10.
- Gate: FAIL (core 43/47 blocked, FP 0/80).

### R14 — PR #53 @ f57f1b2 (merged onto main 1edcf6a), private holdout
- Public suite (2e9da67 + latest qa/memory-p1, QA_STRICT=1): all TestQA_* pass; holdout report core 19/19, FP 0/34, recall 10/12.
- Private r12 (초성/English-mixed counted as recall): core 27/27, FP 4/40, recall 0/3.
- Private r13: core 19/20, FP 4/40, recall 2/10.
- Private r14 (new): core 19/20, FP 5/40, recall 1/10.
- Gate: FAIL (core 65/67 blocked, FP 13/120).

### Round 15 — #53 head 5d66979 (private holdout r15, new sentences): FAIL
- r15: core 60/66, non-public FP 3/143, recall 7/15. Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 10/12.
- Required checks failed: 「개헌 국민투표는 반대에 한 표」 and 「민주 지지해요」 are not blocked.
- Other core misses: 「기호 3번 지지해」, 「국민투표에서 찬성에 한 표」, 「대선 후보 중 2번 지지해」, 「민주당 후보에게 한 표」 (지지/한 표 without a 찍/뽑 verb).
- FPs: 「친구들이랑 영화 고르는 투표 1번 뽑았어」 (earlier FP, still blocked), 「남편은 회사 노조 선거에서 1번 뽑았대」, 「강아지 이름 정하는 투표 3번 뽑았어」.
- r12–r14 reruns: core misses only in recall-class (초성/English) except 「민주 지지해요」 and 「개헌 국민투표는 반대에 한 표」; r14 FP 1 (the movie sentence above).

### Round 16 — #53 head 1497c27 (private holdout r16, new sentences): FAIL
- r16 (gate): core 51/65, non-public FP 1/159, recall 3/12. r15 core 66/66, FP 0/143, recall 7/15. r13/r14 core 20/20, FP 0. r12 misses only 초성/English (recall-class).
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 10/12.
- Core misses: 「총선은 국민의힘에 한 표야」 「개헌 국민투표에 찬성표 냈어」 「정의당 밀어주기로 했어」 「대선에서 2번 밀어줬어」 「총선 때 1번 밀어줌」 「기호 3번 밀어줬어요」 「지방선거 3번에 기표」 「작년에 조국혁신당 입당했어」 「개혁신당에 입당했어요」 「민주당 권리당원입니다」 「사전투표에서 기호 2번 기표」 「엄마는 대선 때 2번 뽑으셨대」 「민주당 찍는다」 「민주당 후보를 밀어요」.
- FP: 「우리 아빠는 노조 조합원이셔」.

### Round 17 — #53 head c56323c (code 6de417e), gate r16 + r17: FAIL
- r17 (new): core 27/63, non-public FP 2/174, recall 1/10; news/third-person/factual FP 2/25 (flagged for PM).
- r16: core 60/65, FP 1/159, recall 4/12. r15 66/66, FP 0/143. r13/r14 20/20, FP 0. r12 misses only 초성/English.
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 10/12.
- Misses cluster on unshared verbs (선택/골랐/응원/편이야/표 몰아주/~으로 했어·갔어/투표 완료/뽑아드렸어), identity nouns (지지층/지지자에요/성향/당비/대의원), 밀어주/기표 still partial, 권리당원.
- FP: 노조 조합원 (2 in r17, 1 in r16); news: 「민주당이 개정안에 반대표 던졌다」 「그 의원이 국민의힘 탈당했대」.

### Round 18 — #53 head da063f5 (code e7544f2, default-block), gate r16+r17+r18: FAIL (FP)
- Core: r18 64/64, r17 63/63, r16 65/65 (recall criterion met). Non-public FP: r18 13/202, r17 1/174, r16 1/159.
- Reference: news FP r18 4/22, r17 4/25; first-person info questions FP 14/20. r12 adds FP 「우리 동네 투표소 어디야?」.
- r18 FP patterns: 사전투표 used for non-public votes (3), '후보' as a site candidate (2) and 동창회장 후보, 지지자 with non-public anchor (3), 노조 조합원/가입 (3), 「새로운 미래 계획 세웠어」.
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 11/12.

### Round 19 — #53 head 1c2f6b7 (code 8e08c18), gate r16–r19: FAIL (FP)
- Core: r19 63/63, r18 64/64, r17 63/63, r16 65/65. Non-public FP: r19 48/203, r18 7/202 (shared sentences still blocked), r17 1/174, r16 1/159.
- r19 FP patterns: 사전투표 with a non-public org (22), 기호 N번 with a non-public org (15), 지지자 with a non-public org (12 incl. r18), plus 「사전 투표 아니고 사전 주문 2번 했어」; borderline decoys 「정의당이라는 카페 이름 웃기다」 「조국을 위한 노래 들었어」. The org bypass covers only a small fixed org list.
- Ambiguous bypass probe (public vote with a non-public companion word): 13/15 blocked; leaks 「회사 동료랑 사전투표 가서 2번 찍었어」 「아파트 이웃들 다 기호 3번 지지자야」.
- Reference: questions FP r19 4/22 (all -지?), r18 3/20; news FP r19 3/20, r18 4/22, r17 6/25.
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 11/12.

### Round 20 — #53 head 7a6eddd (code b5d18fe), gate r16–r20 with PM relabel: FAIL (FP)
- Relabel: self/family union membership → should-block (12 sentences across r16–r19); news: only -대/-래 endings allow; -지? blocked only with an evaluative word.
- Core: r20 61/61, r19 63/63, r18 64/64, r17 63/63, r16 65/65. Union membership: 5/12 blocked (7 leak).
- Non-public FP: r20 12/220, r19 16/201, r18 3/197, r17 0/170, r16 0/158.
- FP patterns: '[group] 회장 N번 지지자' family statements (22), 사전투표 with a group (3), 기호/사전투표 as an everyday word (3), decoys with 조국/정의당 (3).
- Ambiguous probe: r20 18/20, r19 14/15.
- Reference: info-question FP r20 5/12, r19 4/22, r18 4/20; evaluative -지? 4/6 blocked; news -대/-래 FP r20 2/10, r19 0/13, r18 1/14, r17 4/18.
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 11/12.

### Round 21 — #53 head aff335b (code 46731c4), gate r16–r21 with PM relabel: FAIL
- Should-block (core + union membership): r21 55/63 (87.3%; core 45/45, union 10/18), r20 60/61, r19 64/65 (core 62/63, union 2/2), r18 68/69 (core 64/64, union 4/5), r17 65/67 (core 63/63, union 2/4), r16 66/66.
- Non-public FP: r21 11/205, r20 1/220, r19 2/201, r18 1/197, r17 0/170, r16 0/158 (total 15).
- Union leaks (11): 사무장 (2), 일/집회/활동 without title (2), 교육부장, 상근자, 전임자, 한국노총 소속, 조합비 (2), 조합원 교육.
- FP patterns: '우리 [group]는 회장 선거 기호 N번 찍었어' (11), decoys 조국/정의당 (3), 「우리 아빠 노조 위원장 2번 골수 지지야」.
- Core leaks on older sets: 「기호 3번 줬어요」 「기호 2번 쪽에 표 넣음」.
- Ambiguous probe: r21 13/15, r20 18/20, r19 13/15.
- Reference: info-question FP r21 3/12, r20 4/12, r19 4/22, r18 4/20 (투표소 questions in r21 all allowed); evaluative -지? r21 3/4, r20 4/6 blocked; news -대/-래 FP r21 2/8, r20 2/10, r19 0/13, r18 1/14, r17 4/18.
- Strict suite (QA_STRICT=1) passes; public 2e9da67 holdout core 19/19, FP 0/34, recall 11/12.
