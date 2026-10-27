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
