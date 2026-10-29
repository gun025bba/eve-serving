#!/bin/bash
# Builds and SIGNS the manifest of one eve-pim (calendar adapter) build. Run on the SIGNING machine (never on the Mac server): the private key stays there.
#   bash sign-pim-manifest.sh --key <private key> --git-sha <40 lowercase hex> --adapter <eve-pim binary> --out <dir>
# Output: <dir>/eve-pim.MANIFEST (3 lines) and <dir>/eve-pim.MANIFEST.sig. Copy both plus the binary to the staging dir on the server, then
#   sudo bash install-calendar-adapter.sh --manifest <abs>/eve-pim.MANIFEST --sig <abs>/eve-pim.MANIFEST.sig --adapter <abs>/eve-pim
# Signature: ssh-keygen -Y sign, namespace "eve-deploy": the same pinned release key the passwordless deploy verifies (no second trust root).
# --git-sha must be the source commit the binary was built from, i.e. the PIM_SHA pinned in install-calendar-adapter.sh.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin
die() { echo "sign-pim-manifest: $*" >&2; exit 1; }
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }
KEY="" SHA="" BIN="" OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY=${2:-}; shift 2;; --git-sha) SHA=${2:-}; shift 2;; --adapter) BIN=${2:-}; shift 2;; --out) OUT=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
[ -n "$KEY" ] && [ -n "$SHA" ] && [ -n "$BIN" ] && [ -n "$OUT" ] || die "need --key --git-sha --adapter --out"
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "--git-sha must be 40 lowercase hex"
[ -f "$KEY" ] && [ -f "$BIN" ] && [ -s "$BIN" ] && [ ! -L "$BIN" ] || die "key/adapter file missing or empty"
mkdir -p "$OUT" && OUT=$(cd "$OUT" && pwd -P) || die "cannot create --out"
M=$OUT/eve-pim.MANIFEST
rm -f "$M" "$M.sig"
{ echo "eve-pim-manifest=1"; echo "pim_sha=$SHA"; echo "pim_sha256=$(sha_of "$BIN")"; } >"$M"
ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$M" >/dev/null
[ -s "$M.sig" ] || die "no signature produced"
echo "signed: $M (+ .sig)"
cat "$M"
