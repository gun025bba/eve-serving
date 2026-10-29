#!/bin/bash
# Maintainer helper (not installed, never run as root): the pins of the unattended-deploy one-shot. NO COMMIT IS PINNED ANYWHERE: PR commits do not survive the squash/rebase merge that
# main-protect requires. Every file that is fetched at run time is trusted by the sha256 embedded in the file that fetches it; the one main commit it is downloaded from is an ARGUMENT
# (--src-commit / --merge-commit) of the final command. The hashes form a chain with no cycle:
#     eve-deploy, eve-fetch, trusted_root.json, uninstall  ->  setup-deploy-nopasswd.sh (SHA_*)  ->  mac-oneshot.sh (INSTALL_SHA256, ENTRY_SHA256)  ->  the README lines
#     eve-poll, launchd plist template                     ->  setup-unattended.sh (SHA_POLL, SHA_PLIST)   settings/*  ->  settings/SHA256SUMS
#     the installer, setup-unattended.sh, setup-pim-adapter.sh, settings/SHA256SUMS  ->  eve-oneshot.sh (PIN_*)  ->  the one-line command (sha256 of eve-oneshot.sh; no self-reference)
# After ANY change, from the final tree (no commits in between, run in this order; test/run.sh fails on a stale pin):
#   1. bash deploy/nopasswd/pin-installer.sh     rewrites the SHA_* pins of the installer
#   2. bash deploy/nopasswd/pin-unattended.sh   rewrites SHA_POLL/SHA_PLIST in setup-unattended.sh and writes settings/SHA256SUMS
#   3. bash deploy/nopasswd/pin-unattended.sh --oneshot   rewrites the pins of mac-oneshot.sh, eve-oneshot.sh and the sha256 values in the README (the README commit is the MAIN placeholder)
#   4. after the merge:   bash deploy/nopasswd/pin-unattended.sh --print <40-hex merge commit on main> [--calendar-adapter X.Y.Z] [--gateway-deploy X.Y.Z] [--web-deploy X.Y.Z]   prints the final one-line command
set -Eeuo pipefail
D=$(cd "$(dirname "$0")" && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
FILES="release-tags.json release-tags.nokey.json release-tags-lock.json main-protect.eve-serving.json main-protect.eve-web.json main-protect.eve-ios.json release-branches.eve-serving.json env-deploy.json env-deploy-major.json security-and-analysis.json actions-workflow.json actions-fork-approval.json"
case "${1:-}" in
  --oneshot)
    m=$D/mac-oneshot.sh; r=$D/README.md; f=$D/eve-oneshot.sh
    oh=$(sed -n 's/^INSTALL_SHA256=\([0-9a-f]*\).*/\1/p' "$m"); nh=$(sha256 "$D/setup-deploy-nopasswd.sh")
    sed -i.bak -e "s/^INSTALL_SHA256=[0-9a-f]*/INSTALL_SHA256=$nh/" -e "s/^ENTRY_SHA256=[0-9a-f]*/ENTRY_SHA256=$(sha256 "$D/eve-deploy")/" "$m" && rm -f "$m.bak"
    # README: the installer line carries the installer's sha256, the mac-oneshot lines carry mac-oneshot.sh's
    om=$(grep -o 'mac-oneshot.sh) && .*= [0-9a-f]\{64\}' "$r" | head -1 | grep -o '[0-9a-f]\{64\}$' || true)
    [ -n "$oh" ] && [ -n "$om" ] || { echo "README pins not found" >&2; exit 1; }
    sed -i.bak -e "s/$oh/$nh/g" -e "s/$om/$(sha256 "$m")/g" "$r" && rm -f "$r.bak"
    sed -i.bak -e "s/^PIN_INSTALLER_SHA256=[0-9a-f]*/PIN_INSTALLER_SHA256=$(sha256 "$D/setup-deploy-nopasswd.sh")/" -e "s/^PIN_UNATTENDED_SHA256=[0-9a-f]*/PIN_UNATTENDED_SHA256=$(sha256 "$D/setup-unattended.sh")/" \
      -e "s/^PIN_PIMADAPTER_SHA256=[0-9a-f]*/PIN_PIMADAPTER_SHA256=$(sha256 "$D/setup-pim-adapter.sh")/" \
      -e "s/^PIN_SETTINGS_SHA256=[0-9a-f]*/PIN_SETTINGS_SHA256=$(sha256 "$D/settings/SHA256SUMS")/" "$f" && rm -f "$f.bak"
    grep -E '^(INSTALL_SHA256|ENTRY_SHA256)=' "$m" | cut -c1-90; grep -E '^PIN_[A-Z0-9_]+=' "$f" | cut -c1-90;;
  --print)
    m=${2:-}; [[ "$m" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 --print <40-hex merge commit on main> [eve-oneshot options]" >&2; exit 1; }
    h=$(sha256 "$D/eve-oneshot.sh"); x=${*:3}   # optional options of eve-oneshot.sh, e.g. --calendar-adapter 5.1.0 --gateway-deploy 5.1.0
    case "$x" in *[!0-9a-z.\ -]*) echo "only [0-9a-z. -] after the commit" >&2; exit 1;; esac
    printf '%s\n' "s=\$(curl -fsSL --proto '=https' --tlsv1.2 $RAW/$m/deploy/nopasswd/eve-oneshot.sh) && [ \"\$(printf '%s\\n' \"\$s\" | shasum -a 256 | cut -d' ' -f1)\" = $h ] && bash -c \"\$s\" eve-oneshot --merge-commit $m${x:+ $x}";;
  "")
    f=$D/setup-unattended.sh
    sed -i.bak -e "s/^SHA_POLL=.*/SHA_POLL=$(sha256 "$D/eve-poll")/" -e "s/^SHA_PLIST=.*/SHA_PLIST=$(sha256 "$D/launchd/com.llmserving.evepoll.plist.template")/" "$f" && rm -f "$f.bak"
    ( cd "$D/settings" && for n in $FILES; do printf '%s  %s\n' "$(sha256 "$n")" "$n"; done >SHA256SUMS )
    grep -E '^(SHA_POLL|SHA_PLIST)=' "$f"; wc -l <"$D/settings/SHA256SUMS" | tr -d ' ' | sed 's/$/ settings files pinned in settings\/SHA256SUMS/';;
  *) echo "usage: $0 | --oneshot | --print <40-hex merge commit on main> [eve-oneshot options]" >&2; exit 1;;
esac
