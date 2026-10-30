#!/bin/bash
# Builds the CALENDAR-ONLY eve-pim release binary. Runs in CI on a macOS runner (never on the server: the server only verifies and installs).
#   bash pim/eve-pim/build-release.sh --out <dir>
# Output: <dir>/eve-pim  (arm64/x86_64 as the runner is, ad-hoc signed with the hardened runtime) and a line `pim_sha256=<hex>` on stdout.
# Info.plist (usage strings) is embedded into the binary; entitlements are applied by codesign. Both files are in Support/ and may name
# the Calendar only: this script re-checks that before building, a Go test checks it on every CI run, the installer checks the finished binary.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
export LC_ALL=C
die() { echo "build-release: $*" >&2; exit 1; }
HERE=$(cd "$(dirname "$0")" && pwd -P)
INFO=$HERE/Support/eve-pim-Info.plist
ENT=$HERE/Support/eve-pim.entitlements
ALLOWED_USAGE=" NSCalendarsUsageDescription NSCalendarsFullAccessUsageDescription "
ALLOWED_INFO=" CFBundleIdentifier CFBundleName CFBundleVersion $ALLOWED_USAGE"
ALLOWED_ENT=" com.apple.security.personal-information.calendars "
OUT=""
while [ $# -gt 0 ]; do
  case "$1" in --out) OUT=${2:-}; shift 2;; *) die "unknown argument: $1";; esac
done
[ -n "$OUT" ] || die "need --out <dir>"
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }
plist_keys() { python3 -c 'import plistlib, sys
for k in plistlib.load(open(sys.argv[1], "rb")): print(k)' "$1"; }
check_keys() { # $1 file $2 allowed-list (space padded) $3 label
  local k
  for k in $(plist_keys "$1"); do case "$2" in *" $k "*) ;; *) die "$3 carries a key that is not allowed (calendar only): $k";; esac; done
}
[ -f "$INFO" ] && [ -f "$ENT" ] || die "Support/eve-pim-Info.plist and Support/eve-pim.entitlements are required"
check_keys "$INFO" "$ALLOWED_INFO" "Info.plist"
check_keys "$ENT" "$ALLOWED_ENT" "entitlements"
plist_keys "$INFO" | grep -qx NSCalendarsUsageDescription || die "Info.plist lacks NSCalendarsUsageDescription"
mkdir -p "$OUT" && OUT=$(cd "$OUT" && pwd -P) || die "cannot create --out"
cd "$HERE"
swift build -c release -Xlinker -sectcreate -Xlinker __TEXT -Xlinker __info_plist -Xlinker "$INFO"
BIN=$(swift build -c release --show-bin-path)/eve-pim
[ -f "$BIN" ] || die "built binary missing"
rm -f "$OUT/eve-pim"; cp "$BIN" "$OUT/eve-pim"; chmod 0755 "$OUT/eve-pim"
codesign --force -s - -o runtime --identifier com.eve.pim --entitlements "$ENT" "$OUT/eve-pim"
codesign --verify --strict "$OUT/eve-pim"
codesign -dv "$OUT/eve-pim" 2>&1 | grep -q 'runtime' || die "the hardened runtime flag is missing after codesign"
echo "pim_sha256=$(sha_of "$OUT/eve-pim")"
