#!/bin/bash
# Maintainer helper (not installed, never run as root): rewrites the pins at the top of setup-deploy-nopasswd.sh and prints the
# one-time install line. Usage, after the files it pins are final and COMMITTED (and pushed, so the raw URL works):
#   bash deploy/nopasswd/pin-installer.sh <40-hex sha of the commit that contains eve-deploy, eve-fetch, trusted_root.json, uninstall-deploy-nopasswd.sh>
# Commit the changed installer, then:   bash deploy/nopasswd/pin-installer.sh --print <40-hex sha of the commit that contains the installer>
# and put that line in README.md (the tests check that the README pins the installer's current sha256).
set -Eeuo pipefail
D=$(cd "$(dirname "$0")" && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
if [ "${1:-}" = --print ]; then
  c=${2:-}; [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 --print <commit sha of the installer>" >&2; exit 1; }
  h=$(sha256 "$D/setup-deploy-nopasswd.sh")
  printf '%s\n' "s=\$(curl -fsSL --proto '=https' --tlsv1.2 $RAW/$c/deploy/nopasswd/setup-deploy-nopasswd.sh) && [ \"\$(printf '%s\\n' \"\$s\" | shasum -a 256 | cut -d' ' -f1)\" = $h ] && printf '%s\\n' \"\$s\" | sudo /bin/bash -s"
  exit 0
fi
c=${1:-}; [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 <commit sha> | --print <commit sha>" >&2; exit 1; }
f=$D/setup-deploy-nopasswd.sh
sed -i.bak -e "s/^SRC_COMMIT=.*/SRC_COMMIT=$c/" -e "s/^SHA_DEPLOY=.*/SHA_DEPLOY=$(sha256 "$D/eve-deploy")/" -e "s/^SHA_FETCH=.*/SHA_FETCH=$(sha256 "$D/eve-fetch")/" \
  -e "s/^SHA_ROOT=.*/SHA_ROOT=$(sha256 "$D/trusted_root.json")/" -e "s/^SHA_UNINSTALL=.*/SHA_UNINSTALL=$(sha256 "$D/uninstall-deploy-nopasswd.sh")/" "$f" && rm -f "$f.bak"
grep -E '^(SRC_COMMIT|SHA_[A-Z]+)=' "$f"
