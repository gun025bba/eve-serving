#!/usr/bin/env bash
# Self-test of cases 1 and 2 against a local mock (port 8003 must be free). Proves the bundle logic only; says nothing about a real gateway.
set -euo pipefail
cd "$(dirname "$0")/../.."
D=$(mktemp -d /tmp/eve-release-selftest.XXXXXX); export MOCK_STATE=$D/state.json; mkdir -p /tmp/eve-release-selftest
lsof -nP -iTCP:8003 -sTCP:LISTEN >/dev/null 2>&1 && { echo "port 8003 busy: not starting the mock"; exit 2; }
node release/selftest/mock-gateway.js & MP=$!; trap 'kill $MP 2>/dev/null; rm -rf "$D"' EXIT; sleep 1
export EVE_BASE_URL=http://127.0.0.1:8003 EVE_GW_CLI="$PWD/release/selftest/fake-cli.js" EVE_TEST_API_KEY=selftest-key EVE_SKIP_CHAT=1 EVE_UI_DIR=${EVE_UI_DIR:-skip} EVE_STATE_DIR=$D/rs
export EVE_QA_LOGIN_A=qa-a@test.invalid EVE_QA_LOGIN_B=qa-b@test.invalid
release/run.sh --case 1,2 --no-go --phase pre && release/run.sh --case 1,2 --no-go --phase post
