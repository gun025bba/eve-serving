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
