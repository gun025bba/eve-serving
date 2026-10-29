#!/bin/bash
# setup-unattended.sh: the root half of the unattended-deploy install. It is run by eve-oneshot.sh INSIDE THE SAME sudo session as setup-deploy-nopasswd.sh (one password), as
#   sudo /bin/bash -c "<this checked text>" setup-unattended <subcommand> [args]
# Subcommands (each idempotent):
#   accounts            create the two service accounts (evedeploy: runs the poller and is the only user in sudoers; evebackup: unused until the DB backup job exists), hidden, no login
#                       shell, no password, not admin, home 0700 under /var, plus the poller work dir /var/evedeploy/poll
#   state               first time only (marker file): PAUSE (reason=initial), floors = the live versions, empty blocklist; installs the poller and its LaunchDaemon plist (NOT loaded)
#   deploy KIND X.Y.Z   a SUPERVISED deploy as the deploy user through the sudoers rule, run before `state` (the owner is sitting there); refused by the entrypoint while PAUSE exists
#   rehearse KIND X.Y.Z removes the initial PAUSE, deploys a signed test release that MUST fail its health check, then checks: it failed, the previous version is live again, PAUSE is back,
#                       the version is on the blocklist, the floor is intact. PASS writes $STATE/rehearsal_ok. PAUSE is recreated on every exit path.
#   unpause             removes PAUSE, only if rehearsal_ok exists (the owner can always `sudo rm $STATE/PAUSE` by hand)
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
SRC_COMMIT=a4a6c026bdca3edc8021abe852f833e38f9bb387   # pin-unattended.sh <A> rewrites the three pins below
SRC_RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
SHA_POLL=d14ad0d4f612370dfa8d67b2159379b4dc450af0d70f42b4024b3c672f863996
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
[ $# -ge 1 ] || die "usage: accounts | state | deploy KIND X.Y.Z | rehearse KIND X.Y.Z | unpause | load | verify"
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

fetch_pinned() { # $1 name $2 sha $3 dest
  local t; t=$(mktemp "${TMPDIR:-/tmp}/eve-unatt.XXXXXX") || die "mktemp failed"
  if [ -n "$SRC_DIR" ]; then cp "$SRC_DIR/$(basename "$4")" "$t"; else curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --max-time 120 -o "$t" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/$4" || { rm -f "$t"; die "download failed: $1"; }; fi
  [ "$(sha_of "$t")" = "$2" ] || { rm -f "$t"; die "$1: sha256 does not match the pin"; }
  cp "$t" "$3" && rm -f "$t"
}

cmd_accounts() {
  say "accounts"
  ensure_account "$DEPLOY_USER"; ensure_account "$BACKUP_USER"
  ensure_dir "$HOME_BASE/$BACKUP_USER" "$BACKUP_USER" 0700
  ensure_dir "$HOME_D" "$DEPLOY_USER" 0700
  ensure_dir "$WORK_D" "$DEPLOY_USER" 0700
}

state_ok() { [ -d "$STATE" ] && [ ! -L "$STATE" ] && [ -f "$STATE/current_version" ] || die "the deploy entrypoint is not installed ($STATE)"; }
cmd_state() {
  say "state, poller files"
  state_ok
  [ -d "$WORK_D" ] || die "the poller work dir does not exist: run the accounts step first"
  [ "$(head -1 "$STATE/allowed_user" 2>/dev/null)" = "$DEPLOY_USER" ] || die "allowed_user is not $DEPLOY_USER: run the installer with --deploy-user $DEPLOY_USER first"
  local t; t=$(mktemp "${TMPDIR:-/tmp}/eve-unatt.XXXXXX")
  fetch_pinned poll "$SHA_POLL" "$t.poll" eve-poll; install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$t.poll" "$LIBEXEC/poll.new" && mv -f "$LIBEXEC/poll.new" "$LIBEXEC/poll"
  fetch_pinned plist "$SHA_PLIST" "$t.plist" launchd/com.llmserving.evepoll.plist.template
  sed -e "s#__USER__#$DEPLOY_USER#g" -e "s#__LIBEXEC__#$LIBEXEC#g" -e "s#__WORK__#$WORK_D#g" "$t.plist" >"$t.out"
  if command -v plutil >/dev/null 2>&1; then plutil -lint "$t.out" >/dev/null || die "rendered plist is invalid"; fi
  install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0644 "$t.out" "$LAUNCHD_DIR/$LABEL.plist"
  rm -f "$t" "$t.poll" "$t.plist" "$t.out"
  say "  poller installed ($LIBEXEC/poll, $LAUNCHD_DIR/$LABEL.plist), not loaded"
  if [ -f "$STATE/unattended_installed" ]; then say "  state files already created earlier: PAUSE, floors and blocklist left as they are"; return 0; fi
  ( umask 077
    printf 'reason=initial (no rehearsal yet) at=%s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" >"$STATE/PAUSE"
    [ -f "$STATE/blocklist" ] || : >"$STATE/blocklist"
    [ -f "$STATE/floor" ] || head -1 "$STATE/current_version" >"$STATE/floor"
    if [ -f "$STATE/current_web_version" ] && [ ! -f "$STATE/floor_web" ]; then head -1 "$STATE/current_web_version" >"$STATE/floor_web"; fi
    date '+%Y-%m-%dT%H:%M:%S%z' >"$STATE/unattended_installed" )
  chown "$OWNER" "$STATE/PAUSE" "$STATE/blocklist" "$STATE/floor" "$STATE/unattended_installed" 2>/dev/null || true
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
cmd_deploy() {
  [ $# -eq 2 ] || die "usage: deploy KIND X.Y.Z"
  local VERB CURF; kind_args "$1" "$2"; state_ok
  say "supervised deploy of $1 $2 as $DEPLOY_USER"
  [ ! -e "$STATE/PAUSE" ] || die "PAUSE exists: a supervised deploy is done BEFORE the state step (or after the owner removed PAUSE)"
  run_deploy "$1" "$2" || die "the deploy of $1 $2 failed (see above and $LOGF); nothing else was done"
  [ "$(head -1 "$STATE/$CURF")" = "$2" ] || die "after the deploy the installed $1 version is not $2"
  say "  $1 $2 is live"
}
cmd_rehearse() {
  [ $# -eq 2 ] || die "usage: rehearse KIND X.Y.Z"
  local VERB CURF before rc=0 fl bad=0; kind_args "$1" "$2"; state_ok
  [ -f "$STATE/PAUSE" ] && [ -f "$STATE/unattended_installed" ] || die "no initial PAUSE: run the state step first"
  before=$(head -1 "$STATE/$CURF")
  trap '[ -e "$STATE/PAUSE" ] || printf "reason=rehearsal-safety at=%s\n" "$(date +%s)" >"$STATE/PAUSE"' EXIT
  say "rehearsal: $1 $2 must FAIL its health check and be rolled back (installed now: $before)"
  rm -f "$STATE/PAUSE" "$STATE/rehearsal_ok"
  run_deploy "$1" "$2" || rc=$?
  chk() { if [ "$2" = "$3" ]; then printf '  PASS  %s\n' "$1"; else printf '  FAIL  %s (got %s, want %s)\n' "$1" "$3" "$2"; bad=1; fi; }
  [ "$rc" != 0 ] && rc=failed || rc=ok
  chk "the test release failed its health check" failed "$rc"
  chk "the previous version is live again" "$before" "$(head -1 "$STATE/$CURF")"
  chk "PAUSE exists" yes "$([ -e "$STATE/PAUSE" ] && echo yes || echo no)"
  chk "$1 $2 is on the blocklist" yes "$(grep -qxF "$1 $2" "$STATE/blocklist" 2>/dev/null && echo yes || echo no)"
  fl=floor; [ "$1" = web ] && fl=floor_web
  chk "the floor is intact (valid version, not above the live one)" yes "$({ f=$(head -1 "$STATE/$fl" 2>/dev/null); [[ "$f" =~ $V_RE ]] && echo yes; } || echo no)"
  chk "the entrypoint log shows an automatic restore" yes "$(tail -40 "$LOGF" 2>/dev/null | grep -Eq 'automatic (web )?rollback OK' && echo yes || echo no)"
  if [ "$bad" = 0 ]; then ( umask 077; date '+%Y-%m-%dT%H:%M:%S%z' >"$STATE/rehearsal_ok" ); say "REHEARSAL PASS (PAUSE stays set; remove it with: unpause, or sudo rm $STATE/PAUSE)"
  else die "REHEARSAL FAIL: PAUSE stays set, nothing was unpaused"; fi
}
cmd_unpause() {
  state_ok
  [ -f "$STATE/rehearsal_ok" ] || die "no rehearsal_ok: the rollback rehearsal has not passed (the owner can still remove $STATE/PAUSE by hand)"
  rm -f "$STATE/PAUSE"; say "PAUSE removed: unattended deploys are ON (poller must be loaded)"
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
  chk "poller plist installed" yes "$([ -f "$LAUNCHD_DIR/$LABEL.plist" ] && echo yes || echo no)"
  chk "poller loaded" yes "$("$LAUNCHCTL" print "system/$LABEL" >/dev/null 2>&1 && echo yes || echo no)"
  chk "floor is a valid version" yes "$({ fl=$(head -1 "$STATE/floor" 2>/dev/null); [[ "$fl" =~ $V_RE ]] && echo yes; } || echo no)"
  chk "blocklist file exists" yes "$([ -f "$STATE/blocklist" ] && echo yes || echo no)"
  if [ -e "$STATE/PAUSE" ]; then say "  INFO  deploys are PAUSED ($(head -1 "$STATE/PAUSE" 2>/dev/null | cut -c1-60)): expected until the rollback rehearsal passed; see docs/runbooks/unattended-deploy.md"
  else say "  INFO  deploys are NOT paused: unattended deploys are ON"; fi
  [ "$bad" = 0 ] || die "install table has FAIL rows"
}
case "$CMD" in
  verify) cmd_verify;;
  accounts) cmd_accounts;; state) cmd_state;; deploy) cmd_deploy "$@";; rehearse) cmd_rehearse "$@";; unpause) cmd_unpause;; load) cmd_load;;
  *) die "unknown subcommand: $CMD";;
esac
}
main "$@"
