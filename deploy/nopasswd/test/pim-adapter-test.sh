#!/bin/bash
# Behaviour tests of deploy/nopasswd/setup-pim-adapter.sh (verify + atomic install of the Sigstore-signed calendar adapter).
# Runs unprivileged on Linux and macOS (bash 3.2): the script is SOURCED with EVE_PIM_LIB_ONLY=1 (it then only defines functions and constants; a root run never takes that path),
# its fixed paths are re-pointed at a temp dir, `cosign` and `codesign` are stubs. The PRODUCTION verify_manifest_sig / verify_inputs / install_adapter run unchanged.
#   bash deploy/nopasswd/test/pim-adapter-test.sh        (also run by test/run.sh)
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
PA=$D/setup-pim-adapter.sh
umask 022
[ "$(id -u)" != 0 ] || { echo "SKIP pim adapter tests: run as an unprivileged user"; echo "pim adapter tests passed: 0 failed: 0 (skipped)"; exit 0; }
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()   { PASS=$((PASS + 1)); }
bad()  { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()   { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
has()  { case "$2" in *"$3"*) ok;; *) bad "$1: missing [$3]";; esac; }
yes_() { local n=$1; shift; if "$@" >/dev/null 2>&1; then ok; else bad "$n: expected success: $*"; fi; }
no_()  { local n=$1; shift; if "$@" >/dev/null 2>&1; then bad "$n: expected failure: $*"; else ok; fi; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
modeof() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
inodeof() { stat -c %i "$1" 2>/dev/null || stat -f %i "$1"; }
const() { sed -n "s/^$1=\"\{0,1\}\([^\" ]*\).*/\1/p" "$2" | head -1; }
MYUID=$(id -u); MYGID=$(id -g)

# ================= 0. static: the same trust root as the entrypoint, nothing new in the privileged surface =================
for k in OIDC_ISSUER SIGNER_IDENTITY SIGNER_TRIGGER TRUSTED_ROOT_SHA256; do eq "constant $k = the entrypoint's" "$(const $k "$PA")" "$(const $k "$D/eve-deploy")"; done
eq "constant COSIGN_SHA256S = the entrypoint's" "$(sed -n 's/^COSIGN_SHA256S="\(.*\)"$/\1/p' "$PA")" "$(sed -n 's/^COSIGN_SHA256S="\(.*\)"$/\1/p' "$D/eve-deploy")"
case "$(const PIM_TREE "$PA")" in ????????????????????????????????????????) ok;; *) bad "PIM_TREE is not 40 characters";; esac
case "$(const PIM_TREE "$PA")" in *[!0123456789abcdef]*|"") bad "PIM_TREE is not lowercase hex";; *) ok;; esac
eq "the commit-SHA pin is gone (a commit that is not on main cannot be the pin)" "$(grep -v '^ *#' "$PA" | grep -c 'PIM_SHA\|pim_sha=')" 0
case "$(const PIM_SIGNING_CI "$PA")" in yes|no) ok;; *) bad "PIM_SIGNING_CI must be yes or no";; esac
eq "cosign is bound to the release commit (--certificate-github-workflow-sha)" "$(grep -c -- '--certificate-github-workflow-sha "\$3"' "$PA")" 1
# the pin is the tree hash of pim/eve-pim at the reviewed commit, when this checkout has it
if command -v git >/dev/null 2>&1 && git -C "$HERE" cat-file -e 19cb222a0951ba95c6c7d8b28861143feb8591b7 2>/dev/null; then eq "PIM_TREE = git tree of pim/eve-pim at the reviewed commit" "$(const PIM_TREE "$PA")" "$(git -C "$HERE" rev-parse 19cb222a0951ba95c6c7d8b28861143feb8591b7:pim/eve-pim)"; fi
eq "no sudoers file or verb is written by the adapter installer" "$(grep -v '^ *#' "$PA" | grep -c 'sudoers')" 0
eq "no ssh key / ssh-keygen in the adapter installer (Sigstore only)" "$(grep -v '^ *#' "$PA" | grep -c 'ssh-keygen\|allowed_signers')" 0
eq "curl is https-only" "$(grep -c -- "--proto '=https'" "$PA")" 1
eq "no insecure verify flag" "$(grep -c -- 'insecure\|--key \|--skip' "$PA")" 0
eq "no Mac Studio string" "$(grep -c 'Mac Studio' "$PA")" 0
eq "the LaunchAgent never passes --writes-enabled" "$(grep -c -- '<string>--writes-enabled' "$PA")" 0
eq "flags are only ever added as false" "$(grep -v "^ *#" "$PA" | grep -c "GW_PIM_WRITES_ENABLED.*true\|GW_PIM_ENABLED.*true\|GW_BRIEFING_ENABLED.*true")" 0

# shellcheck source=setup-pim-adapter.sh
EVE_PIM_LIB_ONLY=1 . "$PA" || { echo "cannot source the adapter installer"; exit 1; }
SHA_PIN=$PIM_TREE
REL_SHA=$(printf 'd%.0s' $(seq 1 40))   # the commit the release tag points to (manifest release_sha)
OTHER_REL=$(printf 'e%.0s' $(seq 1 40))
VERSION=5.1.0
# ---------------- the guard: refuse until CI signs the adapter ----------------
SAVE_G=$PIM_SIGNING_CI
PIM_SIGNING_CI=no; no_ "guard: PIM_SIGNING_CI=no refuses" require_signing_ci; has "guard: the reason is shown" "$(require_signing_ci 2>&1)" "CI does not sign and publish the calendar adapter yet"
PIM_SIGNING_CI=""; no_ "guard: empty refuses" require_signing_ci
PIM_SIGNING_CI=maybe; no_ "guard: anything but yes refuses" require_signing_ci
PIM_SIGNING_CI=yes; yes_ "guard: yes passes" require_signing_ci
PIM_SIGNING_CI=$SAVE_G
# the shipped value: when it is "no", a real run (not sourced) must stop at the guard before touching anything
if [ "$SAVE_G" = no ]; then
  out=$(bash "$PA" --version 5.1.0 2>&1); rc=$?
  eq "guard: the script run for real exits 3 while PIM_SIGNING_CI=no" "$rc" 3
  has "guard: the script run for real says why" "$out" "Nothing was downloaded or changed"
fi
yes_ "version 5.1.0" is_ver 5.1.0
no_ "version 5.1" is_ver 5.1
no_ "version 5.1.0.rc1" is_ver 5.1.0.rc1
no_ "version v5.1.0" is_ver v5.1.0
no_ "version 05.1.0" is_ver 05.1.0
no_ "version with a path" is_ver "5.1.0/../x"
no_ "version empty" is_ver ""
no_ "version with newline" is_ver "$(printf '5.1.0\n')x"

# ---------------- test world: every fixed path re-pointed at the temp dir ----------------
mkdir -p "$T/libexec" "$T/in" "$T/bin"; chmod 755 "$T/libexec"
printf '{"fake":"trusted root"}\n' >"$T/libexec/trusted_root.json"; chmod 644 "$T/libexec/trusted_root.json"
cat >"$T/libexec/cosign" <<'STUB'
#!/bin/bash
# fake cosign verify-blob: records its arguments; succeeds only if the bundle says it signed exactly the bytes of the manifest (so a swapped manifest fails), and never for BADSIG
shin() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1; else sha256sum | cut -d' ' -f1; fi; }
echo "$@" >"$COSARGS_FILE"
[ "$1" = verify-blob ] || exit 2
b=""; prev=""; for a; do [ "$prev" = --bundle ] && b=$a; prev=$a; done; for last; do :; done
grep -q BADSIG "$b" && { echo "Error: none of the expected identities matched" >&2; exit 1; }
w=""; prev=""; for a; do [ "$prev" = --certificate-github-workflow-sha ] && w=$a; prev=$a; done
[ -n "$w" ] && [ "$(sed -n 's/^sha=//p' "$b")" = "$w" ] || { echo "Error: expected SHA does not match the certificate" >&2; exit 1; }
[ "$(sed -n 's/^signed=//p' "$b")" = "$(shin <"$last")" ] || { echo "Error: invalid signature when validating ASN.1 encoded signature" >&2; exit 1; }
exit 0
STUB
sed -i.bak "s|\$COSARGS_FILE|$T/cosargs|" "$T/libexec/cosign" && rm -f "$T/libexec/cosign.bak"; chmod 755 "$T/libexec/cosign"
cat >"$T/bin/codesign" <<'STUB'
#!/bin/bash
for f; do :; done
case "$1" in
  --verify) grep -aq BADCS "$f" && exit 1; exit 0;;
  -dv) echo "Executable=/Users/x/runtime/eve-pim" >&2   # a path that contains the word "runtime" must never count
       if grep -aq NORUNTIME "$f"; then echo "CodeDirectory v=20500 flags=0x2(adhoc) hashes=1" >&2
       elif grep -aq RTWORDONLY "$f"; then echo "CodeDirectory v=20500 flags=0x2(adhoc,runtimeish) hashes=1" >&2
       elif grep -aq RTNOBIT "$f"; then echo "CodeDirectory v=20500 flags=0x2(adhoc,runtime) hashes=1" >&2
       elif grep -aq RTTWOLINES "$f"; then echo "CodeDirectory v=20500 flags=0x10002(adhoc,runtime) hashes=1" >&2; echo "CodeDirectory v=20500 flags=0x2(adhoc) hashes=1" >&2
       else echo "CodeDirectory v=20500 flags=0x10002(adhoc,runtime) hashes=1" >&2; fi; exit 0;;
  -d) echo '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>'
      grep -a -o 'ENT=[^ ]*' "$f" | sed 's/^ENT=//; s/.*/<key>&<\/key><true\/>/'
      echo '</dict></plist>'; exit 0;;
esac
exit 2
STUB
chmod 755 "$T/bin/codesign"
cat >"$T/bin/install" <<'STUB'
#!/bin/bash
# wrapper: fails for a destination that matches the pattern in $T/failpat (one-shot per run), otherwise the real install
F="$FAILPAT_FILE"; if [ -s "$F" ]; then for last; do :; done; case "$last" in *$(cat "$F")*) : >"$F"; exit 1;; esac; fi
exec /usr/bin/install "$@"
STUB
sed -i.bak "s|\$FAILPAT_FILE|$T/failpat|" "$T/bin/install" && rm -f "$T/bin/install.bak"; chmod 755 "$T/bin/install"; : >"$T/failpat"
COSIGN=$T/libexec/cosign; LIBEXEC=$T/libexec; TRUSTED_ROOT=$T/libexec/trusted_root.json
COSIGN_SHA256S=$(sha256 "$COSIGN"); TRUSTED_ROOT_SHA256=$(sha256 "$TRUSTED_ROOT")
CODESIGN=$T/bin/codesign; INSTALL=$T/bin/install
TRUST_ROOT=$T; ROOT_UID=$MYUID; ROOT_GID=$MYGID; OWNER_ARGS=""
OTHER_SHA=$(printf 'b%.0s' $(seq 1 40))

fake() { # $1 name $2 payload [extra marker lines...]
  local n=$1 p=$2; shift 2
  { echo "NSCalendarsUsageDescription"; echo "ENT=com.apple.security.personal-information.calendars"; echo "PAYLOAD-$p"; for m in "$@"; do echo "$m"; done; } >"$T/in/$n"
}
bundle() { # $1 name [$2 commit the certificate carries, default REL_SHA]: Sigstore bundle "signed" over exactly the bytes of $T/in/$1.MANIFEST
  printf 'signed=%s\nsha=%s\n' "$(sha256 "$T/in/$1.MANIFEST")" "${2:-$REL_SHA}" >"$T/in/$1.sigstore.json"
}
manifest() { # $1 name $2 pim_tree $3 pim_sha256 [$4 release_sha, default REL_SHA] -> manifest + bundle
  printf 'eve-pim-manifest=1\nrelease_sha=%s\npim_tree=%s\npim_sha256=%s\n' "${4:-$REL_SHA}" "$2" "$3" >"$T/in/$1.MANIFEST"; bundle "$1"
}
build() { local n=$1; fake "$@"; manifest "$n" "$SHA_PIN" "$(sha256 "$T/in/$n")"; }
point_at() { # $1 base dir: fresh installed-state directory tree
  SOCK_BASE=$1; PIM_DIR=$SOCK_BASE/bin; PIM_BIN=$PIM_DIR/eve-pim; PIM_SB=$PIM_DIR/eve-pim.sb
  PIM_MF=$PIM_BIN.manifest; PIM_MF_SIG=$PIM_BIN.sigstore.json
  rm -rf "$1"; mkdir -p "$PIM_DIR"; chmod 755 "$SOCK_BASE" "$PIM_DIR"
}
newwork() { # $1 name: a work dir holding copies named like the real ones
  WORK=$T/work.$$.$RANDOM; mkdir -p "$WORK"; chmod 700 "$WORK"
  cp "$T/in/$1.MANIFEST" "$WORK/in.MANIFEST"; cp "$T/in/$1.sigstore.json" "$WORK/in.sigstore.json"; cp "$T/in/${2:-$1}" "$WORK/in.eve-pim"
}
do_verify() { ( newwork "$@"; verify_inputs "$WORK/in.MANIFEST" "$WORK/in.sigstore.json" "$WORK/in.eve-pim" ) >"$T/out" 2>&1; }
do_install() { ( newwork "$@"; verify_inputs "$WORK/in.MANIFEST" "$WORK/in.sigstore.json" "$WORK/in.eve-pim" && install_adapter; rc=$?; echo "${INSTALL_RESULT:-}" >"$T/res"; exit $rc ) >"$T/out" 2>&1; }
leftovers() { ls -A "$PIM_DIR" | grep -c '\.new\.'; }

# ================= 1. manifest format (parse_manifest) =================
H40=$(printf 'a%.0s' $(seq 1 40)); R40=$(printf 'f%.0s' $(seq 1 40)); H64=$(printf 'c%.0s' $(seq 1 64))
pm() { printf "$1" >"$T/m"; parse_manifest "$T/m"; }
yes_ "good manifest" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n"
yes_ "no trailing newline" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64"
yes_ "any order" pm "pim_sha256=$H64\neve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\n"
eq "parsed pim_tree" "$P_TREE" "$H40"; eq "parsed release_sha" "$P_REL" "$R40"; eq "parsed pim_sha256" "$P_SHA256" "$H64"
no_ "pim_tree uppercase" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$(echo $H40 | tr a A)\npim_sha256=$H64\n"
no_ "pim_tree 39 hex" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=${H40%?}\npim_sha256=$H64\n"
no_ "pim_tree 41 hex" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=${H40}a\npim_sha256=$H64\n"
no_ "pim_tree not hex" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=${H40%?}g\npim_sha256=$H64\n"
no_ "pim_tree short ref" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=6ce1993\npim_sha256=$H64\n"
no_ "pim_tree empty" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=\npim_sha256=$H64\n"
no_ "pim_tree with space" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=${H40} \npim_sha256=$H64\n"
no_ "CR line ends" pm "eve-pim-manifest=1\r\nrelease_sha=$R40\npim_tree=$H40\r\npim_sha256=$H64\r\n"
no_ "pim_sha256 63 hex" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=${H64%?}\n"
no_ "pim_sha256 uppercase" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$(echo $H64 | tr c C)\n"
no_ "pim_sha256 40 hex" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H40\n"
no_ "missing version line" pm "release_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n"
no_ "missing release_sha" pm "eve-pim-manifest=1\npim_tree=$H40\npim_sha256=$H64\n"
no_ "release_sha uppercase" pm "eve-pim-manifest=1\nrelease_sha=$(echo $R40 | tr f F)\npim_tree=$H40\npim_sha256=$H64\n"
no_ "release_sha 39 hex" pm "eve-pim-manifest=1\nrelease_sha=${R40%?}\npim_tree=$H40\npim_sha256=$H64\n"
no_ "old 3-line commit manifest (pim_sha)" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64\n"
no_ "duplicate release_sha" pm "eve-pim-manifest=1\nrelease_sha=$R40\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n"
no_ "missing pim_sha256" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\n"
no_ "wrong version" pm "eve-pim-manifest=2\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n"
no_ "extra line" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\nnote=x\n"
no_ "extra blank line" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n\n"
no_ "duplicate key" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\nrelease_sha=$R40\npim_tree=$H40\n"
no_ "unknown key" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\nsha256=$H64\n"
no_ "line without =" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\n$H64\n"
no_ "NUL byte" pm "eve-pim-manifest=1\nrelease_sha=$R40\npim_tree=$H40\npim_sha256=$H64\n\000"
no_ "empty file" pm ""
no_ "missing file" parse_manifest "$T/nonexistent"
printf 'eve-pim-manifest=1\nrelease_sha=%s\npim_tree=%s\npim_sha256=%s\n' "$R40" "$H40" "$H64" >"$T/m"; ln -sf "$T/m" "$T/m.link"
no_ "symlinked manifest" parse_manifest "$T/m.link"
head -c 2000 /dev/zero | tr '\0' 'a' >"$T/m.big"; no_ "oversized manifest" parse_manifest "$T/m.big"

# ================= 2. the pinned tools (tool_ok) =================
yes_ "tools ok" tool_ok
SAVE_CS=$COSIGN_SHA256S; COSIGN_SHA256S=$H64; no_ "cosign hash not pinned" tool_ok; COSIGN_SHA256S=$SAVE_CS
SAVE_TR=$TRUSTED_ROOT_SHA256; TRUSTED_ROOT_SHA256=$H64; no_ "trusted root hash not pinned" tool_ok; TRUSTED_ROOT_SHA256=$SAVE_TR
chmod 775 "$COSIGN"; no_ "cosign group writable" tool_ok; chmod 755 "$COSIGN"
chmod 666 "$TRUSTED_ROOT"; no_ "trusted root world writable" tool_ok; chmod 644 "$TRUSTED_ROOT"
chmod 777 "$LIBEXEC"; no_ "libexec dir world writable" tool_ok; chmod 755 "$LIBEXEC"
SAVE_UID=$ROOT_UID; ROOT_UID=$((MYUID + 1)); no_ "tools not owned by root" tool_ok; ROOT_UID=$SAVE_UID
yes_ "tools ok again" tool_ok

# ================= 3. verification of the inputs =================
build good one
yes_ "good inputs verify" do_verify good
has "good: log says signature OK and hash matched" "$(cat "$T/out")" "manifest signature OK"
has "good: the command is cosign verify-blob" "$(cat "$T/cosargs")" "verify-blob"
for want in "--bundle $T/work" "--trusted-root $TRUSTED_ROOT" "--certificate-oidc-issuer $OIDC_ISSUER" "--certificate-identity $SIGNER_IDENTITY" "--certificate-github-workflow-repository $REPO" "--certificate-github-workflow-ref refs/tags/v5.1.0" "--certificate-github-workflow-sha $REL_SHA" "--certificate-github-workflow-trigger push"; do
  has "cosign arg: $want" "$(cat "$T/cosargs")" "$want"
done
eq "cosign has no key / insecure / tlog-skip flag" "$(grep -c -e '--key' -e 'insecure' -e 'skip' -e 'private-infrastructure' "$T/cosargs")" 0
# the identity is bound to the TAG being installed
VERSION=5.2.0; yes_ "verify for another tag (stub accepts)" do_verify good; has "cosign arg: ref follows VERSION" "$(cat "$T/cosargs")" "refs/tags/v5.2.0"; VERSION=5.1.0
# signature failures
build badsig two; echo BADSIG >>"$T/in/badsig.sigstore.json"
no_ "signature by another identity" do_verify badsig; has "badsig: reason shown" "$(cat "$T/out")" "manifest signature verification FAILED"
build swapped three; printf 'eve-pim-manifest=1\nrelease_sha=%s\npim_tree=%s\npim_sha256=%s\n' "$REL_SHA" "$OTHER_SHA" "$(sha256 "$T/in/swapped")" >"$T/in/swapped.MANIFEST"
no_ "manifest changed after signing" do_verify swapped
build sigfail four; SAVE_CS=$COSIGN_SHA256S; COSIGN_SHA256S=$H64; no_ "unpinned cosign: nothing verified" do_verify sigfail; COSIGN_SHA256S=$SAVE_CS
# malformed manifest (signed, but the wrong shape)
fake mal one; printf 'eve-pim-manifest=1\nrelease_sha=%s\npim_tree=%s\npim_sha256=%s\nnote=x\n' "$REL_SHA" "$SHA_PIN" "$(sha256 "$T/in/mal")" >"$T/in/mal.MANIFEST"; bundle mal
no_ "signed but malformed manifest" do_verify mal; has "malformed: reason" "$(cat "$T/out")" "manifest malformed"
# a validly signed manifest for ANOTHER source tree
fake othertree one; manifest othertree "$OTHER_SHA" "$(sha256 "$T/in/othertree")"
no_ "signed manifest for another source tree" do_verify othertree; has "other tree: reason" "$(cat "$T/out")" "not the pinned, security-reviewed pim/eve-pim source tree"
# the certificate must be bound to the commit the manifest names: a signature made at another commit (a tag moved, or a manifest copied from another run) is refused
fake bind one; manifest bind "$SHA_PIN" "$(sha256 "$T/in/bind")"; bundle bind "$OTHER_REL"
no_ "certificate commit differs from the manifest's release_sha" do_verify bind; has "bind: reason" "$(cat "$T/out")" "manifest signature verification FAILED"
fake bind2 one; manifest bind2 "$SHA_PIN" "$(sha256 "$T/in/bind2")" "$OTHER_REL"
no_ "manifest names a commit the certificate does not carry" do_verify bind2
fake bind3 one; manifest bind3 "$SHA_PIN" "$(sha256 "$T/in/bind3")" "$OTHER_REL"; bundle bind3 "$OTHER_REL"
yes_ "manifest and certificate agree on another commit: verifies, and cosign got that commit" do_verify bind3; has "bind3: cosign arg follows the manifest" "$(cat "$T/cosargs")" "--certificate-github-workflow-sha $OTHER_REL"
# hash mismatch: manifest signed for one binary, another presented
fake other two; no_ "binary differs from the manifest" do_verify good other; has "hash mismatch message" "$(cat "$T/out")" "does not match the manifest"
# code-signature rules
for m in BADCS NORUNTIME RTWORDONLY RTNOBIT RTTWOLINES; do build "cs$m" x "$m"; no_ "code signature: $m refused" do_verify "cs$m"; done
build entx x "ENT=com.apple.security.cs.allow-jit"; no_ "extra entitlement refused" do_verify entx; has "extra entitlement named" "$(cat "$T/out")" "entitlements beyond calendars"
build usg x "NSContactsUsageDescription"; no_ "extra usage string refused" do_verify usg
{ echo "ENT=com.apple.security.personal-information.calendars"; echo "PAYLOAD"; } >"$T/in/nocal"; manifest nocal "$SHA_PIN" "$(sha256 "$T/in/nocal")"; no_ "no calendar usage string refused" do_verify nocal
build calfull x "NSCalendarsFullAccessUsageDescription"; yes_ "calendar full-access usage string is allowed" do_verify calfull

# ================= 4. install (fixed directory, atomic, restore) =================
point_at "$T/opt"
build v1 one; yes_ "first install" do_install v1
eq "first install result" "$(cat "$T/res")" installed
eq "installed binary = the signed binary" "$(sha256 "$PIM_BIN")" "$(sha256 "$T/in/v1")"
eq "binary mode 755" "$(modeof "$PIM_BIN")" 755
eq "manifest + bundle recorded (644)" "$(modeof "$PIM_MF") $(modeof "$PIM_MF_SIG")" "644 644"
eq "recorded manifest = the verified one" "$(sha256 "$PIM_MF")" "$(sha256 "$T/in/v1.MANIFEST")"
eq "no temp files left" "$(leftovers)" 0
# same sha256 again: never replaced
ino=$(inodeof "$PIM_BIN"); yes_ "reinstall of the same adapter" do_install v1
eq "same: result" "$(cat "$T/res")" same; eq "same: binary not replaced (same inode)" "$(inodeof "$PIM_BIN")" "$ino"
# same bytes but a wrong mode: repaired in place, hash unchanged
chmod 700 "$PIM_BIN"; yes_ "same bytes, wrong mode" do_install v1; eq "wrong mode repaired" "$(modeof "$PIM_BIN")" 755
# a lost record is refreshed, the binary is not touched
rm -f "$PIM_MF" "$PIM_MF_SIG"; ino=$(inodeof "$PIM_BIN"); yes_ "record refreshed" do_install v1; eq "refresh: binary untouched" "$(inodeof "$PIM_BIN")" "$ino"; eq "refresh: record back" "$(modeof "$PIM_MF") $(modeof "$PIM_MF_SIG")" "644 644"
# upgrade
build v2 two; yes_ "upgrade" do_install v2; eq "upgrade: new binary" "$(sha256 "$PIM_BIN")" "$(sha256 "$T/in/v2")"; eq "upgrade: record follows" "$(sha256 "$PIM_MF")" "$(sha256 "$T/in/v2.MANIFEST")"
# a failing step during an upgrade: the previous adapter AND its record are restored
build v3 three; printf 'sigstore.json' >"$T/failpat"
no_ "upgrade whose last file cannot be written" do_install v3
eq "failed upgrade: previous binary restored" "$(sha256 "$PIM_BIN")" "$(sha256 "$T/in/v2")"
eq "failed upgrade: previous record restored" "$(sha256 "$PIM_MF")" "$(sha256 "$T/in/v2.MANIFEST")"
eq "failed upgrade: previous bundle restored" "$(sha256 "$PIM_MF_SIG")" "$(sha256 "$T/in/v2.sigstore.json")"
eq "failed upgrade: no temp files" "$(leftovers)" 0
: >"$T/failpat"
# a failing FIRST install leaves nothing behind
point_at "$T/opt2"; printf 'eve-pim.manifest' >"$T/failpat"
no_ "first install that fails" do_install v1
eq "failed first install: no binary" "$([ -e "$PIM_BIN" ] && echo yes || echo no)" no
eq "failed first install: no record" "$([ -e "$PIM_MF" ] || [ -e "$PIM_MF_SIG" ] && echo yes || echo no)" no
: >"$T/failpat"
# the directory must be root-owned and closed
point_at "$T/opt3"; chmod 777 "$PIM_DIR"; no_ "bin dir world writable" do_install v1; eq "nothing installed into an open dir" "$([ -e "$PIM_BIN" ] && echo yes || echo no)" no
point_at "$T/opt4"; chmod 775 "$SOCK_BASE"; no_ "parent dir group writable" do_install v1
point_at "$T/opt5"; SAVE_UID=$ROOT_UID; ROOT_UID=$((MYUID + 1)); no_ "bin dir not owned by root" do_install v1; ROOT_UID=$SAVE_UID
point_at "$T/opt6"; rm -rf "$PIM_DIR"; mkdir "$T/elsewhere"; chmod 755 "$T/elsewhere"; ln -s "$T/elsewhere" "$PIM_DIR"; no_ "bin dir is a symlink" do_install v1; eq "nothing installed through the symlink" "$(ls -A "$T/elsewhere" | wc -l | tr -d ' ')" 0
# a planted symlink or directory at the target is never followed
point_at "$T/opt7"; ln -s "$T/elsewhere/x" "$PIM_BIN"; yes_ "symlink at the target is replaced, not followed" do_install v1; eq "target is a regular file now" "$([ -L "$PIM_BIN" ] && echo link || echo file)" file; eq "nothing written through the link" "$(ls -A "$T/elsewhere" | wc -l | tr -d ' ')" 0
point_at "$T/opt8"; mkdir "$PIM_BIN"; no_ "directory at the target refused" do_install v1

echo "pim adapter tests passed: $PASS failed: $FAILN"
[ "$FAILN" = 0 ]
