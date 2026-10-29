#!/bin/bash
# Maintainer helper (not installed, never run as root): the pins of the unattended-deploy one-shot. Run in this order, after the files are final and COMMITTED:
#   1. bash pin-installer.sh <A>             A = commit containing eve-deploy, eve-fetch, trusted_root.json, uninstall-deploy-nopasswd.sh   (rewrites the installer's pins)
#   2. bash pin-unattended.sh <A>            A also contains eve-poll and launchd/com.llmserving.evepoll.plist.template: rewrites SRC_COMMIT/SHA_POLL/SHA_PLIST in setup-unattended.sh
#                                            and writes settings/SHA256SUMS (the sha256 of every settings file)
#      commit (installer, setup-unattended.sh, settings/SHA256SUMS) = B
#   3. bash pin-unattended.sh --oneshot <B> writes the sha256 of the installer, setup-unattended.sh and settings/SHA256SUMS into eve-oneshot.sh, and the installer pins (commit B, sha256,
#                                            entrypoint sha256) into mac-oneshot.sh and the README install line   (commit = C)
#   4. bash pin-unattended.sh --print <M>    prints the clone-free one-shot command for the commit M that is on main after the merge (squash/rebase: M = the merge commit)
set -Eeuo pipefail
D=$(cd "$(dirname "$0")" && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
FILES="release-tags.json release-tags.nokey.json release-tags-lock.json main-protect.eve-serving.json main-protect.eve-web.json env-deploy.json env-deploy-major.json security-and-analysis.json actions-workflow.json actions-fork-approval.json"
case "${1:-}" in
  --oneshot)
    b=${2:-}; [[ "$b" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 --oneshot <commit B that contains the installer and setup-unattended.sh>" >&2; exit 1; }
    m=$D/mac-oneshot.sh; r=$D/README.md
    oc=$(sed -n 's/^INSTALL_COMMIT=\([0-9a-f]*\).*/\1/p' "$m"); oh=$(sed -n 's/^INSTALL_SHA256=\([0-9a-f]*\).*/\1/p' "$m")
    nh=$(sha256 "$D/setup-deploy-nopasswd.sh")
    sed -i.bak -e "s/^INSTALL_COMMIT=[0-9a-f]*/INSTALL_COMMIT=$b/" -e "s/^INSTALL_SHA256=[0-9a-f]*/INSTALL_SHA256=$nh/" -e "s/^ENTRY_SHA256=[0-9a-f]*/ENTRY_SHA256=$(sha256 "$D/eve-deploy")/" "$m" && rm -f "$m.bak"
    sed -i.bak -e "s#/$oc/deploy/nopasswd/setup-deploy-nopasswd.sh#/$b/deploy/nopasswd/setup-deploy-nopasswd.sh#g" -e "s/$oh/$nh/g" "$r" && rm -f "$r.bak"
    f=$D/eve-oneshot.sh
    sed -i.bak -e "s/^PIN_INSTALLER_SHA256=[0-9a-f]*/PIN_INSTALLER_SHA256=$(sha256 "$D/setup-deploy-nopasswd.sh")/" -e "s/^PIN_UNATTENDED_SHA256=[0-9a-f]*/PIN_UNATTENDED_SHA256=$(sha256 "$D/setup-unattended.sh")/" \
      -e "s/^PIN_SETTINGS_SHA256=[0-9a-f]*/PIN_SETTINGS_SHA256=$(sha256 "$D/settings/SHA256SUMS")/" "$f" && rm -f "$f.bak"
    grep -E '^PIN_[A-Z0-9_]+=' "$f" | cut -c1-90;;
  --print)
    m=${2:-}; [[ "$m" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 --print <40-hex merge commit on main>" >&2; exit 1; }
    h=$(sha256 "$D/eve-oneshot.sh")
    printf '%s\n' "s=\$(curl -fsSL --proto '=https' --tlsv1.2 $RAW/$m/deploy/nopasswd/eve-oneshot.sh) && [ \"\$(printf '%s\\n' \"\$s\" | shasum -a 256 | cut -d' ' -f1)\" = $h ] && bash -c \"\$s\" eve-oneshot --merge-commit $m";;
  *)
    c=${1:-}; [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 <commit A> | --oneshot | --print <merge commit>" >&2; exit 1; }
    f=$D/setup-unattended.sh
    sed -i.bak -e "s/^SRC_COMMIT=[0-9a-f]*/SRC_COMMIT=$c/" -e "s/^SHA_POLL=.*/SHA_POLL=$(sha256 "$D/eve-poll")/" -e "s/^SHA_PLIST=.*/SHA_PLIST=$(sha256 "$D/launchd/com.llmserving.evepoll.plist.template")/" "$f" && rm -f "$f.bak"
    ( cd "$D/settings" && for n in $FILES; do printf '%s  %s\n' "$(sha256 "$n")" "$n"; done >SHA256SUMS )
    grep -E '^(SRC_COMMIT|SHA_POLL|SHA_PLIST)=' "$f"; wc -l <"$D/settings/SHA256SUMS" | tr -d ' ' | sed 's/$/ settings files pinned in settings\/SHA256SUMS/';;
esac
