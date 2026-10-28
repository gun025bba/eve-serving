#!/usr/bin/env bash
# Build QA copies of the release binaries for case 5 (rollback chain) into bin/ (gitignored). Builds from refs in throw-away worktrees; nothing is deployed.
#   EVE_REF_500 (default a419ef9 = 5.0.0, there is no v5.0.0 tag)   EVE_REF_501 (default origin/release/5.0.1)
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"; cd "$REPO"
R500=${EVE_REF_500:-a419ef9}; R501=${EVE_REF_501:-origin/release/5.0.1}
git fetch -q origin || true
for pair in "500:$R500:5.0.0" "501:$R501:5.0.1"; do
  IFS=: read -r tag ref ver <<<"$pair"
  sha=$(git rev-parse --short "$ref"); wt=$(mktemp -d /tmp/eve-rel-build.XXXXXX)
  git worktree add --detach -q "$wt" "$ref"
  ( cd "$wt" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${ver}-qa.${sha}" -o "$REPO/bin/gateway-test-${ver}" ./cmd/gateway )
  git worktree remove --force "$wt"
  echo "export EVE_BIN_${tag}=$REPO/bin/gateway-test-${ver}   # $ver-qa.$sha ($ref)"
done
echo "export EVE_BIN_PREV=$REPO/bin/gateway-test-4.0.0     # previous version (default)"
