#!/bin/bash
# Pre-bootstrap guard: refuse a gateway plist + config that would crash-loop
# (tailscale-identity with 0 allowed logins is a startup error since #58).
#
#   deploy/check-auth-env.sh <gateway-binary> <gateway.plist> <config.yaml>
#
# All parsing is done by the gateway itself (`-check-auth-config`), with the
# same loader and env overlay rules as at startup; the plist's
# EnvironmentVariables are the only env used. Output is provider names and
# counts only, never login values. Exit 1 = do not bootstrap. Run with sudo for
# root-owned LaunchDaemon plists/configs.
set +x                       # never trace (no values pass through here anyway)
set -euo pipefail
GW="${1:?usage: $0 <gateway-binary> <gateway.plist> <config.yaml>}"
P="${2:?usage: $0 <gateway-binary> <gateway.plist> <config.yaml>}"
C="${3:?usage: $0 <gateway-binary> <gateway.plist> <config.yaml>}"
[ -x "$GW" ] || { echo "check-auth-env: gateway binary $GW missing or not executable, stop" >&2; exit 1; }
[ -f "$P" ]  || { echo "check-auth-env: missing plist $P, stop" >&2; exit 1; }
[ -f "$C" ]  || { echo "check-auth-env: missing config $C, stop" >&2; exit 1; }
if head -c 6 "$P" | grep -q bplist; then
  # Binary plist: convert on the fly (macOS plutil), stream via stdin.
  plutil -convert xml1 -o - "$P" | "$GW" -check-auth-config -config "$C" -env-plist -
else
  "$GW" -check-auth-config -config "$C" -env-plist "$P"
fi
