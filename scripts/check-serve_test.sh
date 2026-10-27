#!/bin/sh
# Fixture tests for check-serve.sh (run in CI).
set -u
d=$(dirname "$0"); t="$d/testdata/check-serve"; fail=0
run() { # want-rc expect serve status
  EXPECT="$2" SERVE_JSON="$t/$3" STATUS_JSON="$t/$4" "$d/check-serve.sh" >/dev/null; rc=$?
  [ "$rc" = "$1" ] || { echo "FAIL: $3/$4 EXPECT=$2 rc=$rc want $1"; fail=1; }
}
U=unix:/opt/eve-gw/run/gw.sock; T=http://127.0.0.1:8000
run 0 "$U" ok.json st.json
run 1 "$U" old.json st.json
run 0 "$T" old.json st.json
run 1 "$T" ok.json st.json
run 1 "$U" fun.json st.json
run 1 "$U" two.json st.json
run 1 "$U" ok.json stf.json
: > "${TMPDIR:-/tmp}/cs-empty.json"; t=${TMPDIR:-/tmp}; cp "$d/testdata/check-serve/st.json" "$t/cs-st.json"; run 1 "$U" cs-empty.json cs-st.json
exit $fail
