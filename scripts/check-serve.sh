#!/bin/sh
# Read-only check of the `tailscale serve` config (4.0.0 compensating control).
# Exit 0 only if serve has exactly one handler, "/" -> $EXPECT, on HTTPS 443,
# funnel is off, and this node has no funnel capability. Otherwise exit 1
# (2 = could not read state). Changes nothing.
#
#   scripts/check-serve.sh                                   # 4.0.0: same loopback target
#   EXPECT=http://127.0.0.1:8000 scripts/check-serve.sh      # before cutover (3.x)
# Env: EXPECT, TAILSCALE (CLI path), SERVE_JSON / STATUS_JSON (files instead of the CLI, for tests).
set -u
EXPECT="${EXPECT:-http://127.0.0.1:8000}"
TS="${TAILSCALE:-tailscale}"
tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT
if [ -n "${SERVE_JSON:-}" ]; then cp "$SERVE_JSON" "$tmp/serve.json" || exit 2
else "$TS" serve status --json > "$tmp/serve.json" 2>/dev/null || { echo "check-serve: cannot read serve status" >&2; exit 2; }; fi
if [ -n "${STATUS_JSON:-}" ]; then cp "$STATUS_JSON" "$tmp/status.json" || exit 2
else "$TS" status --json > "$tmp/status.json" 2>/dev/null || { echo "check-serve: cannot read status" >&2; exit 2; }; fi

EXPECT="$EXPECT" /usr/bin/env python3 - "$tmp/serve.json" "$tmp/status.json" <<'PY'
import json, os, sys
exp = os.environ["EXPECT"]
bad = []
try:
    sv = json.load(open(sys.argv[1])) if os.path.getsize(sys.argv[1]) else {}
    st = json.load(open(sys.argv[2]))
except Exception as e:
    print("check-serve: unparsable json:", e, file=sys.stderr); sys.exit(2)
sv = sv or {}
handlers = []
for host, web in (sv.get("Web") or {}).items():
    for path, h in ((web or {}).get("Handlers") or {}).items():
        handlers.append((host, path, h))
if len(handlers) != 1:
    bad.append("expected exactly 1 serve handler, found %d" % len(handlers))
else:
    host, path, h = handlers[0]
    if path != "/": bad.append("handler path is %r, want '/'" % path)
    if set(h) - {"Proxy"} or h.get("Proxy") != exp:
        bad.append("handler is %r, want Proxy=%s" % (h, exp))
    if not host.endswith(":443"): bad.append("handler on %s, want :443" % host.rsplit(":",1)[-1])
tcp = sv.get("TCP") or {}
if set(tcp) != {"443"} or tcp.get("443") != {"HTTPS": True}:
    bad.append("TCP config is %r, want only 443 HTTPS" % tcp)
if any((sv.get("AllowFunnel") or {}).values()): bad.append("funnel is ON")
for k in ("Services", "Foreground"):
    if sv.get(k): bad.append("unexpected %s config" % k)
caps = list(((st.get("Self") or {}).get("CapMap") or {})) + list((st.get("Self") or {}).get("Capabilities") or [])
fc = sorted({c for c in caps if c == "funnel" or c.endswith("/cap/funnel") or "funnel-ports" in c})
if fc: bad.append("node has funnel capability: %s" % ",".join(fc))
if bad:
    for b in bad: print("check-serve: FAIL:", b)
    sys.exit(1)
print("check-serve: OK (/ -> %s, funnel off, no funnel capability)" % exp)
PY
