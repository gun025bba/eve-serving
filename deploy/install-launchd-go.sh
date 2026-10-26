#!/bin/bash
# Render launchd plists for the Go gateway (same labels as the old uvicorn agents).
#
# Loopback-only mode (recommended; expose with `tailscale serve`, see README "Auth"):
#   ALLOWED_USERS=<login>@github deploy/install-launchd-go.sh
#   -> writes com.llmserving.gateway (127.0.0.1) and disables com.llmserving.gateway-ts
#      (its plist is moved to *.plist.prev; run the printed bootout yourself).
# Legacy mode (also bind directly to a tailnet IP, API key only on that listener):
#   BIND_IP=<tailscale-ip> ALLOWED_USERS=... deploy/install-launchd-go.sh
#
# Backups: the first Python-era plist stays in *.plist.py-bak; the plist being
# replaced is always copied to *.plist.prev so the last step can be reverted.
# Nothing is loaded/unloaded by this script.
set -euo pipefail
ALLOWED_USERS="${ALLOWED_USERS:-}"
# Conversation storage: GW_DATABASE_URL is read from .db_env (written by
# deploy/pg-setup.sh, chmod 600, gitignored). Missing file = persistence off.
DB_ENV="${DB_ENV:-$HOME/llm-serving/.db_env}"
DATABASE_URL=""
if [ -f "$DB_ENV" ]; then
  DATABASE_URL="$(sed -n 's/^GW_DATABASE_URL=//p' "$DB_ENV")"
fi
t="$(dirname "$0")/launchd-go/gateway.plist.template"
dir="$HOME/Library/LaunchAgents"
backup() {
  [ -f "$1" ] || return 0
  [ -f "$1.py-bak" ] || ! grep -q uvicorn "$1" || cp "$1" "$1.py-bak"
  cp "$1" "$1.prev"
}
render() { # label host logname
  out="$dir/$1.plist"
  backup "$out"
  sed -e "s#__HOME__#$HOME#g" -e "s#__BIND_HOST__#$2#g" -e "s#__LABEL__#$1#g" -e "s#__LOG__#$3#g" \
      -e "s#__ALLOWED_USERS__#$ALLOWED_USERS#g" -e "s#__DATABASE_URL__#$DATABASE_URL#g" "$t" > "$out"
  chmod 600 "$out"; echo "wrote $out"
}
render com.llmserving.gateway 127.0.0.1 gateway
if [ -n "${BIND_IP:-}" ]; then
  render com.llmserving.gateway-ts "$BIND_IP" gateway-ts
else
  ts="$dir/com.llmserving.gateway-ts.plist"
  if [ -f "$ts" ]; then
    backup "$ts"; rm "$ts"
    echo "loopback-only: moved $ts -> $ts.prev"
    echo "  run: launchctl bootout gui/\$(id -u)/com.llmserving.gateway-ts"
  fi
fi
[ -z "$DATABASE_URL" ] && echo "note: no GW_DATABASE_URL in $DB_ENV -> persistence disabled" >&2
[ -z "$ALLOWED_USERS" ] && echo "note: ALLOWED_USERS empty -> tailscale-identity auth disabled (API key only)" >&2
exit 0
