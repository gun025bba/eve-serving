#!/usr/bin/env bash
# Filesystem checks for memory data dir on the 8002 test instance (read-only). Usage: memory-fs-check.sh <data_dir> [--log <gateway-test.log>]
set -uo pipefail
D="${1:?data_dir}"; fail=0
[ -d "$D" ] || { echo "MISSING $D (expected when memory off: OK)"; exit 0; }
p=$(stat -f '%Lp' "$D" 2>/dev/null || stat -c '%a' "$D"); [ "$p" = 700 ] && echo "ok dir 0700" || { echo "FAIL dir perm $p"; fail=1; }
for f in "$D"/* "$D"/.[!.]*; do [ -e "$f" ] || continue; n=$(basename "$f")
  [ -L "$f" ] && { echo "FAIL symlink $n"; fail=1; continue; }
  [[ "$n" =~ ^[1-9][0-9]*\.(json|lock)$ ]] || { echo "FAIL name $n"; fail=1; }
  p=$(stat -f '%Lp' "$f" 2>/dev/null || stat -c '%a' "$f"); [ "$p" = 600 ] || { echo "FAIL perm $n $p"; fail=1; }
done
if [ "${2:-}" = "--log" ]; then L="${3:?log}"
  grep -E 'memory|audit key' "$L" | tail -20 | sed -E 's/[0-9a-fA-F]{32,}/<hex-redacted>/g'
fi
exit $fail
