# QA Go tests for memory PRs (#33 #34 #35)
Copy into `internal/memory/` of a PR worktree and run with a non-symlink TMPDIR:
`TMPDIR=$(mktemp -d /private/tmp/x.XXXX) go test -count=1 -v -run TestQA_ ./internal/memory/`
- qa_key_boundary_test.go: key file 1023/1024 accepted, 1025 rejected, no key bytes in errors (#33)
- qa_sensitive_corpus_test.go: 40 ordinary + 25 sensitive sentences, logs FP/FN; QA_STRICT=1 fails (#34)
- qa_redact_leak_test.go: log leak paths with redactor on, unchanged when off (#35)
