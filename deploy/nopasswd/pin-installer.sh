#!/bin/bash
# Maintainer helper (not installed, never run as root): rewrites the sha256 pins at the top of setup-deploy-nopasswd.sh and prints the one-time install line.
# NO COMMIT is pinned anywhere (PR commits do not survive a squash/rebase merge): every file is trusted by its sha256, and the one main commit it is downloaded from is the --src-commit argument.
#   bash deploy/nopasswd/pin-installer.sh                 rewrites SHA_DEPLOY, SHA_FETCH, SHA_ROOT, SHA_UNINSTALL from the files in this directory
#   bash deploy/nopasswd/pin-installer.sh --print <40-hex main commit>   prints the install line for that main commit (the README carries the same line with MAIN_SHA in place of the commit)
set -Eeuo pipefail
D=$(cd "$(dirname "$0")" && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
if [ "${1:-}" = --print ]; then
  c=${2:-}; [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 --print <40-hex main commit>" >&2; exit 1; }
  h=$(sha256 "$D/setup-deploy-nopasswd.sh")
  printf '%s\n' "s=\$(curl -fsSL --proto '=https' --tlsv1.2 $RAW/$c/deploy/nopasswd/setup-deploy-nopasswd.sh) && [ \"\$(printf '%s\\n' \"\$s\" | shasum -a 256 | cut -d' ' -f1)\" = $h ] && printf '%s\\n' \"\$s\" | sudo /bin/bash -s -- --src-commit $c"
  exit 0
fi
[ $# -eq 0 ] || { echo "usage: $0 | --print <40-hex main commit>" >&2; exit 1; }
f=$D/setup-deploy-nopasswd.sh
sed -i.bak -e "s/^SHA_DEPLOY=.*/SHA_DEPLOY=$(sha256 "$D/eve-deploy")/" -e "s/^SHA_FETCH=.*/SHA_FETCH=$(sha256 "$D/eve-fetch")/" \
  -e "s/^SHA_ROOT=.*/SHA_ROOT=$(sha256 "$D/trusted_root.json")/" -e "s/^SHA_UNINSTALL=.*/SHA_UNINSTALL=$(sha256 "$D/uninstall-deploy-nopasswd.sh")/" "$f" && rm -f "$f.bak"
grep -E '^SHA_[A-Z]+=' "$f"
