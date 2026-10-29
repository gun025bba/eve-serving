#!/bin/bash
# Behaviour tests of deploy/install-calendar-adapter.sh (verify + atomic install + rollback of the signed calendar adapter).
# Runs unprivileged on Linux and macOS (bash 3.2): the installer is SOURCED (it only defines functions then), its fixed paths are re-pointed at a
# temp dir, the real `ssh-keygen -Y sign/verify` and the real eve-deploy verifier (verify_sig) do the signature work, only `codesign` is a stub.
#   bash deploy/test-calendar-adapter.sh
# Needs: bash, ssh-keygen, install, sha256sum or shasum, python3 not needed.
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
INS=$HERE/install-calendar-adapter.sh
umask 022
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()   { PASS=$((PASS + 1)); }
bad()  { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()   { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
yes_() { local n=$1; shift; if "$@" >/dev/null 2>&1; then ok; else bad "$n: expected success: $*"; fi; }
no_()  { local n=$1; shift; if "$@" >/dev/null 2>&1; then bad "$n: expected failure: $*"; else ok; fi; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
modeof() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
inodeof() { stat -c %i "$1" 2>/dev/null || stat -f %i "$1"; }
ME=$(id -un); MYUID=$(id -u); MYGID=$(id -g)

# shellcheck source=install-calendar-adapter.sh
. "$INS" || { echo "cannot source the installer"; exit 1; }
SHA_PIN=$PIM_SHA
OTHER_SHA=$(printf 'b%.0s' $(seq 1 40))

# ---------------- trust root: temp key, signers file, copy of the real entrypoint (its verify_sig) ----------------
mkdir -p "$T/libexec" "$T/in" "$T/bin" "$T/key2"
chmod 755 "$T/libexec"
ssh-keygen -q -t ed25519 -N '' -f "$T/key" >/dev/null || { echo "ssh-keygen failed"; exit 1; }
ssh-keygen -q -t ed25519 -N '' -f "$T/key2/key" >/dev/null
printf 'eve-release namespaces="eve-deploy" %s\n' "$(cut -d' ' -f1,2 "$T/key.pub")" >"$T/libexec/allowed_signers"
cp "$HERE/nopasswd/eve-deploy" "$T/libexec/deploy"; chmod 755 "$T/libexec/deploy"
DEPLOY_ENTRY=$T/libexec/deploy
ROOT_UID=$MYUID; ROOT_GID=$MYGID; STAGE_UID=$MYUID; OWNER_ARGS=""
eve_deploy_overrides() { SIGNERS=$T/libexec/allowed_signers; ROOT_UID=$MYUID; }

# ---------------- codesign stub: reads markers from the fake binary ----------------
cat >"$T/bin/codesign" <<'STUB'
#!/bin/bash
for f; do :; done
case "$1" in
  --verify) grep -aq BADSIG "$f" && exit 1; exit 0;;
  -dv) if grep -aq NORUNTIME "$f"; then echo "CodeDirectory v=20500 flags=0x2(adhoc) hashes=1" >&2; else echo "CodeDirectory v=20500 flags=0x10002(adhoc,runtime) hashes=1" >&2; fi; exit 0;;
  -d) echo '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>'
      grep -a -o 'ENT=[^ ]*' "$f" | sed 's/^ENT=//; s/.*/<key>&<\/key><true\/>/'
      echo '</dict></plist>'; exit 0;;
esac
exit 2
STUB
chmod 755 "$T/bin/codesign"; CODESIGN=$T/bin/codesign

# fake adapters: a text file; markers steer the stub
fake() { # $1 name $2 payload [extra marker lines...]
  local n=$1 p=$2; shift 2
  { echo "NSCalendarsUsageDescription"; echo "ENT=com.apple.security.personal-information.calendars"; echo "PAYLOAD-$p"; for m in "$@"; do echo "$m"; done; } >"$T/in/$n"
}
sign_manifest() { # $1 name (files $T/in/$1.MANIFEST) [$2 key] [$3 namespace]
  rm -f "$T/in/$1.MANIFEST.sig"
  ssh-keygen -q -Y sign -f "${2:-$T/key}" -n "${3:-eve-deploy}" "$T/in/$1.MANIFEST" >/dev/null 2>&1
}
manifest() { # $1 name $2 pim_sha $3 pim_sha256 -> signed 3-line manifest
  printf 'eve-pim-manifest=1\npim_sha=%s\npim_sha256=%s\n' "$2" "$3" >"$T/in/$1.MANIFEST"; sign_manifest "$1"
}
build() { # $1 name $2 payload [markers]: adapter + good signed manifest for the pinned commit
  local n=$1; fake "$@"; manifest "$n" "$SHA_PIN" "$(sha256 "$T/in/$n")"
}
point_at() { # $1 base dir: fresh installed-state directory tree
  SOCK_BASE=$1; PIM_DIR=$SOCK_BASE/bin; PIM_BIN=$PIM_DIR/eve-pim; PIM_SB=$PIM_DIR/eve-pim.sb
  PIM_MF=$PIM_BIN.manifest; PIM_MF_SIG=$PIM_MF.sig; PIM_PREV=$PIM_BIN.prev; PIM_PREV_MF=$PIM_PREV.manifest; PIM_PREV_SIG=$PIM_PREV_MF.sig
  rm -rf "$1"; mkdir -p "$PIM_DIR"; chmod 755 "$SOCK_BASE" "$PIM_DIR"
}
newwork() { WORK=$T/work.$$.$RANDOM; mkdir -p "$WORK"; chmod 700 "$WORK"; LOG=$WORK/log; : >"$LOG"; }
# verify + install in a subshell; prints nothing, leaves the result in $T/res and the exit code in $?
do_install() { # $1 name
  ( newwork; verify_inputs "$T/in/$1.MANIFEST" "$T/in/$1.MANIFEST.sig" "$T/in/$1" && install_adapter; rc=$?; echo "${INSTALL_RESULT:-}" >"$T/res"; exit $rc ) >"$T/out" 2>&1
}
do_verify() { ( newwork; verify_inputs "$T/in/$1.MANIFEST" "$T/in/$1.MANIFEST.sig" "$T/in/${2:-$1}" ) >"$T/out" 2>&1; }
do_rollback() { ( newwork; rollback_adapter; rc=$?; echo "${INSTALL_RESULT:-}" >"$T/res"; exit $rc ) >"$T/out" 2>&1; }
installed_sha() { sha256 "$PIM_BIN"; }
leftovers() { ls -A "$PIM_DIR" | grep -c '\.new\.' ; }

# ================= 1. manifest format (parse_manifest) =================
H40=$(printf 'a%.0s' $(seq 1 40)); H64=$(printf 'c%.0s' $(seq 1 64))
pm() { printf "$1" >"$T/m"; parse_manifest "$T/m"; }
yes_ "good manifest" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64\n"
yes_ "no trailing newline" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64"
yes_ "any order" pm "pim_sha256=$H64\neve-pim-manifest=1\npim_sha=$H40\n"
eq "parsed pim_sha" "$P_SHA" "$H40"; eq "parsed pim_sha256" "$P_SHA256" "$H64"
no_ "pim_sha uppercase" pm "eve-pim-manifest=1\npim_sha=$(echo $H40 | tr a A)\npim_sha256=$H64\n"
no_ "pim_sha 39 hex" pm "eve-pim-manifest=1\npim_sha=${H40%?}\npim_sha256=$H64\n"
no_ "pim_sha 41 hex" pm "eve-pim-manifest=1\npim_sha=${H40}a\npim_sha256=$H64\n"
no_ "pim_sha not hex" pm "eve-pim-manifest=1\npim_sha=${H40%?}g\npim_sha256=$H64\n"
no_ "pim_sha short ref" pm "eve-pim-manifest=1\npim_sha=6ce1993\npim_sha256=$H64\n"
no_ "pim_sha empty" pm "eve-pim-manifest=1\npim_sha=\npim_sha256=$H64\n"
no_ "pim_sha with space" pm "eve-pim-manifest=1\npim_sha=${H40} \npim_sha256=$H64\n"
no_ "pim_sha CR" pm "eve-pim-manifest=1\r\npim_sha=$H40\r\npim_sha256=$H64\r\n"
no_ "pim_sha256 63 hex" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=${H64%?}\n"
no_ "pim_sha256 uppercase" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$(echo $H64 | tr c C)\n"
no_ "pim_sha256 40 hex" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H40\n"
no_ "missing version line" pm "pim_sha=$H40\npim_sha256=$H64\n"
no_ "missing pim_sha256" pm "eve-pim-manifest=1\npim_sha=$H40\n"
no_ "wrong version" pm "eve-pim-manifest=2\npim_sha=$H40\npim_sha256=$H64\n"
no_ "extra line" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64\nnote=x\n"
no_ "extra blank line" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64\n\n"
no_ "duplicate key" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha=$H40\n"
no_ "unknown key (sha256 spelled other)" pm "eve-pim-manifest=1\npim_sha=$H40\nsha256=$H64\n"
no_ "line without =" pm "eve-pim-manifest=1\npim_sha=$H40\n$H64\n"
no_ "NUL byte" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=$H64\n\000"
no_ "NUL inside value" pm "eve-pim-manifest=1\npim_sha=$H40\npim_sha256=${H64%??}\000a\n"
no_ "empty file" pm ""
no_ "missing file" parse_manifest "$T/nonexistent"
ln -sf "$T/m" "$T/m.link"; printf 'eve-pim-manifest=1\npim_sha=%s\npim_sha256=%s\n' "$H40" "$H64" >"$T/m"
no_ "symlinked manifest" parse_manifest "$T/m.link"
head -c 2000 /dev/zero | tr '\0' 'a' >"$T/m.big"; no_ "oversized manifest" parse_manifest "$T/m.big"

# ================= 2. verification of the inputs =================
build good one
yes_ "good inputs verify" do_verify good
eq "good: install log says matched" "$(grep -c 'sha256 matches the manifest' "$T/out")" 1
# hash mismatch: manifest signed for another binary, or the binary swapped after signing
fake other two; yes_ "(setup) other adapter built" test -f "$T/in/other"
no_ "hash mismatch: different binary than the manifest says" do_verify good other
grep -q "does not match the manifest" "$T/out" && ok || bad "hash mismatch message"
cp "$T/in/good" "$T/in/tampered"; echo tail >>"$T/in/tampered"
no_ "hash mismatch: one byte appended" do_verify good tampered
manifest hm "$SHA_PIN" "$(printf 'd%.0s' $(seq 1 64))"; cp "$T/in/good" "$T/in/hm"
no_ "hash mismatch: manifest carries an arbitrary sha256" do_verify hm
# pim_sha: pinned commit only, and strict format (a SIGNED manifest with a bad pim_sha is still refused)
fake pa one; manifest pa "$OTHER_SHA" "$(sha256 "$T/in/pa")"
no_ "pim_sha is another (valid-format) commit" do_verify pa
grep -q "not the pinned" "$T/out" && ok || bad "pim_sha mismatch message"
for bad_sha in "${SHA_PIN%?}" "${SHA_PIN}0" "$(echo $SHA_PIN | tr a-f A-F)" "${SHA_PIN:0:7}" "${SHA_PIN%?}z" ""; do
  fake pb one; manifest pb "$bad_sha" "$(sha256 "$T/in/pb")"
  no_ "signed manifest with bad pim_sha [$bad_sha]" do_verify pb
  grep -q "malformed" "$T/out" && ok || bad "bad pim_sha message [$bad_sha]"
done
fake pc one; printf 'eve-pim-manifest=1\npim_sha=%s\npim_sha256=%s\nextra=1\n' "$SHA_PIN" "$(sha256 "$T/in/pc")" >"$T/in/pc.MANIFEST"; sign_manifest pc
no_ "signed manifest with an extra line" do_verify pc
# signature
cp "$T/in/good.MANIFEST" "$T/in/sig1.MANIFEST"; cp "$T/in/good.MANIFEST.sig" "$T/in/sig1.MANIFEST.sig"; cp "$T/in/good" "$T/in/sig1"
echo "# edited" >>"$T/in/sig1.MANIFEST"
no_ "manifest edited after signing" do_verify sig1
cp "$T/in/good.MANIFEST" "$T/in/sig2.MANIFEST"; cp "$T/in/good" "$T/in/sig2"; sign_manifest sig2 "$T/key2/key"
no_ "signed by another key" do_verify sig2
cp "$T/in/good.MANIFEST" "$T/in/sig3.MANIFEST"; cp "$T/in/good" "$T/in/sig3"; sign_manifest sig3 "$T/key" other-namespace
no_ "signed in another namespace" do_verify sig3
cp "$T/in/good.MANIFEST" "$T/in/sig4.MANIFEST"; cp "$T/in/good" "$T/in/sig4"; : >"$T/in/sig4.MANIFEST.sig"
no_ "empty signature" do_verify sig4
cp "$T/in/good.MANIFEST" "$T/in/sig5.MANIFEST"; cp "$T/in/good" "$T/in/sig5"; head -c 100 /dev/urandom >"$T/in/sig5.MANIFEST.sig"
no_ "garbage signature" do_verify sig5
# trust root: the entrypoint must be root-owned and closed
( DEPLOY_ENTRY=$T/libexec/missing; do_verify good ); no_ "missing verifier entrypoint" bash -c 'exit 1'
( DEPLOY_ENTRY=$T/libexec/missing; newwork; verify_inputs "$T/in/good.MANIFEST" "$T/in/good.MANIFEST.sig" "$T/in/good" ) >/dev/null 2>&1; eq "no verifier: refused" "$?" 1
chmod 777 "$T/libexec/deploy"; do_verify good; eq "verifier entrypoint world-writable: refused" "$?" 1; chmod 755 "$T/libexec/deploy"
( ROOT_UID=$((MYUID + 1)); STAGE_UID=$MYUID; do_verify good ); do_verify good; eq "(control) verifies again" "$?" 0
# code-signature / entitlement / usage-key checks of the adapter itself
chk() { shift; build cs p "$@"; do_verify cs; }   # bash 3.2 + set -u: only "$@" (never "${@:2}")
chk p BADSIG; eq "invalid code signature refused" "$?" 1
chk p NORUNTIME; eq "no hardened runtime refused" "$?" 1
chk p "ENT=com.apple.security.personal-information.addressbook"; eq "contacts entitlement refused" "$?" 1
chk p "ENT=com.apple.security.personal-information.photos-library"; eq "photos entitlement refused" "$?" 1
chk p "ENT=com.apple.security.files.all"; eq "full-disk-style entitlement refused" "$?" 1
chk p "ENT=com.apple.security.cs.allow-jit"; eq "JIT entitlement refused" "$?" 1
chk p "ENT=com.apple.security.network.client"; eq "network entitlement refused" "$?" 1
chk p "NSContactsUsageDescription"; eq "contacts usage string refused" "$?" 1
chk p "NSRemindersUsageDescription"; eq "reminders usage string refused" "$?" 1
chk p "NSRemindersFullAccessUsageDescription"; eq "reminders (full access) usage string refused" "$?" 1
chk p "NSSystemAdministrationUsageDescription"; eq "system administration usage string refused" "$?" 1
chk p "NSDesktopFolderUsageDescription"; eq "desktop folder usage string refused" "$?" 1
chk p "NSCalendarsFullAccessUsageDescription"; eq "calendar-only strings pass (full access)" "$?" 0
chk p; eq "calendar-only adapter passes" "$?" 0
{ echo "ENT=com.apple.security.personal-information.calendars"; echo "PAYLOAD-x"; } >"$T/in/nocal"; manifest nocal "$SHA_PIN" "$(sha256 "$T/in/nocal")"
do_verify nocal; eq "adapter without the calendar usage string refused" "$?" 1
# staged-input rules
ln -sf "$T/in/good" "$T/in/good.link"; do_verify good good.link; eq "symlinked adapter refused" "$?" 1
( ROOT_UID=$((MYUID + 1)); STAGE_UID=""; newwork; stage_copy "$T/in/good" "$WORK/x" 1000000 ) >/dev/null 2>&1; eq "input owned by a stranger refused" "$?" 1
( STAGE_UID=$MYUID; newwork; stage_copy "$T/in/good" "$WORK/x" 1000000 ) >/dev/null 2>&1; eq "input owned by the login user accepted" "$?" 0
( newwork; stage_copy "$T/in/good" "$WORK/x" 10 ) >/dev/null 2>&1; eq "oversized input refused" "$?" 1
: >"$T/in/empty"; ( newwork; stage_copy "$T/in/empty" "$WORK/x" 1000 ) >/dev/null 2>&1; eq "empty input refused" "$?" 1

# ================= 3. install: fresh, atomic, same-hash no-op, post-install re-verify =================
point_at "$T/o1"; build A a; build B b; build C c
do_install A; eq "fresh install rc" "$?" 0; eq "fresh install result" "$(cat "$T/res")" installed
eq "installed hash = manifest pim_sha256" "$(installed_sha)" "$(sha256 "$T/in/A")"
eq "installed mode 755" "$(modeof "$PIM_BIN")" 755
eq "manifest recorded 644" "$(modeof "$PIM_MF")" 644; eq "signature recorded 644" "$(modeof "$PIM_MF_SIG")" 644
yes_ "recorded manifest is the signed one" cmp -s "$PIM_MF" "$T/in/A.MANIFEST"
eq "no temp files left" "$(leftovers)" 0
eq "no previous kept after the first install" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
# same hash: nothing is replaced (inode and mtime stay), and nothing is rewritten
ino=$(inodeof "$PIM_BIN"); touch -d '2001-02-03 04:05:06' "$PIM_BIN" 2>/dev/null || touch -t 200102030405.06 "$PIM_BIN"; mt=$(stat -c %Y "$PIM_BIN" 2>/dev/null || stat -f %m "$PIM_BIN")
cat >"$T/bin/install-log" <<STUB
#!/bin/bash
echo "\$*" >>"$T/install.calls"
exec /usr/bin/install "\$@"
STUB
chmod 755 "$T/bin/install-log"; : >"$T/install.calls"; INSTALL=$T/bin/install-log
do_install A; eq "same-hash rc" "$?" 0; eq "same-hash result" "$(cat "$T/res")" same
eq "same-hash: inode unchanged (not replaced)" "$(inodeof "$PIM_BIN")" "$ino"
eq "same-hash: mtime unchanged" "$(stat -c %Y "$PIM_BIN" 2>/dev/null || stat -f %m "$PIM_BIN")" "$mt"
eq "same-hash: install(1) never called" "$(wc -l <"$T/install.calls" | tr -d ' ')" 0
eq "same-hash: no previous created" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
# same bytes, different (e.g. re-signed) manifest for the same sha256 and same pin: binary still untouched
manifest A2 "$SHA_PIN" "$(sha256 "$T/in/A")"; cp "$T/in/A" "$T/in/A2"
do_install A2; eq "same hash, new signed record: result" "$(cat "$T/res")" same
eq "same hash, new signed record: binary inode unchanged" "$(inodeof "$PIM_BIN")" "$ino"
yes_ "same hash: the signed record is refreshed" cmp -s "$PIM_MF" "$T/in/A2.MANIFEST"
# same hash but wrong mode: repaired by an atomic replace, still no rollback rotation
chmod 700 "$PIM_BIN"; do_install A; eq "same bytes wrong mode: repaired" "$(modeof "$PIM_BIN")" 755
eq "same bytes wrong mode: no rotation" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
INSTALL=/usr/bin/install
# upgrade A -> B: atomic rename (new inode; an open handle still reads the old content), previous kept with its signed record
exec 7<"$PIM_BIN"; ino=$(inodeof "$PIM_BIN")
do_install B; eq "upgrade rc" "$?" 0; eq "upgrade result" "$(cat "$T/res")" installed
eq "upgrade: new hash" "$(installed_sha)" "$(sha256 "$T/in/B")"
[ "$(inodeof "$PIM_BIN")" != "$ino" ] && ok || bad "upgrade replaced in place (same inode): not an atomic rename"
eq "upgrade: an open handle still sees the OLD complete file" "$(cat <&7 | cksum)" "$(cksum <"$T/in/A")"; exec 7<&-
eq "upgrade: previous = A" "$(sha256 "$PIM_PREV")" "$(sha256 "$T/in/A")"
yes_ "upgrade: previous manifest = A's signed manifest" cmp -s "$PIM_PREV_MF" "$T/in/A.MANIFEST"
yes_ "upgrade: previous signature = A's" cmp -s "$PIM_PREV_SIG" "$T/in/A.MANIFEST.sig"
eq "upgrade: previous mode 755" "$(modeof "$PIM_PREV")" 755
yes_ "upgrade: record = B's manifest" cmp -s "$PIM_MF" "$T/in/B.MANIFEST"
eq "upgrade: no temp files" "$(leftovers)" 0
do_install C; eq "second upgrade: previous is B (only ONE rollback step is kept)" "$(sha256 "$PIM_PREV")" "$(sha256 "$T/in/B")"
# the temp file lives in the SAME directory as the target and is root:wheel + mode set by install(1) (checked through the stub)
point_at "$T/o2"; : >"$T/install.calls"; INSTALL=$T/bin/install-log; OWNER_ARGS="-o $ME"
do_install A
grep -q -- "-o $ME -m 755 $WORK" "$T/install.calls" 2>/dev/null; # WORK is per call: check the shape instead
grep -E -- "-o $ME -m 755 .*/in\.eve-pim $PIM_DIR/\.eve-pim\.new\.[0-9]+$" "$T/install.calls" >/dev/null && ok || bad "temp file is not created in the target directory with owner+mode options: $(head -3 "$T/install.calls")"
OWNER_ARGS=""; INSTALL=/usr/bin/install
# atomic: a failing rename leaves the old binary and record untouched, no temp file left, install refused
point_at "$T/o3"; do_install A; before=$(installed_sha); recb=$(cksum <"$PIM_MF")
printf '#!/bin/bash\nexit 1\n' >"$T/bin/mv-fail"; chmod 755 "$T/bin/mv-fail"; MV=$T/bin/mv-fail
do_install B; eq "failing rename: install fails" "$?" 1
eq "failing rename: old adapter intact" "$(installed_sha)" "$before"
eq "failing rename: old record intact" "$(cksum <"$PIM_MF")" "$recb"
eq "failing rename: no temp left" "$(leftovers)" 0
MV=/bin/mv
# fresh install + failing rename: nothing installed
point_at "$T/o4"; MV=$T/bin/mv-fail; do_install A; eq "fresh + failing rename: fails" "$?" 1
[ ! -e "$PIM_BIN" ] && ok || bad "fresh + failing rename left a binary"; eq "fresh + failing rename: no temp" "$(leftovers)" 0; MV=/bin/mv
# post-install re-verify: the rename 'succeeds' but the installed file is not what was verified -> old state restored
cat >"$T/bin/mv-corrupt" <<'STUB'
#!/bin/bash
/bin/mv "$@" || exit 1
# corrupts the FIRST rename onto the binary only (the restore that follows must work)
for last; do :; done
case "$last" in */eve-pim) [ -e "$CORRUPT_FLAG" ] && { rm -f "$CORRUPT_FLAG"; echo corrupt >>"$last"; };; esac
STUB
chmod 755 "$T/bin/mv-corrupt"; export CORRUPT_FLAG=$T/corrupt.flag
point_at "$T/o5"; do_install A; before=$(installed_sha); recb=$(cksum <"$PIM_MF")
touch "$CORRUPT_FLAG"; MV=$T/bin/mv-corrupt; do_install B; rc=$?; MV=/bin/mv
eq "post-install corruption: install fails" "$rc" 1
eq "post-install corruption: previous adapter restored" "$(installed_sha)" "$before"
eq "post-install corruption: record restored" "$(cksum <"$PIM_MF")" "$recb"
eq "post-install corruption: mode restored" "$(modeof "$PIM_BIN")" 755
eq "post-install corruption: no temp" "$(leftovers)" 0
point_at "$T/o6"; touch "$CORRUPT_FLAG"; MV=$T/bin/mv-corrupt; do_install A; rc=$?; MV=/bin/mv
eq "post-install corruption on a fresh install: fails" "$rc" 1
[ ! -e "$PIM_BIN" ] && [ ! -e "$PIM_MF" ] && ok || bad "unverified adapter or record left behind"
# the manifest put fails after the binary was replaced: binary AND record restored together
cat >"$T/bin/install-failmf" <<STUB
#!/bin/bash
for a; do case "\$a" in *.manifest.new.*) exit 1;; esac; done
exec /usr/bin/install "\$@"
STUB
chmod 755 "$T/bin/install-failmf"
point_at "$T/o7"; do_install A; before=$(installed_sha); recb=$(cksum <"$PIM_MF")
INSTALL=$T/bin/install-failmf; do_install B; rc=$?; INSTALL=/usr/bin/install
eq "record write fails: install fails" "$rc" 1
eq "record write fails: old adapter back" "$(installed_sha)" "$before"
eq "record write fails: old record intact" "$(cksum <"$PIM_MF")" "$recb"
# rollback record rules during an upgrade
point_at "$T/o8"; do_install A; rm -f "$PIM_MF_SIG"; do_install B
eq "installed adapter without a signed record: no rollback kept" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
point_at "$T/o9"; do_install A; cp "$T/in/B.MANIFEST" "$PIM_MF"; do_install C
eq "record that does not match the installed adapter: no rollback kept" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
point_at "$T/o10"; do_install A; echo x >>"$PIM_MF"; do_install B
eq "record with a broken signature: no rollback kept" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
# directories: group/world-writable or symlinked target dirs are refused, nothing is written
point_at "$T/o11"; chmod 775 "$PIM_DIR"; do_install A; eq "group-writable bin dir refused" "$?" 1
[ ! -e "$PIM_BIN" ] && ok || bad "installed into a writable dir"
point_at "$T/o12"; chmod 777 "$SOCK_BASE"; do_install A; eq "world-writable base dir refused" "$?" 1
point_at "$T/o13"; rm -rf "$T/real"; mkdir "$T/real"; chmod 755 "$T/real"; rm -rf "$PIM_DIR"; ln -s "$T/real" "$PIM_DIR"; do_install A; eq "symlinked bin dir refused" "$?" 1
point_at "$T/o14"; ( ROOT_UID=$((MYUID + 1)); newwork; bin_dir_ok ) ; eq "bin dir not owned by root refused" "$?" 1
# a failed verification installs nothing
point_at "$T/o15"; do_install hm; eq "hash mismatch: nothing installed" "$(ls -A "$PIM_DIR" | wc -l | tr -d ' ')" 0
point_at "$T/o16"; do_install sig2; eq "bad signature: nothing installed" "$(ls -A "$PIM_DIR" | wc -l | tr -d ' ')" 0
point_at "$T/o17"; do_install pa; eq "unpinned commit: nothing installed" "$(ls -A "$PIM_DIR" | wc -l | tr -d ' ')" 0
# DRY_RUN
point_at "$T/o18"; ( DRY_RUN=1; newwork; verify_inputs "$T/in/A.MANIFEST" "$T/in/A.MANIFEST.sig" "$T/in/A" && install_adapter ) >/dev/null 2>&1
eq "DRY_RUN installs nothing" "$(ls -A "$PIM_DIR" | wc -l | tr -d ' ')" 0

# ================= 4. rollback =================
point_at "$T/r1"; do_install A; do_install B
do_rollback; eq "rollback rc" "$?" 0; eq "rollback result" "$(cat "$T/res")" rolled-back
eq "rollback: installed = A" "$(installed_sha)" "$(sha256 "$T/in/A")"
eq "rollback: mode 755" "$(modeof "$PIM_BIN")" 755
yes_ "rollback: record = A's signed manifest" cmp -s "$PIM_MF" "$T/in/A.MANIFEST"
eq "rollback: the kept previous is consumed (one step only)" "$(ls "$PIM_DIR" | grep -c 'prev')" 0
eq "rollback: no temp files" "$(leftovers)" 0
do_rollback; eq "second rollback refused (nothing kept)" "$?" 1
eq "second rollback: still A" "$(installed_sha)" "$(sha256 "$T/in/A")"
# rollback is an atomic rename of a verified file
point_at "$T/r2"; do_install A; do_install B; ino=$(inodeof "$PIM_BIN"); do_rollback
[ "$(inodeof "$PIM_BIN")" != "$ino" ] && ok || bad "rollback replaced in place"
# refused: tampered previous binary / manifest / signature / mode
point_at "$T/r3"; do_install A; do_install B; echo x >>"$PIM_PREV"; before=$(installed_sha)
do_rollback; eq "tampered previous binary: refused" "$?" 1; eq "tampered previous binary: installed untouched" "$(installed_sha)" "$before"
point_at "$T/r4"; do_install A; do_install B; echo x >>"$PIM_PREV_MF"; do_rollback; eq "tampered previous manifest: refused" "$?" 1
point_at "$T/r5"; do_install A; do_install B; : >"$PIM_PREV_SIG"; do_rollback; eq "emptied previous signature: refused" "$?" 1
point_at "$T/r6"; do_install A; do_install B; chmod 777 "$PIM_PREV"; do_rollback; eq "previous adapter with the wrong mode: refused" "$?" 1
point_at "$T/r7"; do_install A; do_install B; ( ROOT_UID=$((MYUID + 1)); newwork; rollback_adapter ) >/dev/null 2>&1; eq "previous adapter not root-owned: refused" "$?" 1
point_at "$T/r8"; do_install A; do_install B; rm -f "$PIM_PREV_MF"; do_rollback; eq "previous without its record: refused" "$?" 1
point_at "$T/r9"; do_rollback; eq "no previous at all: refused" "$?" 1
point_at "$T/r10"; do_install A; do_install B; cp "$T/in/other" "$PIM_PREV"; chmod 755 "$PIM_PREV"; do_rollback; eq "previous swapped for another binary (hash differs from its manifest): refused" "$?" 1
# previous fails the code-signature rules now (e.g. a manifest-correct binary that carries a contacts entitlement)
point_at "$T/r11"; build CS1 s1 "ENT=com.apple.security.personal-information.addressbook"; do_install A; do_install B
cp "$T/in/CS1" "$PIM_PREV"; chmod 755 "$PIM_PREV"; cp "$T/in/CS1.MANIFEST" "$PIM_PREV_MF"; cp "$T/in/CS1.MANIFEST.sig" "$PIM_PREV_SIG"; do_rollback
eq "previous with a contacts entitlement: refused" "$?" 1; eq "...installed untouched" "$(installed_sha)" "$(sha256 "$T/in/B")"
# the previous is signed for another pin: still a signed manifest of the release key, the rules are the manifest's own pim_sha256 -> allowed
# identical hash: no-op
point_at "$T/r12"; do_install A; cp "$T/in/A" "$PIM_PREV"; chmod 755 "$PIM_PREV"; cp "$T/in/A.MANIFEST" "$PIM_PREV_MF"; cp "$T/in/A.MANIFEST.sig" "$PIM_PREV_SIG"
ino=$(inodeof "$PIM_BIN"); do_rollback; eq "previous == installed: rc" "$?" 0; eq "previous == installed: result" "$(cat "$T/res")" same
eq "previous == installed: not replaced" "$(inodeof "$PIM_BIN")" "$ino"
# atomic failure during rollback: state before is restored, previous kept
point_at "$T/r13"; do_install A; do_install B; MV=$T/bin/mv-fail; do_rollback; rc=$?; MV=/bin/mv
eq "failing rename in rollback: fails" "$rc" 1; eq "failing rename in rollback: still B" "$(installed_sha)" "$(sha256 "$T/in/B")"
eq "failing rename in rollback: previous kept" "$(sha256 "$PIM_PREV")" "$(sha256 "$T/in/A")"; eq "failing rename in rollback: no temp" "$(leftovers)" 0
point_at "$T/r14"; do_install A; do_install B; touch "$CORRUPT_FLAG"; MV=$T/bin/mv-corrupt; do_rollback; rc=$?; MV=/bin/mv
eq "post-rollback corruption: fails" "$rc" 1; eq "post-rollback corruption: B restored" "$(installed_sha)" "$(sha256 "$T/in/B")"
yes_ "post-rollback corruption: record restored" cmp -s "$PIM_MF" "$T/in/B.MANIFEST"
point_at "$T/r15"; do_install A; do_install B; chmod 775 "$PIM_DIR"; do_rollback; eq "rollback into a writable directory: refused" "$?" 1

# ================= 5. arguments =================
yes_ "args install" parse_args --manifest /m --sig /s --adapter /a; eq "args parsed" "$MODE $A_MF $A_SIG $A_BIN" "install /m /s /a"
yes_ "args install any order" parse_args --adapter /a --sig /s --manifest /m; eq "args parsed (order)" "$MODE $A_MF $A_SIG $A_BIN" "install /m /s /a"
yes_ "args rollback" parse_args --rollback; eq "mode rollback" "$MODE" rollback
no_ "no args" parse_args
no_ "relative path" parse_args --manifest m --sig /s --adapter /a
no_ "missing adapter" parse_args --manifest /m --sig /s
no_ "duplicate flag" parse_args --manifest /m --manifest /m2 --adapter /a
no_ "unknown flag" parse_args --manifest /m --sig /s --adapter /a --build
no_ "rollback + extra" parse_args --rollback now
no_ "rollback + install flags" parse_args --rollback --manifest /m --sig /s --adapter /a
no_ "empty value" parse_args --manifest "" --sig /s --adapter /a
no_ "old no-arg build mode" parse_args ""

# ================= 6. embedded launch files are the repo's =================
mkdir -p "$T/emb"; write_embedded "$T/emb"
python3 - "$HERE/launchd/com.eve.pim.plist.template" "$T/repo.tpl" <<'PY' 2>/dev/null || sed -e '/<!--/,/-->/d' "$HERE/launchd/com.eve.pim.plist.template" >"$T/repo.tpl"
import re, sys
s = open(sys.argv[1]).read()
open(sys.argv[2], "w").write(re.sub(r"<!--.*?-->\n", "", s, flags=re.S))
PY
yes_ "embedded LaunchAgent template == deploy/launchd/com.eve.pim.plist.template (minus its comment)" cmp -s "$T/emb/com.eve.pim.plist.template" "$T/repo.tpl"
grep -q '__PIM_DIR__/eve-pim' "$T/emb/com.eve.pim.plist.template" && ! grep -q '__HOME__/eve-pim' "$T/emb/com.eve.pim.plist.template" && ok || bad "LaunchAgent must run the binary from the fixed root-owned directory, not from the login home"
! grep -q 'writes' "$T/emb/com.eve.pim.plist.template" && ok || bad "writes flag in the template"

# ================= 7. the signing helper produces what the installer accepts =================
mkdir -p "$T/sgn"; fake sg z
yes_ "sign-pim-manifest.sh" bash "$HERE/nopasswd/sign-pim-manifest.sh" --key "$T/key" --git-sha "$SHA_PIN" --adapter "$T/in/sg" --out "$T/sgn"
cp "$T/in/sg" "$T/sgn/eve-pim"; ( newwork; verify_inputs "$T/sgn/eve-pim.MANIFEST" "$T/sgn/eve-pim.MANIFEST.sig" "$T/sgn/eve-pim" ) >/dev/null 2>&1; eq "installer accepts the helper's output" "$?" 0
eq "helper manifest has exactly 3 lines" "$(wc -l <"$T/sgn/eve-pim.MANIFEST" | tr -d ' ')" 3
no_ "sign helper refuses a short git sha" bash "$HERE/nopasswd/sign-pim-manifest.sh" --key "$T/key" --git-sha "${SHA_PIN:0:7}" --adapter "$T/in/sg" --out "$T/sgn2"
no_ "sign helper refuses uppercase git sha" bash "$HERE/nopasswd/sign-pim-manifest.sh" --key "$T/key" --git-sha "$(echo $SHA_PIN | tr a-f A-F)" --adapter "$T/in/sg" --out "$T/sgn2"

echo "calendar adapter installer tests: $PASS passed, $FAILN failed"
[ "$FAILN" = 0 ]
