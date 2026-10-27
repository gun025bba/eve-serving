#!/bin/bash
# Pre-bootstrap guard: refuse a gateway plist that would crash-loop because
# tailscale-identity is enabled with an empty allowlist (startup error since #58).
#
#   deploy/check-auth-env.sh <gateway.plist> [config.yaml]
#
# OK when either:
#   - GW_AUTH_PROVIDERS (plist env, else config auth.providers, else the
#     built-in default "tailscale-identity,api-key") does not include
#     tailscale-identity, or
#   - GW_ALLOWED_USERS (plist env, else config allowed_users) has at least one
#     non-blank login.
# Prints no login values. Exit 1 = do not bootstrap. Run with sudo for
# root-owned LaunchDaemon plists/configs.
set -euo pipefail
P="${1:?usage: $0 <gateway.plist> [config.yaml]}"
C="${2:-}"
[ -f "$P" ] || { echo "check-auth-env: missing plist $P, stop" >&2; exit 1; }
[ -z "$C" ] || [ -f "$C" ] || { echo "check-auth-env: missing config $C, stop" >&2; exit 1; }

env_get() { # key -> value on stdout; exit 1 if the key is absent
  if command -v plutil >/dev/null 2>&1 && plutil -help 2>&1 | grep -q extract; then
    plutil -extract "EnvironmentVariables.$1" raw -o - "$P" 2>/dev/null
  else
    python3 - "$P" "$1" <<'PY'
import plistlib, sys
with open(sys.argv[1], "rb") as f:
    env = plistlib.load(f).get("EnvironmentVariables", {})
if sys.argv[2] not in env:
    sys.exit(1)
print(env[sys.argv[2]])
PY
  fi
}
# Non-blank comma-separated entries (same as config.splitList).
count() { printf '%s' "$1" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -c . || true; }
# Inline YAML list under a top-level key, or under auth: for providers.
cfg_list() { # key [parent]
  [ -n "$C" ] || return 1
  awk -v k="$1" -v p="${2:-}" '
    /^[^[:space:]#]/ { top=$1; sub(/:.*/,"",top) }
    { line=$0; sub(/#.*/,"",line) }
    p=="" && line ~ "^"k":"            { sub("^"k":[[:space:]]*",""   ,line); print line; found=1 }
    p!="" && top==p && line ~ "^[[:space:]]+"k":" { sub("^[[:space:]]+"k":[[:space:]]*","",line); print line; found=1 }
    END { exit found?0:1 }' "$C" | tr -d '[]"'"'"
}

if prov=$(env_get GW_AUTH_PROVIDERS) && [ -n "$prov" ]; then src=plist
elif prov=$(cfg_list providers auth); then src=config
else prov="tailscale-identity,api-key"; src=default; fi
if ! printf '%s' "$prov" | tr ',' '\n' | sed 's/[[:space:]]//g' | grep -qx tailscale-identity; then
  echo "check-auth-env: ok (providers from $src exclude tailscale-identity)"; exit 0
fi
n=0
if users=$(env_get GW_ALLOWED_USERS) && [ -n "$(printf '%s' "$users" | tr -d '[:space:],')" ]; then
  n=$(count "$users"); usrc=plist
elif users=$(cfg_list allowed_users); then
  n=$(count "$users"); usrc=config
fi
if [ "$n" -gt 0 ]; then
  echo "check-auth-env: ok (tailscale-identity with $n allowed login(s) from $usrc)"; exit 0
fi
cat >&2 <<MSG
check-auth-env: ABORT. tailscale-identity is enabled (providers from $src) but
GW_ALLOWED_USERS is empty/missing in $P${C:+ and allowed_users is empty in $C}.
The gateway would refuse to start and launchd would crash-loop it. Fix one of:
  - set a non-empty GW_ALLOWED_USERS in the plist EnvironmentVariables, or
  - set GW_AUTH_PROVIDERS=api-key for API key only.
Do not bootstrap until this passes.
MSG
exit 1
