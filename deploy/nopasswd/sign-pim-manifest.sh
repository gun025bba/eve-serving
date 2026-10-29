#!/bin/bash
# LOCAL USE AND TESTS ONLY. Builds and signs the manifest of one eve-pim (calendar adapter) build with a local ssh key.
# It is NOT the production signing path and must NEVER run in CI or with the deploy release key in an Actions secret: the build job
# holds no secrets; production signing happens in a separate, protected job with Sigstore keyless (cosign), owned by DevOps.
# While the installer's PIM_PIN_READY=0 (the pin does not yet cover the shipped source, security review M-1) this script refuses to sign
# unless --test-only is given; a manifest from --test-only is still refused by the installer (it needs PIM_PIN_READY=1).
#   bash sign-pim-manifest.sh [--test-only] --key <private key> --git-sha <40 lowercase hex> --adapter <eve-pim binary> --out <dir>
# Output: <dir>/eve-pim.MANIFEST (3 lines) and <dir>/eve-pim.MANIFEST.sig. Copy both plus the binary to the staging dir on the server, then
#   sudo bash install-calendar-adapter.sh --manifest <abs>/eve-pim.MANIFEST --sig <abs>/eve-pim.MANIFEST.sig --adapter <abs>/eve-pim
# Signature: ssh-keygen -Y sign, namespace "eve-deploy": the key the passwordless deploy's verifier trusts today (no second trust root); replaced by cosign keyless.
# --git-sha must be the source commit the binary was built from, i.e. the PIM_SHA pinned in install-calendar-adapter.sh.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
export LC_ALL=C
export PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin
die() { echo "sign-pim-manifest: $*" >&2; exit 1; }
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }
KEY="" SHA="" BIN="" OUT="" TEST_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --test-only) TEST_ONLY=1; shift;;
    --key) KEY=${2:-}; shift 2;; --git-sha) SHA=${2:-}; shift 2;; --adapter) BIN=${2:-}; shift 2;; --out) OUT=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
INS=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)/install-calendar-adapter.sh
if [ "$TEST_ONLY" != 1 ] && ! grep -Eq '^PIM_PIN_READY=1([[:space:]]|$)' "$INS" 2>/dev/null; then
  die "the installer's pin is not ready (PIM_PIN_READY!=1): the adapter must not be signed until the pin moves in its own deploy-major change (use --test-only in tests)"
fi
[ -n "$KEY" ] && [ -n "$SHA" ] && [ -n "$BIN" ] && [ -n "$OUT" ] || die "need --key --git-sha --adapter --out"
[[ "$SHA" =~ ^[0123456789abcdef]{40}$ ]] || die "--git-sha must be 40 lowercase hex"
[ -f "$KEY" ] && [ -f "$BIN" ] && [ -s "$BIN" ] && [ ! -L "$BIN" ] || die "key/adapter file missing or empty"
mkdir -p "$OUT" && OUT=$(cd "$OUT" && pwd -P) || die "cannot create --out"
M=$OUT/eve-pim.MANIFEST
rm -f "$M" "$M.sig"
{ echo "eve-pim-manifest=1"; echo "pim_sha=$SHA"; echo "pim_sha256=$(sha_of "$BIN")"; } >"$M"
ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$M" >/dev/null
[ -s "$M.sig" ] || die "no signature produced"
echo "signed: $M (+ .sig)"
cat "$M"
