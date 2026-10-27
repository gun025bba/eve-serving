#!/usr/bin/env bash
# Go-level memory P1 checks on a clean worktree of a given ref (default origin/main). Read-only for services; no network to 8000/8001.
set -euo pipefail
REF="${1:-origin/main}"; REPO="$(git rev-parse --show-toplevel)"; WT="$(mktemp -d /tmp/eve-qa-memgo.XXXX)"
git -C "$REPO" fetch -q origin || true
git -C "$REPO" worktree add -q --detach "$WT" "$REF"
trap 'git -C "$REPO" worktree remove --force "$WT"' EXIT
cd "$WT"
RUN='TestMemory|TestMemoryDisabled_NoRoutesNoPromptChange|TestMemoryAPI_|TestMemoryAudit_|TestMemoryInject_|TestMemoryFile_|TestMemoryOpen_|TestMemoryKey_|TestMemorySecret_|TestMemoryRevise_'
go test -count=1 -run "$RUN" ./internal/memory/... ./internal/handlers/... 2>&1 | tee /tmp/eve-qa-memgo.log
