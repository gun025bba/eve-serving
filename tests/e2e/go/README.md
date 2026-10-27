# QA Go tests for memory PRs (#33 #34 #35)
Copy into `internal/memory/` of a PR worktree and run with a non-symlink TMPDIR:
`TMPDIR=$(mktemp -d /private/tmp/x.XXXX) go test -count=1 -v -run TestQA_ ./internal/memory/`
- qa_key_boundary_test.go: key file 1023/1024 accepted, 1025 rejected, no key bytes in errors (#33)
- qa_sensitive_corpus_test.go: 40 ordinary + 25 sensitive sentences, logs FP/FN; QA_STRICT=1 fails (#34)
- qa_redact_leak_test.go: log leak paths with redactor on, unchanged when off (#35)
- qa_sensitive_unseen_test.go: 12 ordinary + 15 sensitive unseen sentences (overfit check, #34)
- qa_memory_approve_test.go: copy into `internal/handlers/` (#38): tagged text rejected, reissue invalidates old token, token TTL ~120s

Note: the #34 PR ships its own TestQA_SensitiveCorpus, so ours is TestQA_SensitiveCorpusV1.
qa_redact_leak_test.go registers item text first (RegisterText, #39) and fails on any leak.

Round 3:
- qa_sensitive_fresh_test.go (#34): 14 ordinary + 11 sensitive fresh sentences, hive/archive/HIV, ARN date, passport proximity (synthetic IDs)
- qa_memory_approve_test.go (#38): + expiry boundary via Server.Now (119s ok, 120s 410), reissue/revise API key 403
- qa_memory_peruser_test.go (#40, `internal/handlers/`): A on / B off isolation, memory_available tracks global flag
- qa_backup_policy_test.go (#42, `internal/memory/`): CheckBackupPolicy tmutil output / sync-dir edge cases
- qa_memory_killswitch_tool_test.go (#38+#40, `internal/handlers/`): user off ⇒ propose / approve 403 kill_switch (needs both PRs)
- qa_memory_peruser_test.go updated per architect decision (default off, memory_available only, POST 403 kill_switch while off)
- Round 4: qa_memory_approve_test.go + TestQA_Approve_NonPending409; qa_sensitive_api_test.go (#34, `internal/handlers/`): 422 sensitive_category; fresh corpus + politics/health
- `qa_settings_bearer_test.go` (package handlers, PR #43): Bearer PATCH /v1/settings → 403 `scope_denied`, value unchanged; Bearer GET 200; browser-session PATCH 200.
- `qa_memory_propose_sensitive_test.go` (package handlers, PR #46): propose with sensitive text (third-party health, vote preference) → tool result `{"status":"denied","error_code":"sensitive_category"}`, no `error` key, no card, nothing stored. Vote case also needs #45.
- `qa_tombstone_test.go` (package memory, PR #52): a retried Execute of the same action must not re-add a tombstoned item; undeleted items stay.
- `qa_memory_reapprove_test.go` (package handlers, PR #53): re-approve of an executed card → 409 `{"error_code":"conflict"}` (twice), reissue → 409 conflict, item written once.

- `qa_sensitive_fresh_test.go` R1 (architect policy 16:04 KST): strict gate `TestQA_VotePreferenceRequired` (public elections, 기호, party+stance incl. punctuation/emoji, ambiguous "선거에서 N번"); strict 0-FP `TestQA_NonPublicVoteAllowed` (non-public votes + stance-free political topics); report-only `TestQA_PoliticalRecallReport` (unseen recall, 도장 policy-ambiguous log).- `qa_political_holdout_test.go`: report-only Round 11 holdout (`TestQA_PoliticalHoldoutReport`, copy into internal/memory).

- `qa_holdout_file_test.go` (package memory): `TestQA_PrivateHoldoutFile` reads private TSV holdouts from `QA_HOLDOUT_FILES` (colon-separated, kept outside the repo, never committed) and logs counts plus misses.
