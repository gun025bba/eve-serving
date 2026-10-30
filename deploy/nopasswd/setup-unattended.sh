#!/bin/bash
# setup-unattended.sh: the root half of the unattended-deploy install. It is run by eve-oneshot.sh INSIDE THE SAME sudo session as setup-deploy-nopasswd.sh (one password), as
#   sudo /bin/bash -c "<this checked text>" setup-unattended [--src-commit <40-hex main commit>] <subcommand> [args]
# Files it downloads (eve-poll, the plist template) come from that ONE main commit and are trusted only by the sha256 pinned below. Root keeps every temporary file in ONE `mktemp -d` (0700) directory.
# Subcommands (each idempotent):
#   accounts            create the two service accounts (evedeploy: runs the poller and is the only user in sudoers; evebackup: unused until the DB backup job exists), hidden, no login
#                       shell, no password, not admin, home 0700 under /var, plus the poller work dir /var/evedeploy/poll
#   state               first time only (marker file): PAUSE (kept if the installer already made it), floors = the live versions (if missing), empty blocklist; installs the poller and its LaunchDaemon plist (NOT loaded),
#                       then re-verifies the sha256 of every installed file (poller and the rendered plist). Needs --src-commit. Invalidates rehearsal_ok.
#   deploy KIND X.Y.Z   a SUPERVISED deploy as the deploy user through the sudoers rule (the owner is sitting there). An install/initial PAUSE is lifted for the duration and put back on every exit path
#   rehearse KIND X.Y.Z removes the initial PAUSE, deploys a signed test release that MUST fail its health check, then checks: it failed, the previous version is live again, PAUSE is back,
#                       the version is on the blocklist, the floor is intact. PASS writes $STATE/rehearsal_ok. PAUSE is recreated on every exit path.
#   unpause             removes PAUSE, only if rehearsal_ok exists; rehearsal_ok is consumed (the owner can always `sudo rm $STATE/PAUSE` by hand)
#   raise-floor KIND X.Y.Z   the ONLY manual way to change a rollback floor, and it can only RAISE it (equal = no-op; lower or above the live version = refused)
#   load                loads the poller LaunchDaemon (do this last)
#   verify              read-only PASS/FAIL table of the finished install (accounts, sudoers, entrypoint, poller, state, loaded); a FAIL makes the exit status non-zero
# Never reads or prints a secret; there is none. macOS arm64 only (EVE_SKIP_OS=1 is for the tests).
# shellcheck disable=SC2015  # "A && B || die" is intended
main() {
exec </dev/null
set -Eeuo pipefail
umask 022
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
if [ "$(id -u)" = 0 ]; then for _v in $(compgen -e); do case "$_v" in EVE_*) unset "$_v";; esac; done; fi   # a root run never honours the test knobs
unset _v
SRC_COMMIT=""   # from --src-commit (the main commit the files are fetched from); the content is trusted only by the two sha256 pins below
SRC_RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
SHA_POLL=edce0faf4171901f02561da1b19a966804e824f717834e436a13151452904b5d
SHA_PLIST=e85875caf422172dd8fbfa860f4650ac42c3d50af471fb0d1ff20105035df5ed
DEPLOY_USER=evedeploy BACKUP_USER=evebackup LABEL=com.llmserving.evepoll
LIBEXEC=${EVE_LIBEXEC:-/usr/local/libexec/eve}; STATE=${EVE_STATE:-/var/db/eve-deploy}; HOME_BASE=${EVE_HOME_BASE:-/var}
LAUNCHD_DIR=${EVE_LAUNCHD_DIR:-/Library/LaunchDaemons}; LOGF=${EVE_LOGF:-/var/log/eve-deploy.log}
OWNER=${EVE_OWNER:-root:wheel}; DSCL=${EVE_DSCL:-dscl}; LAUNCHCTL=${EVE_LAUNCHCTL:-launchctl}; SUDO=${EVE_SUDO:-sudo}; SRC_DIR=${EVE_SRC_DIR:-}
SKIP_ROOT=${EVE_SKIP_ROOT:-0}; SKIP_OS=${EVE_SKIP_OS:-0}
die() { echo "setup-unattended: $*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
sha_of() { if [ -x /usr/bin/shasum ]; then /usr/bin/shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }
[ "$SKIP_ROOT" = 1 ] || [ "$(id -u)" = 0 ] || die "run with sudo"
[ "$SKIP_OS" = 1 ] || { [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; } || die "macOS on Apple silicon only"
if [ "${1:-}" = --src-commit ]; then SRC_COMMIT=${2:-}; shift; shift || true; fi
[ $# -ge 1 ] || die "usage: [--src-commit <40-hex>] accounts | state | deploy KIND X.Y.Z | rehearse KIND X.Y.Z | unpause | raise-floor KIND X.Y.Z | load | verify"
CMD=$1; shift
V_RE='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'
HOME_D=$HOME_BASE/$DEPLOY_USER WORK_D=$HOME_BASE/$DEPLOY_USER/poll

user_exists() { "$DSCL" . -read "/Users/$1" UniqueID >/dev/null 2>&1; }
uid_of() { "$DSCL" . -read "/Users/$1" UniqueID | awk '{print $2}'; }
free_id() { local i used; used=$("$DSCL" . -list /Users UniqueID | awk '{print $2}'; "$DSCL" . -list /Groups PrimaryGroupID | awk '{print $2}')
  for i in $(seq 460 499); do printf '%s\n' "$used" | grep -qx "$i" || { echo "$i"; return 0; }; done; return 1; }
ensure_account() { # $1 name
  local n=$1 id sh
  if user_exists "$n"; then
    id=$(uid_of "$n"); [ "$id" != 0 ] && [ "$id" != 451 ] || die "$n has an unacceptable uid"
    sh=$("$DSCL" . -read "/Users/$n" UserShell | awk '{print $2}'); [ "$sh" = /usr/bin/false ] || die "$n exists with a login shell ($sh): refusing to reuse it"
    if command -v dsmemberutil >/dev/null 2>&1 && dsmemberutil checkmembership -U "$n" -G admin 2>/dev/null | grep -q 'is a member'; then die "$n is an admin: refusing"; fi
    say "  account $n: exists (uid $id), not admin, no login shell: kept"; return 0
  fi
  id=$(free_id) || die "no free uid in 460-499"
  "$DSCL" . -create "/Groups/$n"; "$DSCL" . -create "/Groups/$n" PrimaryGroupID "$id"
  "$DSCL" . -create "/Users/$n"; "$DSCL" . -create "/Users/$n" UserShell /usr/bin/false; "$DSCL" . -create "/Users/$n" RealName "Eve service $n"
  "$DSCL" . -create "/Users/$n" UniqueID "$id"; "$DSCL" . -create "/Users/$n" PrimaryGroupID "$id"; "$DSCL" . -create "/Users/$n" NFSHomeDirectory "$HOME_BASE/$n"
  "$DSCL" . -create "/Users/$n" IsHidden 1; "$DSCL" . -create "/Users/$n" Password '*'
  say "  account $n: created (uid $id, hidden, no login shell, no password, not admin)"
}
ensure_dir() { # $1 dir $2 user $3 mode
  [ ! -L "$1" ] || die "$1 is a symlink"
  if [ "$SKIP_ROOT" = 1 ]; then install -d -m "$3" "$1"; else install -d -o "$2" -m "$3" "$1"; fi   # SKIP_ROOT is a test knob (ignored for root)
}

# ONE private temp directory for everything root writes outside its destinations: mktemp -d (0700, random name); no other name under /tmp is ever created or derived from it.
TMPD=""
cleanup() { [ -z "$TMPD" ] || rm -rf "$TMPD"; }
trap cleanup EXIT
mk_tmpd() { [ -n "$TMPD" ] || { TMPD=$(umask 077; mktemp -d "${TMPDIR:-/tmp}/eve-unatt.XXXXXX") || die "mktemp -d failed"; chmod 0700 "$TMPD"; }; }
fetch_pinned() { # $1 name $2 sha $3 dest (inside $TMPD) $4 path under deploy/nopasswd
  [ -n "$SRC_DIR" ] || [[ "$SRC_COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "--src-commit <40-hex commit on main> is required for this step"
  if [ -n "$SRC_DIR" ]; then cp "$SRC_DIR/$(basename "$4")" "$3" || die "cannot copy $1"; else curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --max-time 120 -o "$3" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/$4" || die "download failed: $1"; fi
  [ "$(sha_of "$3")" = "$2" ] || die "$1: sha256 does not match the pin"
}

cmd_accounts() {
  say "accounts"
  ensure_account "$DEPLOY_USER"; ensure_account "$BACKUP_USER"
  ensure_dir "$HOME_BASE/$BACKUP_USER" "$BACKUP_USER" 0700
  ensure_dir "$HOME_D" "$DEPLOY_USER" 0700
  ensure_dir "$WORK_D" "$DEPLOY_USER" 0700
}

installed_ok() { # re-verify the sha256 of every file this script installed (the poller and the rendered plist) as they are on disk now; prints a row per file
  local ok=0 want
  want=$(head -1 "$STATE/plist_sha256" 2>/dev/null || true)
  if [ "$(sha_of "$LIBEXEC/poll" 2>/dev/null || true)" = "$SHA_POLL" ]; then say "  PASS  installed poller matches its pin"; else say "  FAIL  installed poller does not match its pin"; ok=1; fi
  if [ -n "$want" ] && [ "$(sha_of "$LAUNCHD_DIR/$LABEL.plist" 2>/dev/null || true)" = "$want" ]; then say "  PASS  installed plist matches the rendered text"; else say "  FAIL  installed plist does not match the rendered text"; ok=1; fi
  return "$ok"
}
state_ok() { [ -d "$STATE" ] && [ ! -L "$STATE" ] && [ -f "$STATE/current_version" ] || die "the deploy entrypoint is not installed ($STATE)"; }
cmd_state() {
  say "state, poller files"
  state_ok
  [ -d "$WORK_D" ] || die "the poller work dir does not exist: run the accounts step first"
  [ "$(head -1 "$STATE/allowed_user" 2>/dev/null)" = "$DEPLOY_USER" ] || die "allowed_user is not $DEPLOY_USER: run the installer with --deploy-user $DEPLOY_USER first"
  mk_tmpd
  fetch_pinned poll "$SHA_POLL" "$TMPD/poll" eve-poll
  fetch_pinned plist "$SHA_PLIST" "$TMPD/plist.template" launchd/com.llmserving.evepoll.plist.template
  sed -e "s#__USER__#$DEPLOY_USER#g" -e "s#__LIBEXEC__#$LIBEXEC#g" -e "s#__WORK__#$WORK_D#g" "$TMPD/plist.template" >"$TMPD/plist.rendered"
  if command -v plutil >/dev/null 2>&1; then plutil -lint "$TMPD/plist.rendered" >/dev/null || die "rendered plist is invalid"; fi
  PLIST_SHA=$(sha_of "$TMPD/plist.rendered")
  install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$TMPD/poll" "$LIBEXEC/poll.new" && mv -f "$LIBEXEC/poll.new" "$LIBEXEC/poll"
  install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0644 "$TMPD/plist.rendered" "$LAUNCHD_DIR/$LABEL.plist.new" && mv -f "$LAUNCHD_DIR/$LABEL.plist.new" "$LAUNCHD_DIR/$LABEL.plist"
  ( umask 077; printf '%s\n' "$PLIST_SHA" >"$STATE/plist_sha256" ); chown "$OWNER" "$STATE/plist_sha256" 2>/dev/null || true
  rm -f "$STATE/rehearsal_ok"                                        # new files installed: an earlier rehearsal says nothing about them
  installed_ok || die "an installed file does not match its pin after the install (see above)"
  say "  poller installed ($LIBEXEC/poll, $LAUNCHD_DIR/$LABEL.plist), sha256 re-verified, not loaded"
  if [ -f "$STATE/unattended_installed" ]; then say "  state files already created earlier: PAUSE, floors and blocklist left as they are"; return 0; fi
  ( umask 077
    [ -e "$STATE/PAUSE" ] || [ -L "$STATE/PAUSE" ] || printf 'reason=initial (no rehearsal yet) at=%s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" >"$STATE/PAUSE"
    [ -f "$STATE/blocklist" ] || : >"$STATE/blocklist"
    [ -f "$STATE/floor" ] || head -1 "$STATE/current_version" >"$STATE/floor"
    if [ -f "$STATE/current_web_version" ] && [ ! -f "$STATE/floor_web" ]; then head -1 "$STATE/current_web_version" >"$STATE/floor_web"; fi
    date '+%Y-%m-%dT%H:%M:%S%z' >"$STATE/unattended_installed" )
  chown -h "$OWNER" "$STATE/PAUSE" "$STATE/blocklist" "$STATE/floor" "$STATE/unattended_installed" 2>/dev/null || true
  [ ! -f "$STATE/floor_web" ] || chown "$OWNER" "$STATE/floor_web" 2>/dev/null || true
  # seed the poller's "done" marks with the live versions: it must not try to redeploy the release that is already running
  head -1 "$STATE/current_version" >"$WORK_D/done_gateway"
  if [ -f "$STATE/current_web_version" ]; then head -1 "$STATE/current_web_version" >"$WORK_D/done_web"; fi
  if [ "$SKIP_ROOT" != 1 ]; then chown "$DEPLOY_USER" "$WORK_D/done_gateway"; [ ! -f "$WORK_D/done_web" ] || chown "$DEPLOY_USER" "$WORK_D/done_web"; fi
  say "  PAUSE set (reason=initial), floors set to the live versions, blocklist empty, poller marks seeded"
}

kind_args() { # $1 kind $2 version -> sets VERB RVERB CURF
  [[ "$2" =~ $V_RE ]] || die "version must be X.Y.Z"
  case "$1" in gateway) VERB=deploy; CURF=current_version;; web) VERB=deploy-web; CURF=current_web_version;; *) die "kind must be gateway or web";; esac
}
as_deploy_user() { "$SUDO" -u "$DEPLOY_USER" "$@"; }
run_deploy() { # $1 kind $2 version ; prints the entrypoint's last line; returns its status
  as_deploy_user "$LIBEXEC/fetch" "$1" "$2" >/dev/null || return 99
  as_deploy_user /usr/bin/sudo -n "$LIBEXEC/deploy" "$VERB" 2>&1 | tail -3 || return "${PIPESTATUS[0]}"
}
restore_pause() { [ -e "$STATE/PAUSE" ] || [ -L "$STATE/PAUSE" ] || printf 'reason=install (restored after the supervised deploy) at=%s\n' "$(date +%s)" >"$STATE/PAUSE"; }
cmd_deploy() {
  [ $# -eq 2 ] || die "usage: deploy KIND X.Y.Z"
  local VERB CURF held=0; kind_args "$1" "$2"; state_ok
  say "supervised deploy of $1 $2 as $DEPLOY_USER"
  if [ -e "$STATE/PAUSE" ] || [ -L "$STATE/PAUSE" ]; then
    case "$(head -1 "$STATE/PAUSE" 2>/dev/null)" in reason=install*|reason=initial*) ;; *) die "PAUSE exists with a reason other than the install: remove it by hand if you mean it";; esac
    held=1
    # the install PAUSE is lifted ONLY for this supervised deploy and put back on every exit path
    rm -f "$STATE/PAUSE"; trap 'restore_pause; cleanup' EXIT
  fi
  run_deploy "$1" "$2" || die "the deploy of $1 $2 failed (see above and $LOGF); nothing else was done"
  [ "$(head -1 "$STATE/$CURF")" = "$2" ] || die "after the deploy the installed $1 version is not $2"
  say "  $1 $2 is live"
  [ "$held" = 0 ] || say "  (the install PAUSE is put back now)"
}
cmd_rehearse() {
  [ $# -eq 2 ] || die "usage: rehearse KIND X.Y.Z"
  local VERB CURF before rc=0 fl bad=0; kind_args "$1" "$2"; state_ok
  [ -f "$STATE/PAUSE" ] && [ -f "$STATE/unattended_installed" ] || die "no initial PAUSE: run the state step first"
  before=$(head -1 "$STATE/$CURF")
  # security (rehearsal invariants): the test release must be STRICTLY HIGHER than what is live (monotonic: no version below or equal to the live one is ever offered, so
  # floor and "never lower" rules stay as they are), never one that was used before, and the drill may change NOTHING in the state except ONE new blocklist line for itself.
  [[ "$before" =~ $V_RE ]] || die "the live $1 version is unreadable: no rehearsal"
  [ "$(ver_cmp "$2" "$before")" = 1 ] || die "the test release $2 is not higher than the live $1 version $before: refused (a rehearsal never offers an equal or lower version)"
  ! grep -qxF "$1 $2" "$STATE/blocklist" 2>/dev/null || die "$1 $2 is already on the blocklist: a test release is used once"
  snap_state() { local f; for f in current_version current_web_version floor floor_web; do printf '%s=%s\n' "$f" "$(head -1 "$STATE/$f" 2>/dev/null || true)"; done; }
  snap_before=$(snap_state); bl_before=$(cat "$STATE/blocklist" 2>/dev/null || true)
  trap '[ -e "$STATE/PAUSE" ] || [ -L "$STATE/PAUSE" ] || printf "reason=rehearsal-safety at=%s\n" "$(date +%s)" >"$STATE/PAUSE"; cleanup' EXIT
  say "rehearsal: $1 $2 must FAIL its health check and be rolled back (installed now: $before)"
  rm -f "$STATE/rehearsal_ok"
  # the drill also covers what an unattended run depends on besides the deploy verb: the deploy user may call BOTH rollback verbs without a password, and the poller is the pinned file and runs as that user
  chk() { if [ "$2" = "$3" ]; then printf '  PASS  %s\n' "$1"; else printf '  FAIL  %s (got %s, want %s)\n' "$1" "$3" "$2"; bad=1; fi; }
  chk "the deploy user may run '$LIBEXEC/deploy rollback' without a password" yes "$(as_deploy_user /usr/bin/sudo -n -l "$LIBEXEC/deploy" rollback >/dev/null 2>&1 && echo yes || echo no)"
  chk "the deploy user may run '$LIBEXEC/deploy rollback-web' without a password" yes "$(as_deploy_user /usr/bin/sudo -n -l "$LIBEXEC/deploy" rollback-web >/dev/null 2>&1 && echo yes || echo no)"
  chk "the poller is the pinned file and the deploy user can execute it" yes "$([ "$(sha_of "$LIBEXEC/poll" 2>/dev/null || true)" = "$SHA_POLL" ] && as_deploy_user /bin/test -x "$LIBEXEC/poll" && echo yes || echo no)"
  [ "$bad" = 0 ] || die "REHEARSAL FAIL before the test release: PAUSE stays set"
  rm -f "$STATE/PAUSE"
  run_deploy "$1" "$2" || rc=$?
  [ "$rc" != 0 ] && rc=failed || rc=ok
  chk "the test release failed its health check" failed "$rc"
  chk "the previous version is live again" "$before" "$(head -1 "$STATE/$CURF")"
  chk "PAUSE exists" yes "$([ -e "$STATE/PAUSE" ] && echo yes || echo no)"
  chk "$1 $2 is on the blocklist" yes "$(grep -qxF "$1 $2" "$STATE/blocklist" 2>/dev/null && echo yes || echo no)"
  fl=floor; [ "$1" = web ] && fl=floor_web
  chk "the floor is intact (valid version, not above the live one)" yes "$({ f=$(head -1 "$STATE/$fl" 2>/dev/null); [[ "$f" =~ $V_RE ]] && echo yes; } || echo no)"
  chk "state unchanged by the drill: both live versions and both floors (nothing was promoted, the floor did not move)" "$snap_before" "$(snap_state)"
  chk "the blocklist gained exactly one line (the test release) and lost none" "$bl_before" "$(grep -vxF "$1 $2" "$STATE/blocklist" 2>/dev/null || true)"
  chk "the test release is on the blocklist exactly once" 1 "$(grep -cxF "$1 $2" "$STATE/blocklist" 2>/dev/null || true)"
  chk "the entrypoint log shows an automatic restore" yes "$(tail -40 "$LOGF" 2>/dev/null | grep -Eq 'automatic (web )?rollback OK' && echo yes || echo no)"
  if [ "$bad" = 0 ]; then ( umask 077; date '+%Y-%m-%dT%H:%M:%S%z' >"$STATE/rehearsal_ok" ); say "REHEARSAL PASS (PAUSE stays set; remove it with: unpause, or sudo rm $STATE/PAUSE)"
  else die "REHEARSAL FAIL: PAUSE stays set, nothing was unpaused"; fi
}
cmd_unpause() {
  state_ok
  [ -f "$STATE/rehearsal_ok" ] || die "no rehearsal_ok: the rollback rehearsal has not passed (the owner can still remove $STATE/PAUSE by hand)"
  rm -f "$STATE/PAUSE" "$STATE/rehearsal_ok"; say "PAUSE removed: unattended deploys are ON (poller must be loaded; rehearsal_ok is consumed)"
}
ver_cmp() { # prints -1, 0, 1 for $1 <,=,> $2 (both valid X.Y.Z)
  local a b i x y; a=$1; b=$2
  for i in 1 2 3; do x=${a%%.*}; y=${b%%.*}; a=${a#*.}; b=${b#*.}
    if [ "$x" -lt "$y" ]; then echo -1; return 0; elif [ "$x" -gt "$y" ]; then echo 1; return 0; fi
  done; echo 0
}
cmd_raise_floor() { # the only manual floor change: upwards only
  [ $# -eq 2 ] || die "usage: raise-floor KIND X.Y.Z"
  local VERB CURF fl cur old c; kind_args "$1" "$2"; state_ok
  fl=floor; [ "$1" = web ] && fl=floor_web
  cur=$(head -1 "$STATE/$CURF" 2>/dev/null); [[ "$cur" =~ $V_RE ]] || die "the live $1 version is unreadable"
  c=$(ver_cmp "$2" "$cur"); [ "$c" != 1 ] || die "$2 is above the live $1 version $cur: a floor above what is running is refused"
  old=$(head -1 "$STATE/$fl" 2>/dev/null || true)
  if [ -n "$old" ]; then
    [[ "$old" =~ $V_RE ]] || die "the existing $fl is invalid: remove it by hand ($STATE/$fl) and run raise-floor again"
    c=$(ver_cmp "$2" "$old")
    [ "$c" != -1 ] || die "$2 is below the current $fl $old: the floor is never lowered"
    [ "$c" != 0 ] || { say "$fl is already $old: nothing to do"; return 0; }
  fi
  ( umask 077; printf '%s\n' "$2" >"$STATE/$fl.new" ) && mv -f "$STATE/$fl.new" "$STATE/$fl"; chown "$OWNER" "$STATE/$fl" 2>/dev/null || true
  say "$fl raised to $2 (was ${old:-missing})"
}
cmd_load() {
  [ -f "$LAUNCHD_DIR/$LABEL.plist" ] || die "no plist: run the state step first"
  "$LAUNCHCTL" bootout "system/$LABEL" >/dev/null 2>&1 || true
  "$LAUNCHCTL" bootstrap system "$LAUNCHD_DIR/$LABEL.plist" || die "launchctl bootstrap failed"
  say "poller loaded ($LABEL, every 5 minutes)"
}
statm() { stat -c '%a %u' "$1" 2>/dev/null || stat -f '%Lp %u' "$1"; }
cmd_verify() {
  local bad=0 u sd="${EVE_SUDOERS_D:-/etc/sudoers.d}" m f st fl
  chk() { if [ "$2" = "$3" ]; then printf '  PASS  %s\n' "$1"; else printf '  FAIL  %s (got [%s], want [%s])\n' "$1" "$3" "$2"; bad=1; fi; }
  say "install table"
  for u in "$DEPLOY_USER" "$BACKUP_USER"; do
    chk "account $u exists" yes "$(user_exists "$u" && echo yes || echo no)"
    chk "account $u has no login shell" /usr/bin/false "$("$DSCL" . -read "/Users/$u" UserShell 2>/dev/null | awk '{print $2}')"
    if command -v dsmemberutil >/dev/null 2>&1; then chk "account $u is not admin" no "$(dsmemberutil checkmembership -U "$u" -G admin 2>/dev/null | grep -q 'is a member' && echo yes || echo no)"; fi
    chk "home of $u is 0700" 700 "$(statm "$HOME_BASE/$u" | awk '{print $1}')"
  done
  chk "state: allowed_user is $DEPLOY_USER" "$DEPLOY_USER" "$(head -1 "$STATE/allowed_user" 2>/dev/null)"
  chk "sudoers file names only $DEPLOY_USER" "$DEPLOY_USER" "$(grep -v '^#' "$sd/eve-deploy" 2>/dev/null | grep -v '^[[:space:]]*$' | grep -v '^Defaults' | awk '{print $1}' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  for f in deploy fetch poll; do
    st=$(statm "$LIBEXEC/$f" 2>/dev/null || echo "none none")
    chk "$LIBEXEC/$f is root-owned and not group/world-writable" "yes" "$({ m=${st% *}; [ "${st#* }" = "$(id -u "${OWNER%:*}" 2>/dev/null || echo 0)" ] && [ "$m" = 755 ] || [ "$m" = 555 ]; } && echo yes || echo no)"
  done
  chk "installed poller matches its sha256 pin" "$SHA_POLL" "$(sha_of "$LIBEXEC/poll" 2>/dev/null || echo none)"
  chk "installed plist matches the sha256 recorded at install" "$(head -1 "$STATE/plist_sha256" 2>/dev/null || echo none)" "$(sha_of "$LAUNCHD_DIR/$LABEL.plist" 2>/dev/null || echo missing)"
  chk "poller plist installed" yes "$([ -f "$LAUNCHD_DIR/$LABEL.plist" ] && echo yes || echo no)"
  chk "poller loaded" yes "$("$LAUNCHCTL" print "system/$LABEL" >/dev/null 2>&1 && echo yes || echo no)"
  chk "floor is a valid version" yes "$({ fl=$(head -1 "$STATE/floor" 2>/dev/null); [[ "$fl" =~ $V_RE ]] && echo yes; } || echo no)"
  if [ -f "$STATE/current_web_version" ]; then chk "floor_web is a valid version" yes "$({ fl=$(head -1 "$STATE/floor_web" 2>/dev/null); [[ "$fl" =~ $V_RE ]] && echo yes; } || echo no)"; fi
  chk "blocklist file exists" yes "$([ -f "$STATE/blocklist" ] && echo yes || echo no)"
  if [ -e "$STATE/PAUSE" ]; then say "  INFO  deploys are PAUSED ($(head -1 "$STATE/PAUSE" 2>/dev/null | cut -c1-60)): expected until the rollback rehearsal passed; see docs/runbooks/unattended-deploy.md"
  else say "  INFO  deploys are NOT paused: unattended deploys are ON"; fi
  [ "$bad" = 0 ] || die "install table has FAIL rows"
}
case "$CMD" in
  verify) cmd_verify;;
  raise-floor) cmd_raise_floor "$@";;
  accounts) cmd_accounts;; state) cmd_state;; deploy) cmd_deploy "$@";; rehearse) cmd_rehearse "$@";; unpause) cmd_unpause;; load) cmd_load;;
  *) die "unknown subcommand: $CMD";;
esac
}
main "$@"
