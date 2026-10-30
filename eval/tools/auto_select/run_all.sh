#!/bin/sh
# Sequential driver (one request at a time against the model server on :8080). Raw runs go to runs/ (git-ignored).
# Usage: ./run_all.sh   (skips runs whose json already exists)
cd "$(dirname "$0")" || exit 1
run() { # name variant thinking [extra args]
  n=$1; v=$2; t=$3; shift 3
  [ -f runs/$n.json ] || python3 run_auto_select.py --variant $v --thinking $t --out runs/$n.json "$@" > runs/$n.log 2>&1
}
# sampling noise at temperature 0.7: /no_think suffix, two repeats; v0 = web_on, v1 = + search rules, v2 = + Gate hint
run web_on_nothink_r1 web_on off
run no_hint_nothink_r1 no_hint off
run bare_nothink_r1 bare off
run web_on_nothink_r2 web_on off
run classifier_nothink_r1 classifier off
# production default (thinking on): V0, V1, V2; V3 = V2 with enable_thinking=false per request
run web_on_think_r1 web_on on
run v1_think_r1 v1 on
run v2_think_r1 v2 on
run v2_kwargs_r1 v2 kwargs
run v1_nothink_r1 v1 off
run v2_nothink_r1 v2 off
run web_on_nodate_nothink_r1 web_on off --no-date
run v2_kwargs_r2 v2 kwargs
run no_hint_think_r1 no_hint on
echo done > runs/ALL_DONE
