#!/bin/bash
# eve-session.sh: the ONE owner session for the release day. One sudo password, every stage fail-closed (a failed stage stops the run, nothing after it runs) and idempotent (run the same command again: finished stages are
# recognised from the real state of the world and skipped). Written for macOS /bin/bash 3.2; start it with `bash`, from a clean clone of eve-serving main that is checked out at the merge commit (see docs/runbooks/release-session.md):
#
#   bash deploy/nopasswd/eve-session.sh --merge-commit <40-hex> --gateway 5.1.0 --web 2.0.0 [--rehearse-gateway X.Y.Z --rehearse-web X.Y.Z] [--switch] [--unpause] [--dry-run]
#   bash deploy/nopasswd/eve-session.sh --merge-commit <40-hex> --web-only --web 2.0.0 [--rehearse-web X.Y.Z] [--dry-run]     SHORTEST PATH for the web release only:
#       preflight -> eve-web deploy key (the gateway key is not needed) -> one-shot -> tag eve-web vX -> wait for the signed release -> deploy web -> rehearse web (if given) -> table.
#       No gateway tag/release/deploy, no switch stage; PAUSE is never released by this mode (--unpause/--switch/--gateway are refused with it).
#
# STAGES (estimates in hours of wall time; most of it is waiting for CI and for you):
#   0 preflight  0.1  pins (sha256) of every script used, the clone, tools, and the READ-ONLY measurement (eve-measure.sh, 14 items): its gates decide whether the switch stage may run. Nothing is changed.
#   1 sudo       0.0  one `sudo -v` (the ONE password prompt of this session) and a keep-alive for the session.
#   2 keys       0.2  the two release-tag deploy keys (one per repository) are created if missing; if GitHub does not know them yet you are told where to add them (Title, key, "Allow write access") and asked to type `registered`.
#                     This comes BEFORE the install on purpose: the one-shot gives the `release-tags` ruleset its DeployKey bypass only if exactly one write key titled `release-tagger` / `eve-web-release-tagger` exists.
#   3 install    0.3  eve-oneshot.sh (re-install of the entrypoint, settings of all rulesets incl. the eve-web pull_request rule, PAUSE). Its own checks and clean-up run as documented there.
#   4 switch     0.5  OPTIONAL (--switch): the root-free service-account switch. Runs only if every gate of the measurement is OK and the switch script is shipped and pinned; otherwise it is SKIPPED and the deploy below uses the
#                     existing root path (fail-safe: nothing about the switch can stop the release). Contract of the switch script: exit 0 = switched, exit 10 = skipped itself (nothing changed), anything else = STOP.
#   5 tags       0.1  release-tag.sh for web vY (a clone of eve-web, --repo eve-web) FIRST and gateway vX (this clone): both tips are shown and you type `tag` once. A tag that exists is not touched.
#                     THE WEB PATH COMES FIRST AND IS INDEPENDENT OF THE GATEWAY: stages 5-8 do web, then the gateway steps as a separate branch. A gateway failure (main moved after the merge commit,
#                     no signed release, deploy refused) is reported, skips only the remaining gateway steps, fails the table at the end (PAUSE stays) and NEVER blocks or undoes the web release.
#   6 releases   0.4  waits (default 30 min) until each release has exactly its three signed files (MANIFEST, sigstore.json, tar) and is published.
#   7 deploy     0.3  `setup-unattended.sh deploy` gateway, then web (supervised: the install PAUSE is lifted for the deploy and put back).
#   8 rehearse   0.5  only with --rehearse-gateway/--rehearse-web (BOTH): `setup-unattended.sh rehearse KIND X.Y.Z` with a signed release that is built to FAIL its health check. It proves the automatic rollback and sets rehearsal_ok.
#                     The manual rollback verbs are NOT used for the drill: they blocklist the rolled-back version for ever, set PAUSE and remove rehearsal_ok, and a version can never be rolled forward again.
#   9 table      0.05 live versions, floors, PAUSE, blocklist, rehearsal_ok, verify: a PASS/FAIL table.
#  10 unpause    0.0  only with --unpause AND rehearsal_ok AND you type `unpause`. Without --unpause the PAUSE stays and the table tells you.
# Nothing in this script reads, prints or stores a secret; the keys are only created (ssh-keygen) and their public halves printed. Everything root runs is a text whose sha256 was checked before sudo was called.
# shellcheck disable=SC2015,SC2034
set -Eeuo pipefail
export LC_ALL=C
umask 077
export PATH=${EVE_SESSION_EXTRA_PATH:+$EVE_SESSION_EXTRA_PATH:}/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin   # EVE_SESSION_EXTRA_PATH exists for the tests (fake tools)

# ---- pins: every file this script runs. `bash deploy/nopasswd/pin-unattended.sh --oneshot` rewrites them; test/run.sh fails on a stale pin. ----
PIN_ONESHOT_SHA256=21f98cd742518a70220ae910911ca55cc98c5ab3b71ed388ecb1b5cec6c5d89a   # deploy/nopasswd/eve-oneshot.sh
PIN_MEASURE_SHA256=d834c395261e2a126bcae271c067f3653f3236e30f4753916fb30ad0bee46052   # deploy/nopasswd/eve-measure.sh
PIN_RELEASE_TAG_SHA256=b4aacc6db3c035cc3504c5b91ee6e8d3addbf58361bb19a20d414d2d820ed7f5   # deploy/nopasswd/release-tag.sh
PIN_RELEASE_TAG_WEB_SHA256=55145eeef94245c46c08b8996fec59d45dde65c54af3a00e04b23a461e05a798   # deploy/nopasswd/release-tag.eve-web.constants
PIN_SWITCH_SHA256=   # deploy/nopasswd/switch-service-account.sh: empty until that script ships (then the switch stage is always SKIPPED)

SERVING=gun025bba/eve-serving; WEB=gun025bba/eve-web
D=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd); ROOT=$(cd "$D/../.." && pwd)
STATE=${EVE_SESSION_STATE_DIR:-/var/db/eve-deploy}            # read with sudo; the knobs below exist for the tests, they only change where this script READS and how long it waits
POLL_SEC=${EVE_SESSION_POLL_SEC:-20}; TTY_FILE=${EVE_SESSION_TTY:-/dev/tty}
SESS_DIR=$HOME/.eve-session; SESS_STATE=$SESS_DIR/state
say() { printf '%s\n' "$*"; }
STAGE="start"
SOFT=0
die() {
  if [ "$SOFT" = 1 ]; then printf '\nGATEWAY step failed at stage "%s": %s\n' "$STAGE" "$*" >&2; exit 1; fi
  printf '\nSTOPPED at stage "%s": %s\nNothing after this stage ran. Fix the cause and run the SAME command again: finished stages are recognised and skipped.\n' "$STAGE" "$*" >&2; exit 1
}
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
V_RE='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'
is_ver() { [[ "$1" =~ $V_RE ]]; }
is_sha() { [[ "$1" =~ ^[0-9a-f]{40}$ ]]; }
ver_cmp() { local a=$1 b=$2 i x y; for i in 1 2 3; do x=${a%%.*}; y=${b%%.*}; a=${a#*.}; b=${b#*.}; if [ "$x" -lt "$y" ]; then echo -1; return 0; elif [ "$x" -gt "$y" ]; then echo 1; return 0; fi; done; echo 0; }

[ "$(id -u)" != 0 ] || die "run this as your own user, not as root: it asks for sudo once itself, and a root run would put every state file under root"
DRY=0 MERGE="" GW="" WV="" REH_GW="" REH_WV="" SWITCH=0 UNPAUSE=0 WAIT_MIN=30 FORCE_INSTALL=0 WEBONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1;; --switch) SWITCH=1;; --unpause) UNPAUSE=1;; --force-install) FORCE_INSTALL=1;; --web-only) WEBONLY=1;;
    --merge-commit) MERGE=${2:-}; shift;; --gateway) GW=${2:-}; shift;; --web) WV=${2:-}; shift;;
    --rehearse-gateway) REH_GW=${2:-}; shift;; --rehearse-web) REH_WV=${2:-}; shift;; --wait-minutes) WAIT_MIN=${2:-}; shift;;
    -h|--help) sed -n "2,36p" "$0"; exit 0;;
    *) die "unknown argument: $1 (see --help)";;
  esac; shift
done
[[ "$WAIT_MIN" =~ ^[0-9]{1,3}$ ]] || die "--wait-minutes must be a number"
is_ver "$WV" || die "--web X.Y.Z is required"
if [ "$WEBONLY" = 1 ]; then
  # the shortest path for the web release: no gateway tag, release, deploy or rehearsal, no switch stage, and PAUSE is never released (it also guards the gateway, which is not rehearsed here)
  [ -z "$GW" ] && [ -z "$REH_GW" ] || die "--web-only takes no --gateway and no --rehearse-gateway"
  [ "$SWITCH" = 0 ] || die "--web-only takes no --switch"
  [ "$UNPAUSE" = 0 ] || die "--web-only never releases PAUSE (it guards the gateway too): run the full session later for that"
else
  is_ver "$GW" || die "--gateway X.Y.Z is required (or --web-only)"
fi
is_sha "$MERGE" || die "--merge-commit needs the full 40-hex sha of the merge commit on main"
if [ "$WEBONLY" = 1 ]; then [ -z "$REH_WV" ] || is_ver "$REH_WV" || die "--rehearse-web needs X.Y.Z"
elif [ -n "$REH_GW$REH_WV" ]; then is_ver "$REH_GW" && is_ver "$REH_WV" || die "the rehearsal needs BOTH --rehearse-gateway X.Y.Z and --rehearse-web X.Y.Z (signed test releases built to fail their health check)"; fi
[ "$UNPAUSE" = 0 ] || [ -n "$REH_GW" ] || [ "$DRY" = 1 ] || true   # --unpause also works in a later run (rehearsal_ok is checked then)

# ---- state file: values are validated, never sourced ----
state_get() { [ -f "$SESS_STATE" ] || return 0; sed -n "s/^$1=\\([0-9A-Za-z._-]*\\)\$/\\1/p" "$SESS_STATE" | tail -1; }
state_put() { [ "$DRY" = 1 ] && return 0; grep -v "^$1=" "$SESS_STATE" 2>/dev/null >"$SESS_STATE.tmp" || true; printf '%s=%s\n' "$1" "$2" >>"$SESS_STATE.tmp"; mv -f "$SESS_STATE.tmp" "$SESS_STATE"; }
ensure_state_dir() {
  [ "$DRY" = 0 ] || return 0
  [ ! -L "$SESS_DIR" ] || die "$SESS_DIR is a symlink"
  [ -d "$SESS_DIR" ] || mkdir -m 700 "$SESS_DIR"
  [ "$(stat -c %u "$SESS_DIR" 2>/dev/null || stat -f %u "$SESS_DIR")" = "$(id -u)" ] || die "$SESS_DIR is not yours"
  chmod 700 "$SESS_DIR"; [ -f "$SESS_STATE" ] || : >"$SESS_STATE"
}
TTY_FD=0
ask_word() { # PROMPT WORD : three tries, read from the terminal only (one descriptor, opened once)
  local ans="" tries=0
  [ "$DRY" = 0 ] || { say "  DRY-RUN: would ask: $1"; return 0; }
  if [ "$TTY_FD" = 0 ]; then if { exec 3<"$TTY_FILE"; } 2>/dev/null; then TTY_FD=1; else TTY_FD=-1; fi; fi
  [ "$TTY_FD" = 1 ] || return 1
  while [ "$tries" -lt 3 ]; do printf '%s' "$1"; if read -r ans <&3; then :; else printf '\n'; return 1; fi; [ "$ans" = "$2" ] && return 0; tries=$((tries + 1)); done; return 1
}
# ---- root: only checked texts are passed to sudo ----
SUDO_KEEP=""
sudo_start() {
  [ "$DRY" = 0 ] || { say "  DRY-RUN: sudo -v (the one password prompt) and a keep-alive"; return 0; }
  command -v sudo >/dev/null 2>&1 || die "sudo is missing"
  sudo -v || die "sudo was refused (no password, or not an administrator)"
  ( while kill -0 "$$" 2>/dev/null; do sudo -n -v 2>/dev/null || exit 0; sleep 50; done ) >/dev/null 2>&1 </dev/null & SUDO_KEEP=$!
  trap '[ -z "$SUDO_KEEP" ] || kill "$SUDO_KEEP" 2>/dev/null || true' EXIT
}
live_ver() { # gateway|web -> version or empty
  local f=current_version; [ "$1" = web ] && f=current_web_version
  sudo -n /usr/bin/head -1 "$STATE/$f" 2>/dev/null | tr -d ' \r' | grep -E "$V_RE" || true
}
state_file_has() { sudo -n /bin/test -e "$STATE/$1" 2>/dev/null; }
su_() { sudo -n /bin/bash -c "$SU_TEXT" setup-unattended --src-commit "$MERGE" "$@"; }

# ---- GitHub reads (gh, authenticated for reads; no write ever goes through gh here) ----
tag_exists() { [ "$(gh api "repos/$1/git/matching-refs/tags/v$2" --jq '[.[]|select(.ref=="refs/tags/v'"$2"'")]|length' 2>/dev/null || echo 0)" = 1 ]; }
release_files() { gh api "repos/$1/releases/tags/v$2" --jq 'select(.draft==false)|[.assets[]|select(.state=="uploaded")|.name]|sort|join(" ")' 2>/dev/null || true; }
want_files() { printf 'eve-%s-%s.MANIFEST eve-%s-%s.sigstore.json eve-%s-%s.tar' "$1" "$2" "$1" "$2" "$1" "$2"; }
remote_tip() { gh api "repos/$1/git/ref/heads/main" --jq .object.sha 2>/dev/null || true; }

stage() { STAGE=$1; printf '\n== stage %s  (about %s h)\n' "$1" "$2"; }

# =========================================================== 0 preflight
stage preflight 0.1
say "  clone: $ROOT"
[ "$DRY" = 1 ] && say "  DRY-RUN: nothing is changed, no sudo, no network, no ssh; the pins, the clone and the measurement are checked for real"
cd "$ROOT"
command -v git >/dev/null 2>&1 || die "git is missing"
[ "$(git rev-parse HEAD 2>/dev/null)" = "$MERGE" ] || die "this clone is not at the merge commit $MERGE (git checkout it, or use the clone the printed command makes)"
[ "$(git symbolic-ref -q --short HEAD 2>/dev/null || true)" = main ] || die "main is not checked out"
[ -z "$(git status --porcelain --untracked-files=no 2>/dev/null)" ] || die "the clone has uncommitted changes to tracked files"
need_pin() { # NAME FILE PIN
  [ -f "$D/$2" ] && [ ! -L "$D/$2" ] || die "$2 is missing from this clone"
  [ "$(sha256 "$D/$2")" = "$3" ] || die "$2 does not match the sha256 pinned in eve-session.sh (a stale pin or a changed file): the run is refused"
  say "  pin OK  $2"
}
need_pin oneshot eve-oneshot.sh "$PIN_ONESHOT_SHA256"; need_pin measure eve-measure.sh "$PIN_MEASURE_SHA256"
need_pin tag release-tag.sh "$PIN_RELEASE_TAG_SHA256"; need_pin tagweb release-tag.eve-web.constants "$PIN_RELEASE_TAG_WEB_SHA256"
PIN_SU=$(sed -n 's/^PIN_UNATTENDED_SHA256=\([0-9a-f]\{64\}\).*/\1/p' "$D/eve-oneshot.sh" | head -1)
[ -n "$PIN_SU" ] || die "no PIN_UNATTENDED_SHA256 in eve-oneshot.sh"
SU_TEXT=$(cat "$D/setup-unattended.sh"; printf x); SU_TEXT=${SU_TEXT%x}
[ "$(printf '%s' "$SU_TEXT" | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; } | cut -d' ' -f1)" = "$PIN_SU" ] || die "setup-unattended.sh does not match the sha256 pinned in eve-oneshot.sh"
say "  pin OK  setup-unattended.sh (the pin of eve-oneshot.sh)"
if [ "$SWITCH" = 1 ]; then if [ -n "$PIN_SWITCH_SHA256" ]; then need_pin switch switch-service-account.sh "$PIN_SWITCH_SHA256"; else say "  switch script: not shipped yet (PIN_SWITCH_SHA256 is empty): the switch stage will be SKIPPED"; fi; fi
for t in gh jq curl ssh ssh-keygen sudo; do command -v "$t" >/dev/null 2>&1 || die "'$t' is missing"; done
[ "$DRY" = 1 ] || gh auth status >/dev/null 2>&1 || die "gh is not logged in (a read-only login is enough: gh auth login)"
say "  measurement (read-only, 14 items):"
SWITCH_GATES=0
if bash "$D/eve-measure.sh" >"${TMPDIR:-/tmp}/.eve-measure.$$" 2>&1; then SWITCH_GATES=1; fi
sed 's/^/    /' "${TMPDIR:-/tmp}/.eve-measure.$$"; rm -f "${TMPDIR:-/tmp}/.eve-measure.$$"
if [ "$SWITCH_GATES" = 1 ]; then say "  gates OK: the switch stage may run (if --switch)"; else say "  gates NOT OK: the switch stage will be SKIPPED; the deploy uses the existing root path"; fi
PLAN="install"; [ "$SWITCH" = 1 ] && PLAN="$PLAN, switch"
if [ "$WEBONLY" = 1 ]; then PLAN="$PLAN, tag web v$WV, wait for the signed release, deploy web $WV (WEB ONLY: no gateway)"; [ -z "$REH_WV" ] || PLAN="$PLAN, rehearse web (test release $REH_WV)"
else PLAN="$PLAN, tag gateway v$GW and web v$WV, wait for the signed releases, deploy gateway $GW and web $WV"; [ -z "$REH_GW" ] || PLAN="$PLAN, rehearse (test releases $REH_GW / $REH_WV)"; fi
say "  plan: $PLAN, table, unpause only on your word"
ensure_state_dir

# =========================================================== 1 sudo
stage sudo 0.0
sudo_start

# =========================================================== 2 keys
stage keys 0.2
KEYS="eve-serving:.eve-release-tagger:release-tagger:$SERVING eve-web:.eve-web-release-tagger:eve-web-release-tagger:$WEB"
[ "$WEBONLY" = 0 ] || KEYS="eve-web:.eve-web-release-tagger:eve-web-release-tagger:$WEB"
key_file() { printf '%s/%s/id_ed25519' "$HOME" "$1"; }
key_ok() { # DIRNAME REPO : the key exists and GitHub accepts it for that repository (read access over ssh)
  local kf; kf=$(key_file "$1"); [ -f "$kf" ] || return 1
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_SSH_COMMAND="ssh -F /dev/null -i $kf -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$D/github_known_hosts" \
    git ls-remote "git@github.com:$2.git" HEAD >/dev/null 2>&1
}
all_keys_ok() { local e name dir title repo; for e in $KEYS; do IFS=: read -r name dir title repo <<<"$e"; key_ok "$dir" "$repo" || return 1; done; }
if [ "$DRY" = 1 ]; then
  for e in $KEYS; do IFS=: read -r name dir title repo <<<"$e"; say "  DRY-RUN: ensure $HOME/$dir/id_ed25519 (ed25519, no passphrase); title \"$title\"; github.com/$repo/settings/keys/new with Allow write access"; done
elif all_keys_ok; then say "  the deploy key(s) exist and GitHub accepts them: nothing to do"
else
  for e in $KEYS; do
    IFS=: read -r name dir title repo <<<"$e"; kf=$(key_file "$dir")
    if [ ! -f "$kf" ]; then mkdir -p -m 700 "$HOME/$dir"; ssh-keygen -q -t ed25519 -N '' -C "$title" -f "$kf" || die "ssh-keygen failed"; say "  created $kf"; fi
    [ "$(stat -c %a "$kf" 2>/dev/null || stat -f %Lp "$kf")" = 600 ] || die "$kf must be mode 0600"
    say "  $name:  open  https://github.com/$repo/settings/keys/new   Title: $title   Key (public half):"; sed 's/^/      /' "$kf.pub"; say "      tick \"Allow write access\", then Add key."
  done
  ask_word "  type 'registered' when the key(s) above are added: " registered || die "no confirmation"
  all_keys_ok || die "GitHub does not accept one of the keys yet (title, repository and the key text must match exactly)"
  say "  key(s) accepted"
fi

# =========================================================== 3 install
stage install 0.3
if [ "$DRY" = 1 ]; then say "  DRY-RUN: bash eve-oneshot.sh --merge-commit $MERGE (its own dry run follows)"; bash "$D/eve-oneshot.sh" --dry-run --merge-commit "$MERGE" | sed 's/^/    /'
elif [ "$FORCE_INSTALL" = 0 ] && [ "$(state_get oneshot)" = "$MERGE" ] && [ -n "$(live_ver gateway)" ]; then say "  the one-shot already ran for $MERGE and the entrypoint is installed: skipped (--force-install runs it again)"
else
  bash "$D/eve-oneshot.sh" --merge-commit "$MERGE" || { rc=$?; die "eve-oneshot.sh failed (exit $rc). The cause is in the lines ABOVE the \"== 5.\" clean-up text: look for \"API ERROR\", \"FAIL\" or \"eve-oneshot:\" (that clean-up only closes the login). It is idempotent: re-run after the fix"; }
  state_put oneshot "$MERGE"
fi

# =========================================================== 4 switch
stage switch 0.5
if [ "$SWITCH" = 0 ]; then say "  not requested (--switch): skipped, the existing root path is used"
elif [ "$SWITCH_GATES" = 0 ]; then say "  SKIPPED: the measurement gates are not OK (above); the deploy continues over the existing root path"
elif [ -z "$PIN_SWITCH_SHA256" ] || [ ! -f "$D/switch-service-account.sh" ]; then say "  SKIPPED: the switch script is not shipped; the deploy continues over the existing root path"
elif [ "$DRY" = 1 ]; then say "  DRY-RUN: would run switch-service-account.sh as root (exit 0 = switched, 10 = skipped itself, else STOP)"
else
  SW_TEXT=$(cat "$D/switch-service-account.sh"; printf x); SW_TEXT=${SW_TEXT%x}
  [ "$(printf '%s' "$SW_TEXT" | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; } | cut -d' ' -f1)" = "$PIN_SWITCH_SHA256" ] || die "switch-service-account.sh changed after the pin check"
  rc=0; sudo -n /bin/bash -c "$SW_TEXT" switch-service-account --merge-commit "$MERGE" || rc=$?
  case "$rc" in 0) say "  switched";; 10) say "  the switch script skipped itself (nothing changed): continuing over the root path";; *) die "the switch script failed (exit $rc): the state is not known to be unchanged; read its output";; esac
fi

# =========================================================== 5 tags
# ORDER AND INDEPENDENCE: the web release comes FIRST and its stages are fatal as before. The gateway steps (tag, wait, deploy, rehearsal) run AFTER the web ones as an
# independent branch: a failure there is reported, sets GW_FAIL, skips the rest of the gateway steps and fails the table at the end, but it never undoes or blocks the web release.
stage tags 0.1
WEBDIR=$SESS_DIR/eve-web
GW_ON=1; [ "$WEBONLY" = 0 ] || GW_ON=0
GW_FAIL=0; GW_TAG=1; WV_TAG=1
soft() { # run a gateway step in a subshell with SOFT=1 (die then prints a gateway-only message and leaves just the subshell): a failure sets GW_FAIL and never stops the web path
  local rc=0; ( SOFT=1; "$@" ) || rc=$?
  if [ "$rc" != 0 ]; then GW_FAIL=1; say "  GATEWAY $GW: this step failed (see above). The web path is not affected; the remaining gateway steps are skipped and the table will show a FAIL row."; fi
  return 0
}
gw_tag_push() { ( bash "$D/release-tag.sh" "$GW" "$MERGE" ) || die "release-tag.sh refused gateway v$GW (its reason is above)"; }
if [ "$DRY" = 1 ]; then
  if [ "$WEBONLY" = 1 ]; then say "  DRY-RUN: release-tag.sh --repo eve-web $WV (clone of $WEB in $WEBDIR); you type 'tag'"
  else say "  DRY-RUN: release-tag.sh --repo eve-web $WV (clone of $WEB in $WEBDIR) FIRST, then release-tag.sh $GW (this clone, tip must equal $MERGE; if main moved the gateway steps are skipped and reported); you type 'tag' once"; fi
else
  if tag_exists "$WEB" "$WV"; then WV_TAG=1; else WV_TAG=0; fi
  if [ "$GW_ON" = 1 ]; then if tag_exists "$SERVING" "$GW"; then GW_TAG=1; else GW_TAG=0; fi; fi
  if [ "$WV_TAG" = 1 ]; then say "  v$WV exists in eve-web: not touched"; fi
  if [ "$GW_ON" = 1 ] && [ "$GW_TAG" = 1 ]; then say "  v$GW exists in eve-serving: not touched"; fi
  if [ "$GW_ON" = 1 ] && [ "$GW_TAG" = 0 ]; then
    tip=$(remote_tip "$SERVING")
    if [ "$tip" != "$MERGE" ]; then
      GW_FAIL=1; GW_TAG=1
      say "  GATEWAY $GW: the tip of eve-serving main is ${tip:-unknown}, not the merge commit $MERGE (main moved): the gateway steps are SKIPPED, the web path continues."
      say "  To tag the gateway later run the SAME command again with the new merge commit (finished web stages are recognised and skipped; the pins renew)."
    fi
  fi
  if [ "$WV_TAG" = 0 ] || [ "$GW_TAG" = 0 ]; then
    if [ "$WV_TAG" = 0 ]; then
      [ -d "$WEBDIR/.git" ] || git clone -q "https://github.com/$WEB.git" "$WEBDIR" || die "cannot clone $WEB"
      ( cd "$WEBDIR" && git checkout -q main && git pull -q --ff-only ) || die "cannot bring the eve-web clone to main"
      [ "$(git -C "$WEBDIR" rev-parse HEAD)" = "$(remote_tip "$WEB")" ] || die "the eve-web clone is not at the tip of its main"
    fi
    say "  about to tag (web first):"
    [ "$WV_TAG" = 1 ] || say "    eve-web     v$WV at $(git -C "$WEBDIR" rev-parse --short=12 HEAD)  ($(git -C "$WEBDIR" log -1 --format=%s | cut -c1-70))"
    [ "$GW_TAG" = 1 ] || say "    eve-serving v$GW at $MERGE  ($(git log -1 --format=%s "$MERGE" | cut -c1-70))"
    ask_word "  type 'tag' to push the tag(s): " tag || die "no confirmation"
    if [ "$WV_TAG" = 0 ]; then ( cd "$WEBDIR" && bash "$D/release-tag.sh" --repo eve-web "$WV" ) || die "release-tag.sh refused web v$WV (its reason is above)"; fi
    if [ "$GW_TAG" = 0 ]; then soft gw_tag_push; fi
  fi
fi

# =========================================================== 6 releases
stage releases 0.4
wait_release() { # KIND REPO VER
  local want got n=0 max=$(( WAIT_MIN * 60 / POLL_SEC + 1 ))
  want=$(want_files "$1" "$3" | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')
  while :; do
    got=$(release_files "$2" "$3")
    if [ "$got" = "$want" ]; then say "  $1 v$3: published with exactly $want"; return 0; fi
    n=$((n + 1)); [ "$n" -le "$max" ] || die "$1 v$3 has no complete signed release after $WAIT_MIN minutes (files now: ${got:-none}). See https://github.com/$2/actions"
    printf '  waiting for the signed %s v%s release (%s)\n' "$1" "$3" "${got:-no files yet}"; sleep "$POLL_SEC"
  done
}
gw_wait() { wait_release gateway "$SERVING" "$GW"; if [ -n "$REH_GW" ]; then wait_release gateway "$SERVING" "$REH_GW"; fi; }
if [ "$DRY" = 1 ]; then say "  DRY-RUN: wait for web v$WV$( [ -n "$REH_WV" ] && echo " and the test release $REH_WV" )$( [ "$GW_ON" = 1 ] && echo "; then (independent) gateway v$GW$( [ -n "$REH_GW" ] && echo " and the test release $REH_GW" )" )"
else
  wait_release web "$WEB" "$WV"; if [ -n "$REH_WV" ]; then wait_release web "$WEB" "$REH_WV"; fi
  if [ "$GW_ON" = 1 ] && [ "$GW_FAIL" = 0 ]; then soft gw_wait; fi
fi

# =========================================================== 7 deploy
stage deploy 0.3
deploy_kind() { # KIND VER
  local cur; cur=$(live_ver "$1")
  if [ -n "$cur" ] && [ "$(ver_cmp "$cur" "$2")" = 0 ]; then say "  $1 $2 is already live: skipped"; return 0; fi
  if [ -n "$cur" ] && [ "$(ver_cmp "$cur" "$2")" = 1 ]; then die "$1 $cur is live, which is NEWER than $2: a downgrade is never done by this script"; fi
  say "  deploying $1 $2 (live now: ${cur:-none})"; su_ deploy "$1" "$2" || die "the deploy of $1 $2 failed (output above; the install PAUSE was put back)"
  [ "$(live_ver "$1")" = "$2" ] || die "after the deploy $1 $2 is not the live version"
}
gw_deploy() { deploy_kind gateway "$GW"; }
if [ "$DRY" = 1 ]; then say "  DRY-RUN: setup-unattended.sh deploy web $WV$( [ "$GW_ON" = 1 ] && echo "; then (independent) deploy gateway $GW" )"
else
  deploy_kind web "$WV"
  if [ "$GW_ON" = 1 ] && [ "$GW_FAIL" = 0 ]; then soft gw_deploy; fi
fi

# =========================================================== 8 rehearse
stage rehearse 0.5
rehearse_kind() { # KIND TESTVER LIVEVER
  if sudo -n /usr/bin/grep -qxF "$1 $2" "$STATE/blocklist" 2>/dev/null; then say "  $1 $2 is on the blocklist: this rehearsal already ran"; return 0; fi
  say "  rehearsing $1 with the failing test release $2 (live must stay $3)"; su_ rehearse "$1" "$2" || die "the $1 rehearsal failed (PAUSE stays set; output above)"
  [ "$(live_ver "$1")" = "$3" ] || die "after the $1 rehearsal the live version is not $3"
}
gw_rehearse() { rehearse_kind gateway "$REH_GW" "$GW"; }
if [ -z "$REH_GW$REH_WV" ]; then say "  not requested (--rehearse-web$( [ "$GW_ON" = 1 ] && echo "/--rehearse-gateway" )): skipped. PAUSE stays set and rehearsal_ok is not written."
elif [ "$DRY" = 1 ]; then say "  DRY-RUN: setup-unattended.sh rehearse web $REH_WV$( [ "$GW_ON" = 1 ] && echo " ; then (independent) rehearse gateway $REH_GW" ) (each must fail its health check and roll back to the live version)"
else
  rehearse_kind web "$REH_WV" "$WV"
  if [ "$GW_ON" = 1 ] && [ "$GW_FAIL" = 0 ]; then soft gw_rehearse; fi
fi

# =========================================================== 9 table
stage table 0.05
ROWS_BAD=0
row() { # NAME EXPECT ACTUAL
  if [ "$2" = "$3" ]; then printf '  %-4s  %-34s  %s\n' PASS "$1" "$3"; else printf '  %-4s  %-34s  %s (want %s)\n' FAIL "$1" "$3" "$2"; ROWS_BAD=$((ROWS_BAD + 1)); fi
}
if [ "$DRY" = 1 ]; then say "  DRY-RUN: a table of live versions, floors, PAUSE, blocklist, rehearsal_ok and the output of setup-unattended.sh verify"
else
  if [ "$GW_ON" = 1 ] && [ "$GW_FAIL" = 1 ]; then row "gateway $GW steps (independent of web)" ok "FAILED or skipped (see above; web is unaffected)"; fi
  [ "$GW_ON" = 0 ] || [ "$GW_FAIL" = 1 ] || row "gateway live" "$GW" "$(live_ver gateway)"
  row "web live" "$WV" "$(live_ver web)"
  [ "$GW_ON" = 0 ] || [ "$GW_FAIL" = 1 ] || row "gateway release v$GW signed (3 files)" "$(want_files gateway "$GW" | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')" "$(release_files "$SERVING" "$GW")"
  row "web release v$WV signed (3 files)" "$(want_files web "$WV" | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')" "$(release_files "$WEB" "$WV")"
  [ "$GW_ON" = 0 ] || [ "$GW_FAIL" = 1 ] || row "floor not above gateway live" yes "$(f=$(sudo -n /usr/bin/head -1 "$STATE/floor" 2>/dev/null | tr -d ' \r'); { is_ver "${f:-x}" && [ "$(ver_cmp "$f" "$GW")" != 1 ] && echo yes; } || echo no)"
  row "floor_web not above web live" yes "$(f=$(sudo -n /usr/bin/head -1 "$STATE/floor_web" 2>/dev/null | tr -d ' \r'); { is_ver "${f:-x}" && [ "$(ver_cmp "$f" "$WV")" != 1 ] && echo yes; } || echo no)"
  if [ -n "$REH_GW$REH_WV" ]; then
    row "rehearsal_ok written" yes "$(state_file_has rehearsal_ok && echo yes || echo no)"
    [ "$GW_ON" = 0 ] || [ "$GW_FAIL" = 1 ] || row "gateway $REH_GW blocklisted" yes "$(sudo -n /usr/bin/grep -qxF "gateway $REH_GW" "$STATE/blocklist" 2>/dev/null && echo yes || echo no)"
    row "web $REH_WV blocklisted" yes "$(sudo -n /usr/bin/grep -qxF "web $REH_WV" "$STATE/blocklist" 2>/dev/null && echo yes || echo no)"
  fi
  say "  PAUSE: $(state_file_has PAUSE && echo present || echo absent)   rehearsal_ok: $(state_file_has rehearsal_ok && echo present || echo absent)"
  say "  setup-unattended.sh verify:"; su_ verify 2>&1 | sed 's/^/    /' || ROWS_BAD=$((ROWS_BAD + 1))
  [ "$ROWS_BAD" = 0 ] || die "$ROWS_BAD table row(s) FAIL: PAUSE is NOT released"
fi

# =========================================================== 10 unpause
stage unpause 0.0
if [ "$DRY" = 1 ]; then say "  DRY-RUN: with --unpause, rehearsal_ok present and the word 'unpause' typed: setup-unattended.sh unpause"
elif [ "$WEBONLY" = 1 ]; then say "  web-only run: PAUSE is left exactly as it is (it also guards the gateway, which this run did not touch)"
elif ! state_file_has PAUSE; then say "  PAUSE is already absent: unattended deploys are ON"
elif [ "$UNPAUSE" = 0 ]; then say "  PAUSE stays. After you have read the table: run the same command again with --unpause (needs rehearsal_ok)."
elif ! state_file_has rehearsal_ok; then die "no rehearsal_ok: the rollback rehearsal has not passed, so PAUSE is not released (give --rehearse-gateway and --rehearse-web)"
else
  ask_word "  type 'unpause' to turn unattended deploys ON: " unpause || die "no confirmation: PAUSE stays"
  su_ unpause || die "unpause failed"
  say "  PAUSE removed"
fi
STAGE="done"; say; say "session finished"
