#!/bin/bash
# setup-pim-adapter.sh: the root half of the calendar-adapter install (캘린더 어댑터 eve-pim, M1). It is run by eve-oneshot.sh INSIDE THE SAME sudo session as the other root steps (one password), as
#   sudo /bin/bash -c "<this checked text>" setup-pim-adapter --version X.Y.Z
# The text is trusted only by the sha256 pinned in eve-oneshot.sh (PIN_PIMADAPTER_SHA256), which is itself checked against the one main commit given as --merge-commit.
#
# The adapter is NOT built on the Mac (no git clone, no swift, no toolchain as root). CI builds and signs it; this script only VERIFIES and INSTALLS it:
#   downloads  eve-pim-X.Y.Z, eve-pim-X.Y.Z.MANIFEST, eve-pim-X.Y.Z.sigstore.json  from the release vX.Y.Z of gun025bba/eve-serving (into ONE mktemp -d, 0700; nothing downloaded is trusted yet)
# Trust (design "a", the same trust root as the passwordless deploy, no second one, no ssh key, no new sudoers verb):
#   1. the MANIFEST (exactly 4 lines: eve-pim-manifest=1, release_sha=<40 lowercase hex>, pim_tree=<40 lowercase hex>, pim_sha256=<64 lowercase hex>) carries a Sigstore KEYLESS signature: `cosign verify-blob`
#      against the pinned trusted root, with the pinned CI identity (the reusable workflow deploy-sign.yml@main), issuer, repository, triggering ref refs/tags/vX.Y.Z, trigger `push`, AND the commit the tag
#      points to (--certificate-github-workflow-sha = the manifest's release_sha, the same binding as eve-deploy). The cosign binary and the trust root are the ones the entrypoint installed (root-owned,
#      sha256-pinned below, re-checked here).
#   2. pim_tree == PIM_TREE below: the git TREE hash of pim/eve-pim at the release commit (the security-reviewed adapter SOURCE; a tree hash covers exactly the files, so no commit has to stay reachable
#      and a source change always changes it). The pin moves only with a reviewed change of this file + a new sha256 pin in eve-oneshot.sh.
#   3. sha256(adapter) == pim_sha256 of the verified manifest
#   4. the adapter is hardened-runtime code-signed, carries only the calendars entitlement and only calendar usage strings
# Install target: the fixed root-owned directory /opt/eve-pim/bin (root:wheel 0755, every parent root-owned and closed), temp file in the SAME directory, hash check, ONE atomic rename, re-verify
# (hash, owner, mode, code signature). A binary already installed with the same sha256 is never replaced (same code signature = no new TCC prompt). A failed install restores the previous adapter.
#
# Also (the same logic as the reviewed deploy/install-calendar-adapter.sh of #154): _evepim group (members exactly _evegw), socket dir, sandbox profile, the user's LaunchAgent, the TCC wait,
# a smoke test, and the gateway env keys. Flags stay OFF: GW_PIM_ENABLED / GW_BRIEFING_ENABLED / GW_PIM_WRITES_ENABLED are only ever ADDED as false; the LaunchAgent never passes --writes-enabled.
# Idempotent. Never prints calendar/reminder content, secrets, hostnames, logins or the owner user id. Written for macOS /bin/bash 3.2, Apple silicon.
#
# TCC (the one planned prompt): "eve-pim wants to access your calendar" -> Allow Full Access. Any OTHER prompt = [Don't Allow], keep PAUSE, report (docs/runbooks/unattended-deploy.md).
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure

# ===================== pinned inputs (edit only here; a change needs a new security review and a new sha256 pin in eve-oneshot.sh) =====================
PIM_TREE=46a431b9c988d280fa488afd7a80a073720b7651   # git tree hash of pim/eve-pim (`git rev-parse <commit>:pim/eve-pim`) as reviewed in #154 (commit 19cb222, which is not on main). 40 lowercase hex. CI computes the same value from the tag's checkout.
# The calendar adapter stays OFF until CI can sign it: deploy-sign.yml must publish eve-pim-X.Y.Z, .MANIFEST, .sigstore.json (separate, security-reviewed PR). Only that PR flips this to yes
# (together with a new pin in eve-oneshot.sh). While it is "no", both eve-oneshot.sh (before any login or sudo) and this script (before any download) refuse.
PIM_SIGNING_CI=no
REPO=gun025bba/eve-serving
REL_BASE=https://github.com/$REPO/releases/download
# the SAME trust root as /usr/local/libexec/eve/deploy (eve-deploy); a test fails if any of these differ from the entrypoint's constants
OIDC_ISSUER=https://token.actions.githubusercontent.com
SIGNER_IDENTITY=https://github.com/gun025bba/eve-serving/.github/workflows/deploy-sign.yml@refs/heads/main
SIGNER_TRIGGER=push
COSIGN_SHA256S="9f9b8cd557cf2e5b3df3373a00baa005a86b02391e3e06dd0ce37b3a1533d3a4 9d1e4abe2b6be279ed0d2ff706fde1ccd738747c834e3e6d44039c238867334f"
TRUSTED_ROOT_SHA256=6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66
# ==========================================================================================================================================================

# a root run never honours a test knob (the tests run unprivileged and re-assign the plain variables below after sourcing)
if [ "$(id -u)" = 0 ]; then for _v in $(compgen -e); do case "$_v" in EVE_*) unset "$_v";; esac; done; fi
unset _v
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

# ----- fixed locations and tools: plain assignments, the environment can never override them -----
LIBEXEC=/usr/local/libexec/eve
COSIGN=$LIBEXEC/cosign                      # root:wheel, installed by the entrypoint's installer, pinned by sha256 above
TRUSTED_ROOT=$LIBEXEC/trusted_root.json
STATE=/var/db/eve-deploy
PIM_GID_WANT=452                            # first-choice gid for _evepim; next free gid in 452..499 is used if taken
SOCK_BASE=/opt/eve-pim                      # root:wheel 0755; the socket dir below it is <login>:_evepim 0750
PIM_DIR=$SOCK_BASE/bin                      # root:wheel 0755; nothing in it is writable by the login user or _evegw
PIM_BIN=$PIM_DIR/eve-pim
PIM_SB=$PIM_DIR/eve-pim.sb
PIM_MF=$PIM_BIN.manifest                    # the verified, signed manifest + Sigstore bundle of the installed binary (audit record)
PIM_MF_SIG=$PIM_BIN.sigstore.json
OWNER_ARGS="-o root -g wheel"               # tests clear this
TRUST_ROOT=/                                # directory chains are checked up to here (tests change this)
ROOT_UID=0                                  # uid/gid every installed file must have (tests change these)
ROOT_GID=0
MAX_BIN=$((64 * 1024 * 1024)); MAX_MF=1024; MAX_SIG=262144
INSTALL=/usr/bin/install; MV=/bin/mv; CODESIGN=/usr/bin/codesign; CURL=/usr/bin/curl
WAIT_TCC_SECS=240
ALLOWED_USAGE_KEYS=" NSCalendarsUsageDescription NSCalendarsFullAccessUsageDescription "   # the only privacy usage strings the adapter may carry
ALLOWED_ENTITLEMENTS=" com.apple.security.personal-information.calendars "                 # the only entitlement it may carry
LABEL=com.eve.pim
GWLABEL=com.llmserving.gateway
DAEMON=/Library/LaunchDaemons/$GWLABEL.plist
SOCK_DIR=$SOCK_BASE/run
SOCK=$SOCK_DIR/pim.sock
STAGE=0        # 0 nothing changed | 2 agent loaded (undo = bootout + disable plist)
DONE=0
VERSION=""
WORK=""

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }
die() { log "FAIL: $*"; exit 1; }

# ----- small helpers -----
# lowercase hex of exactly $2 chars. The digits are listed, never a range: macOS bash 3.2 in a UTF-8 locale lets [a-f] match A-F.
is_hex_len() { case "$1" in ""|*[!0123456789abcdef]*) return 1;; esac; [ "${#1}" -eq "$2" ]; }
require_signing_ci() { [ "$PIM_SIGNING_CI" = yes ] || { echo "setup-pim-adapter: refused: CI does not sign and publish the calendar adapter yet (PIM_SIGNING_CI=no). Nothing was downloaded or changed. See docs/runbooks/unattended-deploy.md." >&2; return 1; }; }
is_ver() { [[ "$1" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]]; }
sha_of() { if [ -x /usr/bin/shasum ]; then /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; else /usr/bin/sha256sum "$1" | /usr/bin/awk '{print $1}'; fi; }
stat_uid() { /usr/bin/stat -c %u "$1" 2>/dev/null || /usr/bin/stat -f %u "$1" 2>/dev/null; }   # GNU first: BSD stat rejects -c, GNU stat misreads -f
stat_gid() { /usr/bin/stat -c %g "$1" 2>/dev/null || /usr/bin/stat -f %g "$1" 2>/dev/null; }
stat_mode() { /usr/bin/stat -c %a "$1" 2>/dev/null || /usr/bin/stat -f %Lp "$1" 2>/dev/null; }
stat_size() { /usr/bin/stat -c %s "$1" 2>/dev/null || /usr/bin/stat -f %z "$1" 2>/dev/null; }
file_ok() { [ -f "$1" ] && [ ! -L "$1" ]; }                                  # regular file, not a symlink
owned_mode_ok() { # $1 path $2 mode: regular file, root-owned, exactly this mode
  file_ok "$1" && [ "$(stat_uid "$1")" = "$ROOT_UID" ] && [ "$(stat_gid "$1")" = "$ROOT_GID" ] && [ "$(stat_mode "$1")" = "$2" ]
}
dir_ok() { # $1 dir: a real directory, root-owned, not writable by group/other
  [ -d "$1" ] && [ ! -L "$1" ] && [ "$(stat_uid "$1")" = "$ROOT_UID" ] || return 1
  local m; m=$(stat_mode "$1") || return 1
  [ $(( 0$m & 0022 )) = 0 ]
}
# $1 dir and EVERY parent up to and including $TRUST_ROOT must be real directories, root-owned and closed to group/other writes
# (otherwise whoever owns /opt or /usr/local could swap the directory that holds the adapter or the verifier). Fails closed if $TRUST_ROOT is not an ancestor.
dir_chain_ok() {
  local d=$1
  case "$d" in /*) ;; *) return 1;; esac
  while :; do
    dir_ok "$d" || return 1
    [ "$d" = "$TRUST_ROOT" ] && return 0
    [ "$d" = / ] && return 1
    d=$(/usr/bin/dirname "$d")
  done
}
bin_dir_ok() { dir_chain_ok "$PIM_DIR"; }   # PIM_DIR is below SOCK_BASE: the chain covers both

# ----- the signed manifest -----
# strict: exactly the 4 lines, in any order, no duplicates, no NUL bytes, nothing else. Sets P_REL / P_TREE / P_SHA256.
parse_manifest() { # $1 file
  local f=$1 line k v n=0 seen=" "
  P_REL=""; P_TREE=""; P_SHA256=""
  file_ok "$f" || return 1
  [ "$(stat_size "$f")" -le "$MAX_MF" ] || return 1
  [ "$(/usr/bin/tr -d '\000' <"$f" | /usr/bin/wc -c | /usr/bin/tr -d ' ')" = "$(stat_size "$f")" ] || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1)); [ "$n" -le 4 ] || return 1
    case "$line" in *=*) ;; *) return 1;; esac
    k=${line%%=*}; v=${line#*=}
    case "$seen" in *" $k "*) return 1;; esac
    seen="$seen$k "
    case "$k" in
      eve-pim-manifest) [ "$v" = 1 ] || return 1;;
      release_sha) is_hex_len "$v" 40 || return 1; P_REL=$v;;
      pim_tree) is_hex_len "$v" 40 || return 1; P_TREE=$v;;
      pim_sha256) is_hex_len "$v" 64 || return 1; P_SHA256=$v;;
      *) return 1;;
    esac
  done <"$f"
  [ "$n" = 4 ] && [ -n "$P_REL" ] && [ -n "$P_TREE" ] && [ -n "$P_SHA256" ] && case "$seen" in *" eve-pim-manifest "*) true;; *) false;; esac
}
# the pinned verifier and trust root: root-owned regular files with the pinned hashes (the same checks as the entrypoint's tool_ok)
tool_ok() {
  local h
  [ -f "$COSIGN" ] && [ ! -L "$COSIGN" ] && [ "$(stat_uid "$COSIGN")" = "$ROOT_UID" ] && [ -x "$COSIGN" ] || { log "cosign is missing or not root-owned"; return 1; }
  [ $(( 0$(stat_mode "$COSIGN") & 0022 )) = 0 ] || { log "cosign is group/world writable"; return 1; }
  dir_chain_ok "$LIBEXEC" || { log "$LIBEXEC (or one of its parents) is not root-owned and closed"; return 1; }
  h=$(sha_of "$COSIGN"); case " $COSIGN_SHA256S " in *" $h "*) ;; *) log "cosign hash is not the pinned one"; return 1;; esac
  [ -f "$TRUSTED_ROOT" ] && [ ! -L "$TRUSTED_ROOT" ] && [ "$(stat_uid "$TRUSTED_ROOT")" = "$ROOT_UID" ] || { log "trusted root is missing or not root-owned"; return 1; }
  [ $(( 0$(stat_mode "$TRUSTED_ROOT") & 0022 )) = 0 ] || { log "trusted root is group/world writable"; return 1; }
  [ "$(sha_of "$TRUSTED_ROOT")" = "$TRUSTED_ROOT_SHA256" ] || { log "trusted root hash is not the pinned one"; return 1; }
}
# No test hook here: tests re-assign the plain variables above (COSIGN, TRUSTED_ROOT, hashes, ROOT_UID, TRUST_ROOT); production has exactly one trust root.
# $1 manifest copy, $2 Sigstore bundle copy, $3 the commit the tag vVERSION must point to (the manifest's release_sha). 0 only if the pinned CI identity signed exactly these bytes for the tag vVERSION of REPO at that commit. Offline (bundle + pinned trusted root), empty environment.
verify_manifest_sig() {
  local why
  tool_ok || return 1
  /bin/mkdir -p "$WORK/home" "$WORK/tmp"
  ( cd "$WORK" && /usr/bin/env -i PATH=/usr/bin:/bin HOME="$WORK/home" TMPDIR="$WORK/tmp" LC_ALL=C "$COSIGN" verify-blob \
      --bundle "$2" --trusted-root "$TRUSTED_ROOT" \
      --certificate-oidc-issuer "$OIDC_ISSUER" --certificate-identity "$SIGNER_IDENTITY" \
      --certificate-github-workflow-repository "$REPO" --certificate-github-workflow-ref "refs/tags/v$VERSION" \
      --certificate-github-workflow-sha "$3" \
      --certificate-github-workflow-trigger "$SIGNER_TRIGGER" "$1" ) >"$WORK/cosign.out" 2>&1 || {
    why=$(/usr/bin/head -c 300 "$WORK/cosign.out" | /usr/bin/tr -d '\000' | /usr/bin/tr '\n' ' ')
    log "cosign said: $why"
    return 1
  }
}

# ----- inputs: downloaded straight into the root-only work dir; everything below judges these copies -----
fetch_asset() { # $1 name $2 dest $3 max bytes
  local sz
  "$CURL" -fsSL --proto '=https' --tlsv1.2 --max-redirs 5 --retry 3 --max-time 600 --max-filesize "$3" -o "$2" "$REL_BASE/v$VERSION/$1" || die "download failed: $1 (is the release v$VERSION published with the adapter assets?)"
  file_ok "$2" || die "not a regular file after download: $1"
  sz=$(stat_size "$2"); [ "$sz" -gt 0 ] && [ "$sz" -le "$3" ] || die "bad size for $1"
}
# Calendar-only adapter: valid (ad-hoc) code signature with the hardened runtime, only the calendars entitlement, only calendar usage strings.
check_signature() { # $1 binary; 0 = ok, otherwise logs why
  local d e k bad="" fl fh nfl
  "$CODESIGN" --verify --strict "$1" >/dev/null 2>&1 || { log "adapter has no valid code signature"; return 1; }
  d=$("$CODESIGN" -dv "$1" 2>&1) || { log "cannot read the adapter's code signature"; return 1; }
  # exact check of the code directory's flags line "CodeDirectory ... flags=0x10002(adhoc,runtime) ..." (never a substring of the whole output:
  # another line, e.g. a path, may contain the word). Exactly one such line, the name list must contain exactly `runtime`, and the numeric flag value must carry the runtime bit 0x10000.
  nfl=$(printf '%s\n' "$d" | /usr/bin/grep -c '^CodeDirectory .*flags=0x') || true
  [ "$nfl" = 1 ] || { log "adapter's code signature has no single CodeDirectory flags line"; return 1; }
  fl=$(printf '%s\n' "$d" | /usr/bin/sed -n 's/^CodeDirectory .*flags=0x[0123456789abcdef]*(\([^)]*\)).*$/\1/p')
  fh=$(printf '%s\n' "$d" | /usr/bin/sed -n 's/^CodeDirectory .*flags=0x\([0123456789abcdef]*\)(.*$/\1/p')
  case ",$fl," in *,runtime,*) ;; *) log "adapter is not signed with the hardened runtime (codesign -o runtime)"; return 1;; esac
  [ -n "$fh" ] && [ $(( 0x$fh & 0x10000 )) != 0 ] || { log "adapter's code-signature flags lack the hardened-runtime bit"; return 1; }
  e=$("$CODESIGN" -d --entitlements :- "$1" 2>/dev/null || true)
  for k in $(printf '%s\n' "$e" | /usr/bin/grep -o '<key>[^<]*</key>' | /usr/bin/sed 's/^<key>//;s/<\/key>$//'); do
    case "$ALLOWED_ENTITLEMENTS" in *" $k "*) ;; *) bad="$bad $k";; esac
  done
  [ -z "$bad" ] || { log "adapter carries entitlements beyond calendars:$bad"; return 1; }
  bad=""
  # shellcheck disable=SC2013  # the keys contain no whitespace
  for k in $(/usr/bin/grep -a -o 'NS[A-Za-z]*UsageDescription' "$1" | /usr/bin/sort -u); do
    case "$ALLOWED_USAGE_KEYS" in *" $k "*) ;; *) bad="$bad $k";; esac
  done
  [ -z "$bad" ] || { log "adapter's Info.plist has usage strings beyond calendars:$bad"; return 1; }
  /usr/bin/grep -a -q 'NSCalendarsUsageDescription' "$1" || { log "adapter has no embedded calendar usage string"; return 1; }
}
# Sets MF_REL / MF_TREE / MF_SHA256. Changes nothing outside $WORK.
verify_inputs() { # $1 manifest $2 Sigstore bundle $3 adapter binary (copies inside $WORK)
  # the manifest is parsed FIRST only to learn which commit the certificate must be bound to; nothing in it is trusted until the signature has been verified against that very commit
  parse_manifest "$1" || die "manifest malformed: need exactly eve-pim-manifest=1, release_sha=<40 lowercase hex>, pim_tree=<40 lowercase hex>, pim_sha256=<64 lowercase hex>"
  verify_manifest_sig "$1" "$2" "$P_REL" || die "manifest signature verification FAILED (not signed by the pinned CI identity for v$VERSION at commit $P_REL)"
  MF_REL=$P_REL; MF_TREE=$P_TREE; MF_SHA256=$P_SHA256
  [ "$MF_TREE" = "$PIM_TREE" ] || die "manifest pim_tree is not the pinned, security-reviewed pim/eve-pim source tree"
  [ "$(sha_of "$3")" = "$MF_SHA256" ] || die "adapter sha256 does not match the manifest pim_sha256: refusing to install"
  check_signature "$3" || die "adapter failed the code-signature checks"
  log "PRE manifest signature OK (CI identity, tag v$VERSION); adapter sha256 matches the manifest (release commit $MF_REL, source tree $MF_TREE)"
}

# ----- install: temp file in the SAME directory (root:wheel, mode set), hash checked, then one atomic rename, hash checked again -----
atomic_put() { # $1 src $2 dst $3 mode [$4 expected sha256, default: that of src]
  local want=${4:-} tmp
  [ ! -d "$2" ] || { log "$2 is a directory: refusing"; return 1; }
  [ -n "$want" ] || want=$(sha_of "$1")
  tmp="$(/usr/bin/dirname "$2")/.${2##*/}.new.$$"
  /bin/rm -f "$tmp"
  # shellcheck disable=SC2086
  "$INSTALL" $OWNER_ARGS -m "$3" "$1" "$tmp" || { /bin/rm -f "$tmp"; return 1; }
  [ "$(sha_of "$tmp")" = "$want" ] || { /bin/rm -f "$tmp"; log "temp file hash differs from the expected sha256: not renamed"; return 1; }
  "$MV" -f "$tmp" "$2" || { /bin/rm -f "$tmp"; return 1; }
  [ "$(sha_of "$2")" = "$want" ] && owned_mode_ok "$2" "$3" || { log "post-install check FAILED for ${2##*/} (hash/owner/mode)"; return 1; }
}
installed_hash() { if file_ok "$PIM_BIN"; then sha_of "$PIM_BIN"; fi; }   # empty when nothing (or a non-regular file) is installed
adapter_current() { [ "$(installed_hash)" = "$MF_SHA256" ] && owned_mode_ok "$PIM_BIN" 755; }   # same sha256 AND correct owner/mode
put_current() { # puts $1 (binary) with its manifest $2 and Sigstore bundle $3 in place; $4/$5/$6 = their expected sha256 (verified earlier); on failure the saved copy of the old state ($WORK/bak.*) goes back
  local had=0 cur hm="" hs="" wb=${4:-} wm=${5:-} ws=${6:-}
  cur=$(installed_hash)
  if [ -n "$cur" ]; then had=1; /bin/cp -p "$PIM_BIN" "$WORK/bak.bin" || return 1; fi
  if file_ok "$PIM_MF" && file_ok "$PIM_MF_SIG"; then /bin/cp -p "$PIM_MF" "$WORK/bak.mf" && /bin/cp -p "$PIM_MF_SIG" "$WORK/bak.sig" || return 1; hm=$(sha_of "$WORK/bak.mf"); hs=$(sha_of "$WORK/bak.sig"); fi
  if atomic_put "$1" "$PIM_BIN" 755 "$wb" && atomic_put "$2" "$PIM_MF" 644 "$wm" && atomic_put "$3" "$PIM_MF_SIG" 644 "$ws"; then return 0; fi
  if [ "$had" = 1 ]; then
    atomic_put "$WORK/bak.bin" "$PIM_BIN" 755 "$cur" && log "previous adapter restored" || log "RESTORE FAILED: inspect $PIM_DIR by hand"
  else /bin/rm -f "$PIM_BIN"; log "unverified adapter removed (nothing was installed before)"; fi
  if [ -n "$hm" ]; then atomic_put "$WORK/bak.mf" "$PIM_MF" 644 "$hm" && atomic_put "$WORK/bak.sig" "$PIM_MF_SIG" 644 "$hs" || log "RESTORE of the signed record FAILED: inspect $PIM_DIR by hand"
  else /bin/rm -f "$PIM_MF" "$PIM_MF_SIG"; fi
  return 1
}
install_adapter() { # needs verify_inputs first. Sets INSTALL_RESULT=same|installed
  INSTALL_RESULT=""
  if adapter_current; then
    INSTALL_RESULT=same
    log "§3 adapter with the same sha256 is already installed: not replaced (same code signature, no new TCC prompt)"
    # a missing/stale record: refresh it (never the binary)
    if ! { file_ok "$PIM_MF" && file_ok "$PIM_MF_SIG" && /usr/bin/cmp -s "$PIM_MF" "$WORK/in.MANIFEST" && /usr/bin/cmp -s "$PIM_MF_SIG" "$WORK/in.sigstore.json"; }; then
      atomic_put "$WORK/in.MANIFEST" "$PIM_MF" 644 && atomic_put "$WORK/in.sigstore.json" "$PIM_MF_SIG" 644 || die "cannot refresh the signed record"
    fi
    return 0
  fi
  bin_dir_ok || die "$SOCK_BASE and $PIM_DIR must be real, root-owned directories closed to group/world writes"
  # (same bytes but wrong owner/mode: replaced in place; same hash, so the code signature and the TCC grant are unchanged)
  put_current "$WORK/in.eve-pim" "$WORK/in.MANIFEST" "$WORK/in.sigstore.json" "$MF_SHA256" "$(sha_of "$WORK/in.MANIFEST")" "$(sha_of "$WORK/in.sigstore.json")" || die "adapter install failed (see above); the previous state was restored"
  [ "$(sha_of "$PIM_BIN")" = "$MF_SHA256" ] || die "installed adapter hash changed after install"
  check_signature "$PIM_BIN" || die "installed adapter failed the code-signature checks"
  INSTALL_RESULT=installed
  log "§3 adapter installed at $PIM_BIN (root:wheel 0755), sha256 re-verified after install"
}

# the work dir holds only copies of public files: remove it whole
cleanup_work() {
  [ -n "$WORK" ] && [ -d "$WORK" ] && [ ! -L "$WORK" ] || return 0
  /bin/rm -rf "$WORK"
}

# ----- embedded files -----
write_embedded() { # $1 dir: eve-pim.sb and the LaunchAgent template
  cat >"$1/eve-pim.sb" <<'SB'
;; eve-pim sandbox profile (best effort; sandbox-exec is deprecated but works).
;; Deny all IP networking. The unix socket in --socket-dir is the only
;; channel. EventKit talks to calendaragent/remindd over XPC (mach-lookup),
;; which is not network access.
(version 1)
(allow default)
(deny network-outbound (remote ip))
(deny network-inbound (local ip))
(deny network-bind (local ip))
SB
  cat >"$1/com.eve.pim.plist.template" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.eve.pim</string>
<key>ProgramArguments</key><array>
  <string>/usr/bin/sandbox-exec</string><string>-f</string><string>__PIM_DIR__/eve-pim.sb</string>
  <string>__PIM_DIR__/eve-pim</string>
  <string>--socket-dir</string><string>__SOCKET_DIR__</string>
  <string>--peer-user</string><string>_evegw</string>
  <string>--group</string><string>_evepim</string>
</array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer>
<key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>__HOME__/Library/Logs/eve-pim.log</string>
<key>StandardErrorPath</key><string>__HOME__/Library/Logs/eve-pim.log</string>
</dict></plist>
PLIST
}

# test hook: ONLY a non-root process may source this file to get the functions and constants (a root run, i.e. the real install, never reaches the return)
# shellcheck disable=SC2317  # "exit 0" is the fallback when the file is run, not sourced
if [ "$(id -u)" != 0 ] && [ "${EVE_PIM_LIB_ONLY:-}" = 1 ]; then return 0 2>/dev/null || exit 0; fi

exec </dev/null
set -Eeuo pipefail
umask 022
require_signing_ci || exit 3   # first thing: before the argument, root and download checks
[ "$#" = 2 ] && [ "$1" = --version ] && is_ver "$2" || { echo "usage: setup-pim-adapter --version X.Y.Z" >&2; exit 2; }
VERSION=$2
[ "$(id -u)" = 0 ] || { echo "setup-pim-adapter: run with sudo (inside the one-shot's sudo session)" >&2; exit 1; }
[ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] || { echo "setup-pim-adapter: SUDO_USER missing" >&2; exit 1; }
{ [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; } || { echo "setup-pim-adapter: macOS on Apple silicon only" >&2; exit 1; }
is_hex_len "$PIM_TREE" 40 || { echo "setup-pim-adapter: PIM_TREE must be the full 40-character lowercase hex tree hash" >&2; exit 1; }

# =============================== main: ONE private work dir (mktemp -d, 0700), root + user context ===============================
WORK=$(umask 077; mktemp -d "${TMPDIR:-/tmp}/eve-pim.XXXXXX") || { echo "mktemp -d failed" >&2; exit 1; }
chmod 0700 "$WORK"
TS_STAMP=$(date +%Y%m%d-%H%M%S)
AGENT_STOPPED=0
cd /
U_UID=$(id -u "$SUDO_USER")
U_HOME=$(dscl . -read "/Users/$SUDO_USER" NFSHomeDirectory | awk '{print $2}')
UPLIST="$U_HOME/Library/LaunchAgents/$LABEL.plist"
as_user() { if [ "$(id -u)" = "$U_UID" ]; then "$@"; else sudo -u "$SUDO_USER" "$@"; fi; }
dscl_val() { # $1 record $2 key -> value(s) or nothing; "No such key" is exit 0, so read the output
  local o; o=$(dscl . -read "$1" "$2" 2>&1 || true)
  case "$o" in *"No such key"*|*"eDSRecordNotFound"*|*"DS Error"*|"") return 0;; esac
  printf '%s' "$o" | sed -e "1s/^$2:[[:space:]]*//" | tr -s ' \n' ' ' | sed 's/^ *//;s/ *$//'; }
agent_state() { launchctl print "gui/$U_UID/$LABEL" 2>&1 || true; }
agent_loaded() { local o; o=$(agent_state); case "$o" in *"state = "*) return 0;; esac; return 1; }
health_gw() { [ "$(curl -s -m 3 http://127.0.0.1:8000/health 2>/dev/null || true)" = '{"status":"ok"}' ]; }

undo() {
  set +e; trap - ERR
  log "UNDO start (stage=$STAGE): helper only; gateway and DB untouched"
  if [ "$STAGE" -ge 2 ]; then
    launchctl bootout "gui/$U_UID/$LABEL" 2>/dev/null
    [ -f "$UPLIST" ] && mv -f "$UPLIST" "$UPLIST.disabled-$TS_STAMP"
  fi
  if health_gw; then log "UNDO gateway health OK"; else log "UNDO gateway health NOT OK (only §6 touches it, and restores the previous plist on failure)"; fi
  log "UNDO done. Kept: _evepim group, $SOCK_BASE, the installed adapter and its signed record (nothing deleted)."
}
on_err() { log "error at line $1 (stage=$STAGE)"; }
on_exit() { local rc=$?
  if [ "$rc" != 0 ] && [ "$DONE" != 1 ] && [ "$STAGE" -ge 2 ]; then undo; fi
  # the agent was stopped for the swap and the run failed: put the (restored or old) adapter back into service
  if [ "$rc" != 0 ] && [ "$AGENT_STOPPED" = 1 ] && [ -f "$UPLIST" ] && ! agent_loaded; then
    launchctl bootstrap "gui/$U_UID" "$UPLIST" 2>/dev/null && log "agent restarted after the failed run" || log "WARN: could not restart the agent: launchctl bootstrap gui/$U_UID $UPLIST"
  fi
  [ "$rc" = 0 ] || log "EXIT rc=$rc"
  cleanup_work; }
trap 'on_err $LINENO' ERR
trap on_exit EXIT

log "setup-pim-adapter start (release v$VERSION, pim source tree $PIM_TREE)"

# =============================== PRECHECKS + DOWNLOAD + VERIFY (no changes outside the work dir) ===============================
[ -f /Library/LaunchDaemons/$GWLABEL.plist ] || die "gateway LaunchDaemon missing: the gateway must be installed first"
health_gw || die "gateway not healthy"
[ "$(dscl_val /Users/_evegw UniqueID)" = 451 ] || die "_evegw missing / uid != 451"
[ "$(id -u "$SUDO_USER")" -ge 500 ] || die "login user uid unexpected"
case "$(launchctl print "gui/$U_UID" 2>&1 | head -1 || true)" in *"Could not find"*|"") die "no GUI session for $SUDO_USER: log in on the Mac screen first (the calendar prompt needs it)";; esac
[ -e "$STATE/PAUSE" ] || [ -L "$STATE/PAUSE" ] || log "NOTE: deploys are not paused right now (the one-shot's installer step creates PAUSE; this step does not change it)"

fetch_asset "eve-pim-$VERSION.MANIFEST" "$WORK/in.MANIFEST" "$MAX_MF"
fetch_asset "eve-pim-$VERSION.sigstore.json" "$WORK/in.sigstore.json" "$MAX_SIG"
fetch_asset "eve-pim-$VERSION" "$WORK/in.eve-pim" "$MAX_BIN"
verify_inputs "$WORK/in.MANIFEST" "$WORK/in.sigstore.json" "$WORK/in.eve-pim"

# (the sections below are the reviewed #154 installer text, kept line for line so the review is a diff; no dry run here: the one-shot has its own --dry-run)
DRY_RUN=0
run() { "$@"; }
run_user() { as_user "$@"; }

# =============================== §1 _evepim group, _evegw member (root) ===============================
EXISTING_GID=$(dscl_val /Groups/_evepim PrimaryGroupID)
if [ -z "$EXISTING_GID" ]; then
  USED=$(dscl . -list /Groups PrimaryGroupID 2>/dev/null | awk '{print $2}')
  GID_NEW=""; g=$PIM_GID_WANT
  while [ "$g" -le 499 ]; do printf '%s\n' "$USED" | grep -qx "$g" || { GID_NEW=$g; break; }; g=$((g+1)); done
  [ -n "$GID_NEW" ] || die "no free gid in $PIM_GID_WANT..499"
  run dscl . -create /Groups/_evepim
  run dscl . -create /Groups/_evepim PrimaryGroupID "$GID_NEW"
  run dscl . -create /Groups/_evepim RealName "Eve pim socket"
  run dscl . -create /Groups/_evepim Password '*'
fi
[ "$DRY_RUN" = 1 ] || { [ -n "$(dscl_val /Groups/_evepim PrimaryGroupID)" ] || die "_evepim group not created"; }
GM=$(dscl_val /Groups/_evepim GroupMembership)
case " $GM " in *" _evegw "*) ;; *) run dseditgroup -o edit -a _evegw -t user _evepim ;; esac
if [ "$DRY_RUN" != 1 ]; then
  GM=$(dscl_val /Groups/_evepim GroupMembership)
  [ "$GM" = _evegw ] || die "_evepim members are '$GM' (must be exactly _evegw)"
  case " $(sudo -u _evegw id -Gn 2>/dev/null || true) " in *" _evepim "*) ;; *) log "§1 WARN: 'id -Gn _evegw' does not list _evepim yet (directory cache); retry in a minute if the smoke test cannot connect";; esac
fi
log "§1 OK (_evepim gid=$(dscl_val /Groups/_evepim PrimaryGroupID), members: ${GM:-_evegw}; the running gateway picks the group up at its next restart)"

# =============================== §2 socket directory + the fixed root-owned adapter directory ===============================
run install -d -o root -g wheel -m 0755 "$SOCK_BASE" "$PIM_DIR"
run install -d -o "$SUDO_USER" -g _evepim -m 0750 "$SOCK_DIR"
if [ "$DRY_RUN" != 1 ]; then
  [ "$(stat -f '%Su:%Sg:%Lp' "$SOCK_DIR")" = "$SUDO_USER:_evepim:750" ] || die "socket dir must be $SUDO_USER:_evepim 0750"
  [ "$(realpath "$SOCK_DIR")" = "$SOCK_DIR" ] || die "socket dir path has a symlink"
  bin_dir_ok || die "$SOCK_BASE and $PIM_DIR must be real, root-owned directories closed to group/world writes"
fi
log "§2 OK ($SOCK_DIR = <login>:_evepim 0750; $SOCK_BASE and $PIM_DIR root:wheel 0755)"

# =============================== §3 install the verified adapter + sandbox profile, render the LaunchAgent ===============================
write_embedded "$WORK"
RENDER=$WORK/com.eve.pim.plist
if [ "$DRY_RUN" != 1 ]; then
  python3 - "$WORK/com.eve.pim.plist.template" "$RENDER" "$U_HOME" "$SOCK_DIR" "$PIM_DIR" <<'PY'
import plistlib, sys
src, dst, home, sock, pimdir = sys.argv[1:6]
t = open(src).read().replace("__HOME__", home).replace("__SOCKET_DIR__", sock).replace("__PIM_DIR__", pimdir)
pl = plistlib.loads(t.encode())
a = pl["ProgramArguments"]
assert not any("writes" in x for x in a), "writes flag in plist"
pl["ProgramArguments"] = a
with open(dst, "wb") as f: plistlib.dump(pl, f)
PY
  plutil -lint "$RENDER" >/dev/null || die "rendered plist lint"
  PA=$(plutil -extract ProgramArguments json -o - "$RENDER" 2>/dev/null || true)
  case "$PA" in *writes*|"") die "rendered plist ProgramArguments empty or has a writes flag";; esac
fi
BIN_SAME=0; SB_SAME=0; PL_SAME=0
if [ "$DRY_RUN" != 1 ]; then
  adapter_current && BIN_SAME=1
  owned_mode_ok "$PIM_SB" 644 && /usr/bin/cmp -s "$WORK/eve-pim.sb" "$PIM_SB" && SB_SAME=1
  [ -f "$UPLIST" ] && /usr/bin/cmp -s "$RENDER" "$UPLIST" && PL_SAME=1
  # the helper keeps running from the old inode after a rename: stop it first whenever anything it uses changes
  if [ "$BIN_SAME$SB_SAME$PL_SAME" != 111 ] && agent_loaded; then
    launchctl bootout "gui/$U_UID/$LABEL" 2>/dev/null || true; AGENT_STOPPED=1; sleep 1
  fi
fi
install_adapter
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: install $PIM_SB (root:wheel 0644) and render $UPLIST"
else
  [ "$SB_SAME" = 1 ] || atomic_put "$WORK/eve-pim.sb" "$PIM_SB" 644 || die "cannot install the sandbox profile"
  run_user mkdir -p "$U_HOME/Library/LaunchAgents" "$U_HOME/Library/Logs"
  if [ "$PL_SAME" != 1 ]; then
    install -o "$SUDO_USER" -g staff -m 0644 "$RENDER" "$UPLIST.new" && mv -f "$UPLIST.new" "$UPLIST" || die "cannot write the LaunchAgent plist"
  fi
  # a stale pim.sock is never deleted by the helper; remove it only if it is a socket and no helper is running
  if [ "$INSTALL_RESULT" = installed ] && [ -e "$SOCK" ] && ! pgrep -x eve-pim >/dev/null 2>&1; then
    if [ -S "$SOCK" ]; then rm -f "$SOCK"; log "§3 removed stale socket (no helper running)"; else die "$SOCK exists and is not a socket: inspect by hand"; fi
  fi
  [ ! -e "$U_HOME/eve-pim/eve-pim" ] || log "§3 NOTE: $U_HOME/eve-pim/eve-pim (user-writable, from the old local build) is no longer used; remove it by hand"
  sandbox-exec -f "$PIM_SB" /usr/bin/true || die "sandbox profile does not load"
  if as_user /usr/bin/nc -z -w 2 127.0.0.1 8000 >/dev/null 2>&1; then
    if as_user sandbox-exec -f "$PIM_SB" /usr/bin/nc -z -w 2 127.0.0.1 8000 >/dev/null 2>&1; then die "sandbox profile does NOT block IP connect"; fi
    log "§3 sandbox profile OK (loads; IP connect works outside, is blocked inside)"
  else log "§3 sandbox profile loads (IP-block test skipped: baseline connect failed)"; fi
fi
log "§3 OK (adapter + profile in $PIM_DIR root:wheel, LaunchAgent plist rendered, writes flag absent)"

# =============================== §4 load the LaunchAgent + TCC instructions ===============================
if ! agent_loaded; then
  STAGE=2
  run launchctl bootstrap "gui/$U_UID" "$UPLIST"
fi
AGENT_STOPPED=0
cat <<'TXT'

================ 건영님이 직접 할 일: 캘린더 권한 (TCC) ================
 캘린더 어댑터(eve-pim)가 권한을 요청합니다. 지금 Mac 화면(또는 화면 공유)에 팝업이 한 번 뜹니다. 이 스크립트는 팝업을 눌러 줄 수 없습니다.
  1) "eve-pim이 캘린더에 접근하려고 합니다" -> [전체 접근 허용 / Allow Full Access]
 (이 어댑터는 캘린더 전용입니다. 미리 알림·연락처·전체 디스크 접근은 요청하지 않습니다.)
 팝업이 안 보이면 (시스템 설정 > 개인정보 보호 및 보안):
  - 캘린더 > 목록에서 eve-pim 켜기 ("전체 접근" 표시 확인)
  - 목록에 없으면: 헬퍼 재시작 후 다시 팝업 확인
        launchctl kickstart -k gui/$(id -u)/com.eve.pim
  - 그래도 없으면 권한 초기화 후 재시도 (eve-pim 항목만 초기화됨)
        tccutil reset Calendar com.eve.pim
 ※ 예상 밖 팝업(캘린더가 아닌 항목, 다른 앱/경로 이름, 갑자기 뜨는 추가 권한 요청)은 [허용]하지 마세요:
     [허용 안 함]을 누르고 설치를 중단(PAUSE)한 뒤, 경로와 sha256을 확인하세요.
        ls -l /opt/eve-pim/bin/eve-pim ; shasum -a 256 /opt/eve-pim/bin/eve-pim
     sha256이 서명된 manifest의 pim_sha256과 다르거나 경로가 /opt/eve-pim/bin/eve-pim이 아니면 헬퍼를 멈추세요
        launchctl bootout gui/$(id -u)/com.eve.pim
     그리고 보안 담당에게 알리세요. (확인 전에는 다시 실행하지 마세요.)
 권한은 eve-pim 바이너리의 서명(cdhash)에 묶입니다. 지금 설치된 것과 sha256이 같은 바이너리는 다시 설치해도 교체되지 않아 팝업이 다시 뜨지 않습니다.
 다른 빌드(sha256이 다름)로 바뀌면 팝업이 한 번 다시 뜹니다.
 읽기 전용입니다: 쓰기(--writes-enabled)는 켜지지 않았고, 게이트웨이 플래그도 모두 꺼져 있습니다.
==================================================================================
TXT

# =============================== §5 wait for the socket (helper binds only after the prompts are answered) ===============================
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: wait up to ${WAIT_TCC_SECS}s for $SOCK; smoke tests"
else
  waited=0; while [ ! -S "$SOCK" ] && [ "$waited" -lt "$WAIT_TCC_SECS" ]; do sleep 3; waited=$((waited+3)); done
  if [ ! -S "$SOCK" ]; then
    log "PENDING: no socket yet after ${WAIT_TCC_SECS}s. The helper is loaded and waiting for the TCC prompts (up to 5 min per prompt)."
    log "         Answer the prompts, then re-run the same one-shot command (idempotent: the adapter is not replaced again, it only runs the smoke test and §6)"
    DONE=1; exit 0
  fi
  # ---- smoke test ----
  [ "$(stat -f '%Su:%Sg:%Lp' "$SOCK_DIR")" = "$SUDO_USER:_evepim:750" ] || die "smoke: socket dir perms changed"
  [ "$(stat -f '%Sg:%Lp' "$SOCK")" = _evepim:660 ] || die "smoke: socket must be group _evepim, mode 660 (is $(stat -f '%Sg:%Lp' "$SOCK"))"
  REQ=$(python3 - <<'PY'
import datetime, json
n = datetime.datetime.now().astimezone().replace(microsecond=0)
print(json.dumps({"v": 1, "id": "smoke", "method": "list_events",
                  "params": {"from": n.isoformat(), "to": (n + datetime.timedelta(days=1)).isoformat()}}))
PY
)
  # (a) as _evegw: must answer (ok:true, or permission_denied while a prompt is still unanswered)
  RESP=$(printf '%s\n' "$REQ" | sudo -u _evegw /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)
  SMOKE=$(printf '%s' "$RESP" | python3 -c 'import json,sys
try:
    d = json.loads(sys.stdin.read())
except Exception:
    print("noresp"); sys.exit()
if d.get("ok"): print("ok count=%d" % len((d.get("result") or {}).get("events", [])))
else: print("err " + str(d.get("error")))' 2>/dev/null || echo noresp)
  case "$SMOKE" in
    ok*) log "SMOKE (a) _evegw list_events: $SMOKE (Calendar permission works)";;
    "err permission_denied") log "SMOKE (a) _evegw reached the helper; Calendar permission not granted yet -> answer/enable the prompt, then re-run";;
    *) die "smoke (a): _evegw got '$SMOKE' (group membership cache? try again in a minute, or check ~/Library/Logs/eve-pim.log)";;
  esac
  # (b) non-_evegw peers (the login user, root) must be disconnected without any response
  [ -z "$(printf '%s\n' "$REQ" | as_user /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)" ] || die "smoke (b): login user got a response"
  [ -z "$(printf '%s\n' "$REQ" | /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)" ] || die "smoke (b): root got a response"
  # (c) writes are off: add_reminder must answer writes_disabled, store untouched
  WR=$(printf '%s\n' '{"v":1,"id":"smoke-w","method":"add_reminder","params":{"title":"x"}}' | sudo -u _evegw /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)
  printf '%s' "$WR" | grep -q '"writes_disabled"' || die "smoke (c): add_reminder did not answer writes_disabled"
  # (d) a stale pim.sock makes a second helper refuse to start; the file must survive (own temp dir)
  T="$SOCK_BASE/selftest.$$"; install -d -o "$SUDO_USER" -g _evepim -m 0750 "$T"; as_user touch "$T/pim.sock"
  as_user "$PIM_BIN" --socket-dir "$T" --peer-user _evegw --group _evepim >/dev/null 2>&1 &
  SP=$!; sleep 4
  if kill -0 "$SP" 2>/dev/null; then kill "$SP" 2>/dev/null || true; log "SMOKE (d) SKIPPED: second helper still waiting (TCC?)"
  else wait "$SP" 2>/dev/null && die "smoke (d): helper started on a stale socket path"; [ -e "$T/pim.sock" ] || die "smoke (d): stale file was deleted"; log "SMOKE (d) stale socket: refused to start, file kept"; fi
  rm -f "$T/pim.sock"; rmdir "$T"
  log "SMOKE OK (a-d). Socket: $SOCK"
fi

# =============================== §6 gateway env block (DEFAULT; flags false, write flags never set) ===============================
# Keys: GW_PIM_SOCKET_PATH, GW_PIM_OWNER_USER_ID, GW_PIM_ENABLED=false, GW_BRIEFING_ENABLED=false (the two flags are only
# added when absent: a value someone set later is kept). The 5.0.0 gateway ignores these keys until a calendar gateway build is deployed.
# GW_PIM_WRITES_ENABLED is only added as false when absent, never changed or set to true. The owner id is read at run time and never printed.
CFG=/opt/eve-gw/etc/config.yaml
OWNER_ID=""
if [ -z "$OWNER_ID" ] && [ -r "$CFG" ]; then
  # exactly one allowed login -> its users.id (SQL goes via stdin so the login never shows up in a process list or log)
  OWNER_ID=$(python3 - "$CFG" <<'PY' | as_user psql -d llmchat -At 2>/dev/null | head -1 || true
import json, re, sys
for l in open(sys.argv[1]).read().splitlines():
    m = re.match(r'^allowed_users:\s*(\[.*?\])', l)
    if m:
        try: u = json.loads(m.group(1))
        except Exception: sys.exit()
        if len(u) == 1 and isinstance(u[0], str):
            print("select id from users where login = '" + u[0].replace("'", "''") + "';")
        break
PY
)
fi
case "$OWNER_ID" in ""|*[!0-9]*|0) OWNER_ID="";; esac
if [ -z "$OWNER_ID" ]; then
  log "§6 PENDING: the calendar owner's user id could not be read yet (not signed in once, or more than one allowed user). Sign in on the device, then re-run the same one-shot command (idempotent)."
  DONE=1; exit 0
fi
log "§6 owner user id resolved (value not logged)"
RB=/var/root/eve-rollback-pim
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: set GW_PIM_SOCKET_PATH, GW_PIM_OWNER_USER_ID (+ GW_PIM_ENABLED/GW_PIM_WRITES_ENABLED/GW_BRIEFING_ENABLED=false if absent) in $DAEMON, bootout+bootstrap"
else
  CHG=$(EVE_OWNER="$OWNER_ID" python3 - "$DAEMON" "$SOCK" <<'PY'
import os, plistlib, sys
p, sock = sys.argv[1:3]
e = plistlib.load(open(p, "rb")).get("EnvironmentVariables", {})
want = {"GW_PIM_SOCKET_PATH": sock, "GW_PIM_OWNER_USER_ID": os.environ["EVE_OWNER"]}
print("diff" if any(e.get(k) != v for k, v in want.items()) or "GW_PIM_ENABLED" not in e or "GW_PIM_WRITES_ENABLED" not in e or "GW_BRIEFING_ENABLED" not in e else "same")
PY
)
  if [ "$CHG" = same ]; then log "§6 gateway env keys already in place: nothing changed, gateway not restarted"
  else
    install -d -o root -g wheel -m 0700 "$RB"
    install -o root -g wheel -m 0600 "$DAEMON" "$RB/$GWLABEL.plist.$TS_STAMP"
    EVE_OWNER="$OWNER_ID" python3 - "$DAEMON" "$SOCK" <<'PY'
import os, plistlib, sys
p, sock = sys.argv[1:3]
pl = plistlib.load(open(p, "rb"))
e = pl.setdefault("EnvironmentVariables", {})
e["GW_PIM_SOCKET_PATH"] = sock
e["GW_PIM_OWNER_USER_ID"] = os.environ["EVE_OWNER"]
e.setdefault("GW_PIM_ENABLED", "false")
e.setdefault("GW_PIM_WRITES_ENABLED", "false")
e.setdefault("GW_BRIEFING_ENABLED", "false")
tmp = p + ".new"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
with os.fdopen(fd, "wb") as f: plistlib.dump(pl, f)
os.replace(tmp, p)
PY
    plutil -lint "$DAEMON" >/dev/null || { install -o root -g wheel -m 0644 "$RB/$GWLABEL.plist.$TS_STAMP" "$DAEMON"; die "plist lint after edit (restored)"; }
    launchctl bootout "system/$GWLABEL" 2>/dev/null || true
    gw_boot() { local _; for _ in $(seq 1 15); do launchctl print "system/$GWLABEL" >/dev/null 2>&1 && return 0; launchctl bootstrap system "$DAEMON" 2>/dev/null && return 0; sleep 1; done; return 1; }
    gw_boot || log "§6 bootstrap kept failing; restoring previous plist"
    for _ in $(seq 1 30); do health_gw && break; sleep 1; done
    if ! health_gw; then
      log "gateway unhealthy after env change: restoring previous plist"
      launchctl bootout "system/$GWLABEL" 2>/dev/null || true
      install -o root -g wheel -m 0644 "$RB/$GWLABEL.plist.$TS_STAMP" "$DAEMON"; gw_boot || true
      die "gateway did not come back with the calendar env keys"
    fi
    log "§6 OK gateway restarted with the calendar env keys (rollback copy: $RB/$GWLABEL.plist.$TS_STAMP)"
  fi
  # report only the flag values (not secrets), and warn if a write flag exists
  for k in GW_PIM_ENABLED GW_BRIEFING_ENABLED GW_PIM_WRITES_ENABLED; do
    v=$(/usr/libexec/PlistBuddy -c "Print :EnvironmentVariables:$k" "$DAEMON" 2>/dev/null || true); log "§6 $k=${v:-unset}"
  done
fi
DONE=1
log "SETUP-PIM-ADAPTER DONE (v$VERSION)"
