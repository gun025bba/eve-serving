#!/bin/bash
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
# 캘린더 연동·아침 브리핑(M1) install: 캘린더 어댑터 (the eve-pim helper, #68) + socket plumbing for the gateway client (#69) and the briefing (#71).
#
# The adapter is NOT built on the Mac any more (no git clone, no swift, no toolchain as root). CI builds and signs it; this script only
# VERIFIES and INSTALLS it:
#   sudo bash install-calendar-adapter.sh --manifest <eve-pim.MANIFEST> --sig <eve-pim.MANIFEST.sig> --adapter <eve-pim>
#   sudo bash install-calendar-adapter.sh --rollback          back to the ONE previous adapter this script kept
#   sudo DRY_RUN=1 bash install-calendar-adapter.sh ...       read-only checks, changes are echoed
# All three files are inputs (absolute paths; staged by the login user, e.g. in /usr/local/libexec/eve/staging).
# Run right AFTER deploy-5.0.0.sh (or use deploy-5.0.0-with-calendar.sh).
#
# Trust: the MANIFEST (3 lines: eve-pim-manifest=1, pim_sha=<40 lowercase hex>, pim_sha256=<64 lowercase hex>) must carry a valid signature
# of the pinned release key. That is the verifier of the passwordless-deploy entrypoint (/usr/local/libexec/eve/deploy, verify_sig:
# ssh-keygen -Y verify against its root-owned allowed_signers): it is sourced in a subshell, no second trust root and no new sudoers verb.
# The adapter is installed only if pim_sha == PIM_SHA below (the security-reviewed source commit) AND sha256(adapter) == pim_sha256.
# Install target: a fixed root-owned directory, atomic rename, hash re-verified afterwards. A binary that is already installed with the same
# sha256 is never replaced (same code signature = no new TCC prompt).
#
# Installs the adapter and (default, PM-approved) the gateway env keys. Flags stay OFF (GW_PIM_ENABLED / GW_BRIEFING_ENABLED = false);
# writes stay OFF: the LaunchAgent never passes --writes-enabled and GW_PIM_WRITES_ENABLED is only ever set to false by this script (never true).
# Re-runnable: every step checks first. Never prints calendar/reminder content, secrets, hostnames, logins or the owner user id.
# Written for macOS /bin/bash 3.2. When SOURCED (tests) it only defines functions and constants.

# ===================== pinned inputs (edit only here) =====================
PIM_SHA=6ce19939245e021f7dff5830d3a4273a85c9c949   # source commit of the adapter that CI builds (last change to pim/eve-pim and the launchd template on release/5.1.0). Update it only to a commit security approved. Must be the full 40-hex SHA.
# shellcheck disable=SC2034  # KEEP_REF / EVE_REPO are documentation + tests (CI and audits find the pinned source through them)
KEEP_REF=keep/pim-adapter-6ce1993  # branch kept on EVE_REPO at PIM_SHA so the pinned source stays reachable for CI rebuilds and audits after release/5.1.0 is squashed into main and deleted. Never delete it; move it (fast-forward only) together with PIM_SHA.
# shellcheck disable=SC2034
EVE_REPO=https://github.com/gun025bba/eve-serving
PIM_GID_WANT=452                 # first-choice gid for _evepim; next free gid in 452..499 is used if taken
SOCK_BASE=/opt/eve-pim           # root:wheel 0755; the socket dir below it is <login>:_evepim 0750
NO_SANDBOX="${NO_SANDBOX:-0}"    # 1 = LaunchAgent without sandbox-exec (use if EventKit/TCC fails under the profile)
WAIT_TCC_SECS="${WAIT_TCC_SECS:-180}"
# Gateway env block (§6) is the default (PM approved). OWNER_USER_ID may be passed to override the run-time lookup; it is never printed.
# ==========================================================================

# ----- fixed locations and tools: plain assignments, the environment can never override them (tests re-assign them after sourcing) -----
DEPLOY_ENTRY=/usr/local/libexec/eve/deploy   # root:wheel entrypoint of the passwordless deploy: provides verify_sig (its pinned key)
PIM_DIR=$SOCK_BASE/bin                       # root:wheel 0755; nothing in it is writable by the login user or _evegw
PIM_BIN=$PIM_DIR/eve-pim
PIM_SB=$PIM_DIR/eve-pim.sb
PIM_MF=$PIM_BIN.manifest                     # the verified, signed manifest + signature of the installed binary (kept for the rollback check)
PIM_MF_SIG=$PIM_MF.sig
PIM_PREV=$PIM_BIN.prev                       # the one previous adapter, with its own signed manifest
PIM_PREV_MF=$PIM_PREV.manifest
PIM_PREV_SIG=$PIM_PREV_MF.sig
OWNER_ARGS="-o root -g wheel"                # tests clear this
ROOT_UID=0                                   # uid/gid every installed file must have (tests change these)
ROOT_GID=0
STAGE_UID=""                                 # uid (besides root) that may own the input files: the login user, set at run time
MAX_BIN=$((64 * 1024 * 1024)); MAX_MF=1024; MAX_SIG=65536
INSTALL=/usr/bin/install; MV=/bin/mv; CODESIGN=/usr/bin/codesign
ALLOWED_USAGE_KEYS=" NSCalendarsUsageDescription NSCalendarsFullAccessUsageDescription "   # the only privacy usage strings the adapter may carry
ALLOWED_ENTITLEMENTS=" com.apple.security.personal-information.calendars "                 # the only entitlement it may carry

DRY_RUN="${DRY_RUN:-0}"
LABEL=com.eve.pim
GWLABEL=com.llmserving.gateway
DAEMON=/Library/LaunchDaemons/$GWLABEL.plist
SOCK_DIR=$SOCK_BASE/run
SOCK=$SOCK_DIR/pim.sock
STAGE=0        # 0 nothing changed | 2 agent loaded (undo = bootout + disable plist)
DONE=0
WORK=""
LOG=""

log()  { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "${LOG:-/dev/null}"; }
die()  { log "FAIL: $*"; exit 1; }
run()  { if [ "$DRY_RUN" = 1 ]; then echo "  DRY: $*"; else "$@"; fi; }

# ----- small helpers -----
# lowercase hex of exactly $2 chars. The digits are listed, never a range: macOS bash 3.2 in a UTF-8 locale lets [a-f] match A-F (found by the macOS CI job).
is_hex_len() { case "$1" in ""|*[!0123456789abcdef]*) return 1;; esac; [ "${#1}" -eq "$2" ]; }
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
bin_dir_ok() { dir_ok "$SOCK_BASE" && dir_ok "$PIM_DIR"; }

# ----- the signed manifest -----
# strict: exactly the 3 lines, in any order, no duplicates, no NUL bytes, nothing else. Sets P_SHA / P_SHA256.
parse_manifest() { # $1 file
  local f=$1 line k v n=0 seen=" "
  P_SHA=""; P_SHA256=""
  file_ok "$f" || return 1
  [ "$(stat_size "$f")" -le "$MAX_MF" ] || return 1
  [ "$(/usr/bin/tr -d '\000' <"$f" | /usr/bin/wc -c | /usr/bin/tr -d ' ')" = "$(stat_size "$f")" ] || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1)); [ "$n" -le 3 ] || return 1
    case "$line" in *=*) ;; *) return 1;; esac
    k=${line%%=*}; v=${line#*=}
    case "$seen" in *" $k "*) return 1;; esac
    seen="$seen$k "
    case "$k" in
      eve-pim-manifest) [ "$v" = 1 ] || return 1;;
      pim_sha) is_hex_len "$v" 40 || return 1; P_SHA=$v;;
      pim_sha256) is_hex_len "$v" 64 || return 1; P_SHA256=$v;;
      *) return 1;;
    esac
  done <"$f"
  [ "$n" = 3 ] && [ -n "$P_SHA" ] && [ -n "$P_SHA256" ] && case "$seen" in *" eve-pim-manifest "*) true;; *) false;; esac
}
# Test hook: runs inside the verifier's subshell right after the entrypoint is sourced (tests point the trust root at a temp dir).
eve_deploy_overrides() { :; }
verify_manifest_sig() { # $1 manifest $2 signature: 0 only if the pinned release key signed exactly these bytes
  [ -f "$DEPLOY_ENTRY" ] && [ ! -L "$DEPLOY_ENTRY" ] && [ "$(stat_uid "$DEPLOY_ENTRY")" = "$ROOT_UID" ] || { log "verifier $DEPLOY_ENTRY is missing or not root-owned"; return 1; }
  [ $(( 0$(stat_mode "$DEPLOY_ENTRY") & 0022 )) = 0 ] || { log "verifier $DEPLOY_ENTRY is group/world writable"; return 1; }
  dir_ok "$(/usr/bin/dirname "$DEPLOY_ENTRY")" || { log "verifier directory is not root-owned and closed"; return 1; }
  # shellcheck source=/dev/null
  ( . "$DEPLOY_ENTRY" && eve_deploy_overrides && verify_sig "$1" "$2" ) >/dev/null 2>&1
}

# ----- inputs: copied into the root-only work dir, everything below judges the COPY, never the staged original -----
stage_copy() { # $1 src $2 dst $3 max bytes
  local o sz
  file_ok "$1" || die "not a regular file (or a symlink): ${1##*/}"
  o=$(stat_uid "$1"); [ "$o" = "$ROOT_UID" ] || { [ -n "$STAGE_UID" ] && [ "$o" = "$STAGE_UID" ]; } || die "input not owned by the login user or root: ${1##*/}"
  /usr/bin/head -c "$(( $3 + 1 ))" <"$1" >"$2" || die "cannot copy ${1##*/}"
  sz=$(stat_size "$2"); [ "$sz" -gt 0 ] && [ "$sz" -le "$3" ] || die "bad size for ${1##*/}"
}
# Calendar-only adapter: valid (ad-hoc) code signature with the hardened runtime, only the calendars entitlement, only calendar usage strings.
check_signature() { # $1 binary; 0 = ok, otherwise logs why
  local d e k bad=""
  "$CODESIGN" --verify --strict "$1" >/dev/null 2>&1 || { log "adapter has no valid code signature"; return 1; }
  d=$("$CODESIGN" -dv "$1" 2>&1) || { log "cannot read the adapter's code signature"; return 1; }
  case "$d" in *runtime*) ;; *) log "adapter is not signed with the hardened runtime (codesign -o runtime)"; return 1;; esac
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
# Sets MF_SHA / MF_SHA256 and leaves the verified copies in $WORK (in.MANIFEST, in.MANIFEST.sig, in.eve-pim). Changes nothing outside $WORK.
verify_inputs() { # $1 manifest $2 signature $3 adapter binary
  stage_copy "$1" "$WORK/in.MANIFEST" "$MAX_MF"
  stage_copy "$2" "$WORK/in.MANIFEST.sig" "$MAX_SIG"
  stage_copy "$3" "$WORK/in.eve-pim" "$MAX_BIN"
  verify_manifest_sig "$WORK/in.MANIFEST" "$WORK/in.MANIFEST.sig" || die "manifest signature verification FAILED (not signed by the pinned release key)"
  parse_manifest "$WORK/in.MANIFEST" || die "manifest malformed: need exactly eve-pim-manifest=1, pim_sha=<40 lowercase hex>, pim_sha256=<64 lowercase hex>"
  MF_SHA=$P_SHA; MF_SHA256=$P_SHA256
  [ "$MF_SHA" = "$PIM_SHA" ] || die "manifest pim_sha is not the pinned, security-reviewed adapter commit"
  [ "$(sha_of "$WORK/in.eve-pim")" = "$MF_SHA256" ] || die "adapter sha256 does not match the manifest pim_sha256: refusing to install"
  check_signature "$WORK/in.eve-pim" || die "adapter failed the code-signature checks"
  log "PRE manifest signature OK; adapter sha256 matches the manifest (source commit $MF_SHA)"
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
# Keep the installed adapter as the ONE rollback target, but only when it is what its own signed manifest says it is.
rotate_prev() { # $1 hash of the installed adapter
  /bin/rm -f "$PIM_PREV" "$PIM_PREV_MF" "$PIM_PREV_SIG"
  if file_ok "$PIM_MF" && file_ok "$PIM_MF_SIG" && verify_manifest_sig "$PIM_MF" "$PIM_MF_SIG" && parse_manifest "$PIM_MF" && [ "$P_SHA256" = "$1" ]; then
    atomic_put "$PIM_MF" "$PIM_PREV_MF" 644 && atomic_put "$PIM_MF_SIG" "$PIM_PREV_SIG" 644 && atomic_put "$PIM_BIN" "$PIM_PREV" 755 "$1" \
      && { log "§3 previous adapter kept for rollback (sha256 $(printf '%.12s' "$1")...)"; return 0; }
    /bin/rm -f "$PIM_PREV" "$PIM_PREV_MF" "$PIM_PREV_SIG"; log "§3 WARN: could not keep the previous adapter: no rollback available"
  else log "§3 installed adapter has no verified signed record: no rollback kept"; fi
}
put_current() { # puts $1 (binary) with its manifest $2 and signature $3 in place; on failure the saved copy of the old state ($WORK/bak.*) goes back
  local had=0 cur hm="" hs=""
  cur=$(installed_hash)
  if [ -n "$cur" ]; then had=1; /bin/cp -p "$PIM_BIN" "$WORK/bak.bin" || return 1; fi
  if file_ok "$PIM_MF" && file_ok "$PIM_MF_SIG"; then /bin/cp -p "$PIM_MF" "$WORK/bak.mf" && /bin/cp -p "$PIM_MF_SIG" "$WORK/bak.sig" || return 1; hm=$(sha_of "$WORK/bak.mf"); hs=$(sha_of "$WORK/bak.sig"); fi
  if atomic_put "$1" "$PIM_BIN" 755 && atomic_put "$2" "$PIM_MF" 644 && atomic_put "$3" "$PIM_MF_SIG" 644; then return 0; fi
  if [ "$had" = 1 ]; then
    atomic_put "$WORK/bak.bin" "$PIM_BIN" 755 "$cur" && log "previous adapter restored" || log "RESTORE FAILED: inspect $PIM_DIR by hand"
  else /bin/rm -f "$PIM_BIN"; log "unverified adapter removed (nothing was installed before)"; fi
  if [ -n "$hm" ]; then atomic_put "$WORK/bak.mf" "$PIM_MF" 644 "$hm" && atomic_put "$WORK/bak.sig" "$PIM_MF_SIG" 644 "$hs" || log "RESTORE of the signed record FAILED: inspect $PIM_DIR by hand"
  else /bin/rm -f "$PIM_MF" "$PIM_MF_SIG"; fi
  return 1
}
install_adapter() { # needs verify_inputs first. Sets INSTALL_RESULT=same|installed
  local cur
  INSTALL_RESULT=""
  if adapter_current; then
    INSTALL_RESULT=same
    log "§3 adapter with the same sha256 is already installed: not replaced (same code signature, no new TCC prompt)"
    # a missing/stale record would break the next rollback rotation: refresh it (never the binary)
    if ! { file_ok "$PIM_MF" && file_ok "$PIM_MF_SIG" && /usr/bin/cmp -s "$PIM_MF" "$WORK/in.MANIFEST" && /usr/bin/cmp -s "$PIM_MF_SIG" "$WORK/in.MANIFEST.sig"; }; then
      [ "$DRY_RUN" = 1 ] || { atomic_put "$WORK/in.MANIFEST" "$PIM_MF" 644 && atomic_put "$WORK/in.MANIFEST.sig" "$PIM_MF_SIG" 644 || die "cannot refresh the signed record"; }
    fi
    return 0
  fi
  if [ "$DRY_RUN" = 1 ]; then echo "  DRY: install $PIM_BIN (root:wheel 0755) via atomic rename; keep the previous one for rollback"; INSTALL_RESULT=installed; return 0; fi
  bin_dir_ok || die "$SOCK_BASE and $PIM_DIR must be real, root-owned directories closed to group/world writes"
  cur=$(installed_hash)
  # same bytes but wrong owner/mode: replaced in place (same hash, so the code signature and the TCC grant are unchanged); no rotation
  if [ -n "$cur" ] && [ "$cur" != "$MF_SHA256" ]; then rotate_prev "$cur"; fi
  put_current "$WORK/in.eve-pim" "$WORK/in.MANIFEST" "$WORK/in.MANIFEST.sig" || die "adapter install failed (see above); the previous state was restored"
  [ "$(sha_of "$PIM_BIN")" = "$MF_SHA256" ] || die "installed adapter hash changed after install"
  check_signature "$PIM_BIN" || die "installed adapter failed the code-signature checks"
  INSTALL_RESULT=installed
  log "§3 adapter installed at $PIM_BIN (root:wheel 0755), sha256 re-verified after install"
}
# Rollback follows the same rules as an install: the kept adapter must still match its own SIGNED manifest, the signature checks and the
# fixed root-owned target must hold, the swap is an atomic rename, the hash is re-verified, and an identical hash is a no-op. One step only.
rollback_adapter() { # sets INSTALL_RESULT=same|rolled-back
  local p
  INSTALL_RESULT=""
  file_ok "$PIM_PREV" && file_ok "$PIM_PREV_MF" && file_ok "$PIM_PREV_SIG" || die "no previous adapter is kept"
  owned_mode_ok "$PIM_PREV" 755 || die "kept previous adapter is not root:wheel 0755"
  verify_manifest_sig "$PIM_PREV_MF" "$PIM_PREV_SIG" || die "kept previous manifest signature verification FAILED"
  parse_manifest "$PIM_PREV_MF" || die "kept previous manifest is malformed"
  p=$P_SHA256
  [ "$(sha_of "$PIM_PREV")" = "$p" ] || die "kept previous adapter fails its recorded sha256: rollback refused"
  check_signature "$PIM_PREV" || die "kept previous adapter failed the code-signature checks"
  MF_SHA256=$p
  if adapter_current; then INSTALL_RESULT=same; log "§3 the installed adapter already has the previous sha256: nothing to roll back"; return 0; fi
  if [ "$DRY_RUN" = 1 ]; then echo "  DRY: roll back $PIM_BIN to the kept previous adapter"; INSTALL_RESULT=rolled-back; return 0; fi
  bin_dir_ok || die "$SOCK_BASE and $PIM_DIR must be real, root-owned directories closed to group/world writes"
  put_current "$PIM_PREV" "$PIM_PREV_MF" "$PIM_PREV_SIG" || die "rollback failed (see above); the state before the rollback was restored"
  [ "$(sha_of "$PIM_BIN")" = "$p" ] || die "installed adapter hash changed after rollback"
  /bin/rm -f "$PIM_PREV" "$PIM_PREV_MF" "$PIM_PREV_SIG"
  INSTALL_RESULT=rolled-back
  log "§3 rolled back to the previous adapter; sha256 re-verified; no further rollback is kept"
}

# ----- arguments: ONLY the install triple or --rollback; paths must be absolute (the script runs from /) -----
parse_args() {
  MODE=""; A_MF=""; A_SIG=""; A_BIN=""
  if [ "$#" = 1 ] && [ "$1" = --rollback ]; then MODE=rollback; return 0; fi
  [ "$#" = 6 ] || return 1
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --manifest) [ -z "$A_MF" ] && A_MF=${2:-} || return 1;;
      --sig) [ -z "$A_SIG" ] && A_SIG=${2:-} || return 1;;
      --adapter) [ -z "$A_BIN" ] && A_BIN=${2:-} || return 1;;
      *) return 1;;
    esac
    shift 2
  done
  case "$A_MF$A_SIG$A_BIN" in "") return 1;; esac
  for f in "$A_MF" "$A_SIG" "$A_BIN"; do case "$f" in /?*) ;; *) return 1;; esac; done
  MODE=install
}
usage() { echo "usage: sudo bash $0 --manifest <abs path> --sig <abs path> --adapter <abs path>   |   sudo bash $0 --rollback" >&2; }

# ----- embedded files (the repo copies are checked against these by tests) -----
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

# run only when executed; sourcing (the tests) stops here
if [ "${BASH_SOURCE[0]}" != "$0" ]; then return 0; fi

set -Eeuo pipefail
umask 022
parse_args "$@" || { usage; exit 2; }

# =============================== main: work dir, root + user context ===============================
WORK=$(mktemp -d /tmp/eve-install-calendar-adapter.XXXXXXXXXX) || { echo "mktemp failed" >&2; exit 1; }
chmod 0700 "$WORK"          # root-only: inputs, copies, build of the plist and the log live here
TS_STAMP=$(date +%Y%m%d-%H%M%S)
LOG=$WORK/install-$TS_STAMP.log
( umask 077; : > "$LOG" ); chmod 0600 "$LOG"
AGENT_STOPPED=0

cd /
if [ "$DRY_RUN" = 1 ]; then SUDO_USER="${SUDO_USER:-$(id -un)}"
else
  [ "$(id -u)" = 0 ] || { echo "run with: sudo bash $0 ..." >&2; exit 1; }
  [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] || { echo "SUDO_USER missing" >&2; exit 1; }
fi
U_UID=$(id -u "$SUDO_USER")
STAGE_UID=$U_UID
U_HOME=$(dscl . -read "/Users/$SUDO_USER" NFSHomeDirectory | awk '{print $2}')
UPLIST="$U_HOME/Library/LaunchAgents/$LABEL.plist"
as_user() { if [ "$(id -u)" = "$U_UID" ]; then "$@"; else sudo -u "$SUDO_USER" "$@"; fi; }
run_user() { if [ "$DRY_RUN" = 1 ]; then echo "  DRY(user): $*"; else as_user "$@"; fi; }
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
  [ "$DRY_RUN" = 1 ] && return
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
  if [ "$rc" != 0 ] && [ "$AGENT_STOPPED" = 1 ] && [ "$DRY_RUN" != 1 ] && [ -f "$UPLIST" ] && ! agent_loaded; then
    launchctl bootstrap "gui/$U_UID" "$UPLIST" 2>/dev/null && log "agent restarted after the failed run" || log "WARN: could not restart the agent: launchctl bootstrap gui/$U_UID $UPLIST"
  fi
  [ "$rc" = 0 ] || log "EXIT rc=$rc, log: $LOG"; }
trap 'on_err $LINENO' ERR
trap on_exit EXIT

log "install-calendar-adapter start (mode=$MODE, dry_run=$DRY_RUN, pim sha=$PIM_SHA, sandbox=$([ "$NO_SANDBOX" = 1 ] && echo off || echo on))"

# =============================== PRECHECKS + VERIFY (no changes outside the work dir) ===============================
[ -f /Library/LaunchDaemons/$GWLABEL.plist ] || die "gateway LaunchDaemon missing: run deploy-5.0.0.sh first (or deploy-5.0.0-with-calendar.sh)"
health_gw || die "gateway not healthy (deploy 5.0.0 first)"
[ "$(dscl_val /Users/_evegw UniqueID)" = 451 ] || die "_evegw missing / uid != 451"
[ "$(id -u "$SUDO_USER")" -ge 500 ] || die "login user uid unexpected"
case "$(launchctl print "gui/$U_UID" 2>&1 | head -1 || true)" in *"Could not find"*|"") die "no GUI session for $SUDO_USER: log in on the Mac screen first (TCC prompts need it)";; esac
is_hex_len "$PIM_SHA" 40 || die "PIM_SHA must be the full 40-character lowercase hex commit SHA"

if [ "$MODE" = rollback ]; then
  # =============================== rollback: previous adapter, same rules as an install ===============================
  if [ "$DRY_RUN" != 1 ] && agent_loaded; then launchctl bootout "gui/$U_UID/$LABEL" 2>/dev/null || true; AGENT_STOPPED=1; sleep 1; fi
  rollback_adapter
  if [ "$DRY_RUN" != 1 ] && [ "$AGENT_STOPPED" = 1 ]; then
    [ -f "$UPLIST" ] && launchctl bootstrap "gui/$U_UID" "$UPLIST" || die "could not restart the agent: launchctl bootstrap gui/$U_UID $UPLIST"
    AGENT_STOPPED=0
  fi
  DONE=1
  log "ROLLBACK DONE ($INSTALL_RESULT). Log: $LOG"
  exit 0
fi

verify_inputs "$A_MF" "$A_SIG" "$A_BIN"

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
  python3 - "$WORK/com.eve.pim.plist.template" "$RENDER" "$U_HOME" "$SOCK_DIR" "$NO_SANDBOX" "$PIM_DIR" <<'PY'
import plistlib, sys
src, dst, home, sock, nosb, pimdir = sys.argv[1:7]
t = open(src).read().replace("__HOME__", home).replace("__SOCKET_DIR__", sock).replace("__PIM_DIR__", pimdir)
pl = plistlib.loads(t.encode())
a = pl["ProgramArguments"]
assert not any("writes" in x for x in a), "writes flag in plist"
if nosb == "1": a[:] = a[3:]       # drop sandbox-exec -f <profile>
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
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: install $PIM_SB (root:wheel 0644) and render $UPLIST (sandbox=$([ "$NO_SANDBOX" = 1 ] && echo off || echo on))"
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
cat <<'TXT' | tee -a "$LOG"

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
    (샌드박스가 원인이면 같은 인자에 NO_SANDBOX=1 을 붙여 다시 실행: sudo NO_SANDBOX=1 bash install-calendar-adapter.sh --manifest ... --sig ... --adapter ...)
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
    log "         Answer the prompts, then re-run the same command (same --manifest/--sig/--adapter; idempotent: the adapter is not replaced again, it only runs the smoke test)"
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
OWNER_ID="${OWNER_USER_ID:-}"
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
  log "§6 PENDING: the calendar owner's user id could not be read yet (not signed in once, or more than one allowed user). Sign in on the device, then re-run the same command (idempotent), or pass OWNER_USER_ID=<users.id>."
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
log "INSTALL-CALENDAR-ADAPTER DONE. Log: $LOG"
