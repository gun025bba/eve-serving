#!/bin/bash
# Render launchd templates into ~/Library/LaunchAgents (macOS deploy hosts).
# Usage: BIND_IP=<tailnet-ip> deploy/install-launchd.sh   (then launchctl bootstrap/kickstart as needed)
set -euo pipefail
: "${BIND_IP:?set BIND_IP to the private/tailnet IP for the *-ts gateway}"
cd "$(dirname "$0")/launchd"
for t in *.plist.template; do
  out="$HOME/Library/LaunchAgents/${t%.template}"
  sed -e "s#__HOME__#$HOME#g" -e "s#__BIND_IP__#$BIND_IP#g" "$t" > "$out"; echo "wrote $out"
done
