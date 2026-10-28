#!/bin/bash
# Builds and SIGNS a deploy bundle. Run on the SIGNING machine (never on the Mac server): the private key stays there.
#   bash sign-deploy-bundle.sh --key <private key> --gateway <file> [--eve-gw <file>] --version X.Y.Z --schema N --out <dir>
# Output: <dir>/eve-X.Y.Z.tar and <dir>/eve-X.Y.Z.tar.sig  -> copy both to the staging dir on the server (scp), then:
#   sudo /usr/local/libexec/eve/deploy deploy X.Y.Z
# Signature: ssh-keygen -Y sign, namespace "eve-deploy" (the server only accepts that namespace and the pinned public key).
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
export COPYFILE_DISABLE=1   # macOS tar: no AppleDouble (._*) members
export PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin
die() { echo "sign-deploy-bundle: $*" >&2; exit 1; }
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }
KEY="" GW="" EG="" VER="" SCHEMA="" OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY=${2:-}; shift 2;; --gateway) GW=${2:-}; shift 2;; --eve-gw) EG=${2:-}; shift 2;;
    --version) VER=${2:-}; shift 2;; --schema) SCHEMA=${2:-}; shift 2;; --out) OUT=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
[ -n "$KEY" ] && [ -n "$GW" ] && [ -n "$VER" ] && [ -n "$SCHEMA" ] && [ -n "$OUT" ] || die "need --key --gateway --version (--eve-gw is optional) --schema --out"
[[ "$VER" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]] || die "--version must be X.Y.Z"
[[ "$SCHEMA" =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "--schema must be a number"
[ -f "$KEY" ] && [ -f "$GW" ] && [ -s "$GW" ] || die "key/gateway file missing or empty"
[ -z "$EG" ] || { [ -f "$EG" ] && [ -s "$EG" ]; } || die "eve-gw file missing or empty"
mkdir -p "$OUT" && OUT=$(cd "$OUT" && pwd -P) || die "cannot create --out"   # absolute: tar runs from another directory
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
cp "$GW" "$W/gateway"
[ -z "$EG" ] || cp "$EG" "$W/eve-gw"
{ echo "eve-deploy-bundle=1"; echo "version=$VER"; echo "schema=$SCHEMA"
  echo "sha256.gateway=$(sha_of "$W/gateway")"
  [ -z "$EG" ] || echo "sha256.eve-gw=$(sha_of "$W/eve-gw")"; } >"$W/MANIFEST"
T=$OUT/eve-$VER.tar
rm -f "$T" "$T.sig"
if [ -n "$EG" ]; then ( cd "$W" && tar -cf "$T" MANIFEST eve-gw gateway ); else ( cd "$W" && tar -cf "$T" MANIFEST gateway ); fi
ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$T" >/dev/null
[ -s "$T.sig" ] || die "no signature produced"
echo "signed: $T (+ .sig)  version=$VER schema=$SCHEMA"
echo "bundle sha256: $(sha_of "$T")"
