#!/bin/bash
# Install + (re)load the daily backup LaunchAgent (com.llmserving.pgbackup).
set -euo pipefail
label=com.llmserving.pgbackup
out="$HOME/Library/LaunchAgents/$label.plist"
sed -e "s#__HOME__#$HOME#g" "$(dirname "$0")/launchd/$label.plist.template" > "$out"
launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$out"
echo "loaded $label (daily 04:00) from $out"
