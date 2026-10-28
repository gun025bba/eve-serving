#!/usr/bin/env bash
# Entry point of the 5.0.x post-deploy bundle. Defaults to the QA instance (8002). Never targets 8000/8001
# unless EVE_TARGET=prod-ro EVE_ALLOW_PROTECTED_PORT=1 (read-only subset, GET/HEAD only).
set -euo pipefail
cd "$(dirname "$0")/.."
[ -d node_modules/@playwright ] || { echo "run: cd tests/e2e && npm install && npx playwright install webkit chromium" >&2; exit 2; }
exec node release/run.js "$@"
