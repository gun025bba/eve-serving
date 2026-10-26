#!/bin/bash
# Render launchd plists for the Go gateway (replaces the uvicorn gateway agents; same labels).
# Usage: BIND_IP=<tailnet-ip> deploy/install-launchd-go.sh   -> then kickstart (see README "Cutover to Go")
# Backs up the previous plists to *.plist.py-bak so the cutover can be reverted.
set -euo pipefail
: "${BIND_IP:?set BIND_IP to the private/tailnet IP for the -ts gateway}"
t="$(dirname "$0")/launchd-go/gateway.plist.template"
render() { # label host logname
  out="$HOME/Library/LaunchAgents/$1.plist"
  [ -f "$out" ] && cp "$out" "$out.py-bak"
  sed -e "s#__HOME__#$HOME#g" -e "s#__BIND_HOST__#$2#g" -e "s#__LABEL__#$1#g" -e "s#__LOG__#$3#g" "$t" > "$out"; echo "wrote $out"
}
render com.llmserving.gateway 127.0.0.1 gateway
render com.llmserving.gateway-ts "$BIND_IP" gateway-ts
