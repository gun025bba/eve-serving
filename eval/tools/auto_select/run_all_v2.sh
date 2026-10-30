#!/bin/sh
# Sequential driver for the #158 frozen-prompt runs (one request at a time against the model server on :8080). Raw runs go to runs/ (git-ignored); skips runs whose json exists.
# Usage: ./run_all_v2.sh
cd "$(dirname "$0")" || exit 1
run() { # name script args...
  n=$1; shift
  [ -f runs/$n.json ] || python3 "$@" --out runs/$n.json > runs/$n.log 2>&1
}
G="run_groups.py"
# a) 3 repeats, thinking off (kwargs), frozen #158 prompt, original 130 + group C 50 + holdout 120
for r in 1 2 3; do
  run p158_kwargs_orig_r$r $G --variant p158 --thinking kwargs --items items.jsonl
  run p158_kwargs_c_r$r $G --variant p158 --thinking kwargs --items items_c.jsonl
  run p158_kwargs_h_r$r $G --variant p158 --thinking kwargs --items items_h.jsonl
done
# b) thinking on (what the gateway sends today), repeat 1
run p158_think_h_r1 $G --variant p158 --thinking on --items items_h.jsonl
run p158_think_c_r1 $G --variant p158 --thinking on --items items_c.jsonl
run p158_think_orig_r1 $G --variant p158 --thinking on --items items.jsonl
# c) group D (calendar/reminders), all 7 tools exposed
run p158_kwargs_d_r1 $G --variant p158 --thinking kwargs --tools pim --items items_d.jsonl
run p158_think_d_r1 $G --variant p158 --thinking on --tools pim --items items_d.jsonl
# d) V4 emulation (classifier call + decision request), decision with thinking off
run v4_kwargs_orig_r1 $G --variant v4 --thinking kwargs --items items.jsonl
run v4_kwargs_c_r1 $G --variant v4 --thinking kwargs --items items_c.jsonl
run v4_kwargs_h_r1 $G --variant v4 --thinking kwargs --items items_h.jsonl
# e) thinking on, repeats 2 and 3 on holdout + group C
for r in 2 3; do
  run p158_think_h_r$r $G --variant p158 --thinking on --items items_h.jsonl
  run p158_think_c_r$r $G --variant p158 --thinking on --items items_c.jsonl
done
# f) V4 with a thinking-on decision request, holdout
run v4_think_h_r1 $G --variant v4 --thinking on --items items_h.jsonl
echo done > runs/V2_DONE
