#!/bin/bash
# eve-oneshot.sh: the one-time setup of the UNATTENDED deploy (and of today's web deploy), run ONCE by the owner on the Mac. Idempotent (a re-run is safe). Written to be read.
#
#   clone-free (the command with the pins is printed by whoever merged the PR; --merge-commit is the squash/rebase commit on main):
#     s=$(curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/<MERGE>/deploy/nopasswd/eve-oneshot.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = <sha256> ] \
#       && bash -c "$s" eve-oneshot --merge-commit <MERGE> [options]
#   from a clone: bash deploy/nopasswd/eve-oneshot.sh --dry-run          (bash, not zsh)
#
#   --dry-run                 print every step and API call; change nothing, ask nothing, no login, no sudo, no network
#   --merge-commit SHA        the commit on main that contains the reviewed files (required for a real run)
#   --calendar-adapter X.Y.Z  install the PIM calendar adapter (eve-pim) of the signed release vX.Y.Z in the same sudo session (setup-pim-adapter.sh: verifies the Sigstore-signed manifest, installs to root-owned /opt/eve-pim/bin,
#                             asks YOU for one calendar prompt on the Mac screen; flags stay off; PAUSE stays). Runs after the installer and before the deploys below. Re-run the same command if the prompt was not answered in time.
#   --gateway-deploy X.Y.Z    today's SUPERVISED gateway deploy (kind=gateway release already tagged and signed), like --web-deploy; BEFORE PAUSE is set
#   --web-deploy X.Y.Z        today's SUPERVISED web deploy (kind=web release already tagged and signed); done inside the sudo session, BEFORE PAUSE is set
#   --rehearse KIND X.Y.Z     rollback rehearsal in the same sudo session with a signed test release that must fail its health check (see docs/runbooks/unattended-deploy.md);
#                             without it the install ends with PAUSE set and the rehearsal is done later
#   --unpause-after-pass      only with --rehearse: if the rehearsal passes, remove PAUSE (unattended deploys ON). Without it PAUSE stays until you remove it.
#   --settings-only           steps 0-3 only (no sudo)
#
# ORDER (the sudo step is reached ONLY if every earlier step and the settings table PASSED; any failure stops there, then only the token clean-up runs):
#   0  pins:      the merge commit is on main and belongs to a merged PR; the installer, setup-unattended.sh, [setup-pim-adapter.sh with --calendar-adapter] and the settings files at that commit have the sha256 pinned below
#   1  login:     `gh auth login --web` as gun025bba in a THROWAWAY gh config dir (OAuth); admin of eve-serving, eve-web and eve-ios; the repositories are public
#   2  settings:  the reviewed JSON files, applied with that login (rulesets incl. release-branches on eve-serving, environments + wait timer, secret scanning + push protection, immutable releases, Actions settings; eve-ios: only the main-protect ruleset with the required check `compile`)
#   3  verify:    every setting is read back (PASS/FAIL table). A FAIL row stops the run: no sudo.
#   4  sudo ONCE: accounts, installer (--deploy-user evedeploy: WEB_ENABLED + the PAUSE/floor/blocklist entrypoint), [calendar adapter], [today's gateway deploy], [today's web deploy], PAUSE + poller + state, [rehearsal], poller loaded, install table
#   5  clean-up:  local gh logout; links to revoke the OAuth token AND the temporary token; you type "revoked"; both are PROVEN dead with an HTTP 401. Not dead = FAIL (non-zero exit).
# THE TEMPORARY TOKEN (the fine-grained token that opened the PR): conditions are fixed: repositories eve-serving and eve-web only; Contents and Pull requests write only; NO Administration;
# expiry 1 day; used from the environment only. This script NEVER changes anything with it. If EVE_TEMP_TOKEN is set when the script starts it is used for ONE read: it must be REFUSED on an
# admin-only endpoint (a token that can read admin settings violates the conditions and stops the run before any change). At the end it is read again (EVE_TEMP_TOKEN or a hidden prompt)
# only to prove, with a 401, that it is revoked. It is never printed, logged or put on a command line (sent to curl through stdin).
# Bash 3.2 / BSD tools compatible. Needs: curl, shasum, sudo, jq, gh, git. No secret is ever printed (xtrace, SHELLOPTS, GH_DEBUG are switched off first; error text is masked).
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set +x +v; unset BASH_XTRACEFD GH_DEBUG DEBUG 2>/dev/null || true; export -n SHELLOPTS 2>/dev/null || true; unset SHELLOPTS 2>/dev/null || true
set -euo pipefail
# the temporary token is taken out of the environment at once: no child process (gh, git, curl, sudo) ever inherits it
TEMP_TOKEN=${EVE_TEMP_TOKEN-}; unset EVE_TEMP_TOKEN 2>/dev/null || true

# ---------- pins and constants (pin-unattended.sh --oneshot rewrites the four sha256 lines; the tests check them against the files). No commit is pinned anywhere: the merge commit is an ARGUMENT ----------
RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
PIN_INSTALLER_SHA256=a198272686d1be9a46e53656c0539be254a021c5602c25e13203d9c5a6f39d26   # deploy/nopasswd/setup-deploy-nopasswd.sh
PIN_UNATTENDED_SHA256=6e946c5d18547f629e0eda8340103e32693579c38a33f0d500066d130abe12c1  # deploy/nopasswd/setup-unattended.sh
PIN_PIMADAPTER_SHA256=31a080e1fbed4f44099bd9b4cc9df990144fbb560e2359202cedf3b016b758fc  # deploy/nopasswd/setup-pim-adapter.sh (fetched and run only with --calendar-adapter)
PIN_SETTINGS_SHA256=9fc392df1c57b0df2637f5db42010340db389671e3cd10154558b6081a4a102e    # deploy/nopasswd/settings/SHA256SUMS (which pins every settings file)
GH_USER=gun025bba
REPOS="gun025bba/eve-serving gun025bba/eve-web"
IOS_REPO=gun025bba/eve-ios   # only main-protect (required check "compile") is applied here: no release tags, no deploy environments, no deploy keys
TTY=${ONESHOT_TTY:-/dev/tty}
API_VERSION=2022-11-28
API_VERSION_ALT=2026-03-10
OAUTH_URL=https://github.com/settings/applications
PAT_URL=https://github.com/settings/personal-access-tokens
SETTINGS_FILES="release-tags.json release-tags.nokey.json release-tags-lock.json main-protect.eve-serving.json main-protect.eve-web.json main-protect.eve-ios.json release-branches.eve-serving.json env-deploy.json env-deploy-major.json security-and-analysis.json actions-workflow.json actions-fork-approval.json"

DRY=0; MERGE=""; PIM_VER=""; GW_VER=""; WEB_VER=""; REH_KIND=""; REH_VER=""; UNPAUSE=0; SETTINGS_ONLY=0; TTY_FD=0
HERE=$(cd "$(dirname "$0")" && pwd)
GHDIR=""; WORKD=""; FAILS=0; TOKEN=""; LOGIN_OK=0; REVOKE_DONE=0; RV_OK=0; REVOKE_RESULT="not run"; OWNER_ID=""; SETTINGS=$HERE/settings; SU_TEXT=""; INST_TEXT=""; PIM_TEXT=""

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die()  { printf 'eve-oneshot: %s\n' "$*" >&2; exit 1; }
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1; else sha256sum | cut -d' ' -f1; fi; }
need() { local c; for c in "$@"; do command -v "$c" >/dev/null 2>&1 || { if [ "$DRY" = 1 ]; then say "  (dry-run) missing tool: $c"; else die "missing tool: $c"; fi; }; done; }
is_ver() { [[ "$1" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]]; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1;;
    --merge-commit) MERGE=${2:-}; shift;;
    --calendar-adapter) PIM_VER=${2:-}; shift;;
    --gateway-deploy) GW_VER=${2:-}; shift;;
    --web-deploy) WEB_VER=${2:-}; shift;;
    --rehearse) REH_KIND=${2:-}; REH_VER=${3:-}; shift 2;;
    --unpause-after-pass) UNPAUSE=1;;
    --settings-only) SETTINGS_ONLY=1;;
    -h|--help) say "usage: eve-oneshot.sh [--dry-run] --merge-commit SHA [--calendar-adapter X.Y.Z] [--gateway-deploy X.Y.Z] [--web-deploy X.Y.Z] [--rehearse gateway|web X.Y.Z [--unpause-after-pass]] [--settings-only]   (see the comment block at the top)"; exit 0;;
    *) die "unknown argument: $1 (see --help)";;
  esac
  shift
done
[ -z "$PIM_VER" ] || is_ver "$PIM_VER" || die "--calendar-adapter needs X.Y.Z"
[ -z "$GW_VER" ] || is_ver "$GW_VER" || die "--gateway-deploy needs X.Y.Z"
[ -z "$WEB_VER" ] || is_ver "$WEB_VER" || die "--web-deploy needs X.Y.Z"
if [ -n "$REH_KIND" ]; then case "$REH_KIND" in gateway|web) ;; *) die "--rehearse KIND must be gateway or web";; esac; is_ver "$REH_VER" || die "--rehearse needs KIND X.Y.Z"; fi
[ "$UNPAUSE" = 0 ] || [ -n "$REH_KIND" ] || die "--unpause-after-pass needs --rehearse"
[ "$DRY" = 1 ] || [[ "$MERGE" =~ ^[0-9a-f]{40}$ ]] || die "--merge-commit needs the full 40-hex sha of the merge commit on main"

# ---------- clean-up: the owner revokes BOTH tokens, then both are proven dead ----------
owner_of() { stat -c %u "$1" 2>/dev/null || stat -f %u "$1"; }
tty_open() { if [ "$TTY_FD" = 0 ]; then if { exec 3<"$TTY"; } 2>/dev/null; then TTY_FD=1; else TTY_FD=-1; fi; fi; [ "$TTY_FD" = 1 ]; }
tty_close() { if [ "$TTY_FD" = 1 ]; then exec 3<&-; fi; TTY_FD=0; }
ask_word() { # PROMPT WORD
  local ans="" tries=0
  tty_open || return 1
  while [ "$tries" -lt 3 ]; do
    printf '%s' "$1"
    if read -r ans <&3; then :; else printf '\n'; return 1; fi
    if [ "$ans" = "$2" ]; then return 0; fi
    tries=$((tries + 1))
  done
  return 1
}
http_code_with() { # $1 token (sent through stdin, never in a command line or the environment)
  local out
  if out=$(printf 'Authorization: Bearer %s\n' "$1" | curl -sS -o /dev/null -w '%{http_code}' -m 30 --proto '=https' --tlsv1.2 -H @- -H 'Accept: application/vnd.github+json' -H "X-GitHub-Api-Version: $API_VERSION" https://api.github.com/user 2>/dev/null); then
    case "$out" in 401) echo 401;; 200) echo 200;; *) echo other;; esac
  else echo other; fi
}
TEMP_RESULT="not run"
revoke_flow() {
  local confirmed=no code=other pat="" pcode=unproven
  REVOKE_DONE=1
  printf '\n== 5. revoke BOTH tokens (gh auth logout only deleted the LOCAL copy of the OAuth token)\n'
  if [ "$LOGIN_OK" = 1 ]; then printf '   1) OAuth login of this run:  %s  -> "Authorized OAuth Apps" -> "GitHub CLI" -> Revoke\n' "$OAUTH_URL"; fi
  printf '   2) the temporary fine-grained token (the one that opened the PR):  %s  -> that token -> Delete\n' "$PAT_URL"
  if tty_open; then if ask_word '   Type "revoked" and press Enter once BOTH are revoked: ' revoked; then confirmed=yes; fi
  else printf '   (no terminal to ask on: not confirmed)\n'; fi
  if [ -n "$TOKEN" ]; then code=$(http_code_with "$TOKEN"); elif [ "$LOGIN_OK" = 0 ]; then code=401; else code=unproven; printf '   (the OAuth token of this login could not be read: its revocation cannot be proven here)\n'; fi
  TOKEN=""
  REVOKE_RESULT="confirmed=$confirmed http=$code"
  pat=$TEMP_TOKEN; TEMP_TOKEN=""
  if [ -z "$pat" ] && [ "$DRY" = 0 ] && tty_open; then printf '   Paste the temporary token to PROVE it is dead (input is hidden; empty = cannot prove = FAIL): '; read -r -s pat <&3 || pat=""; printf '\n'; fi
  if [ -n "$pat" ]; then pcode=$(http_code_with "$pat"); fi
  pat=""
  TEMP_RESULT="http=$pcode"
  tty_close
  if [ "$confirmed" = yes ] && [ "$code" = 401 ] && [ "$pcode" = 401 ]; then RV_OK=1; say "   both tokens are dead (GitHub answered 401 for each): OK"
  else printf '\n   !!!!  WARNING: a token of this run may STILL BE VALID (oauth: %s, temporary: %s).  Revoke it NOW at the links above.  !!!!\n' "$REVOKE_RESULT" "$TEMP_RESULT"; fi
}
teardown() { # idempotent: local logout + delete the throwaway dir; then the revoke flow once
  if [ -n "$GHDIR" ] && [ -d "$GHDIR" ]; then GH_CONFIG_DIR=$GHDIR gh auth logout --hostname github.com >/dev/null 2>&1 || true; rm -rf "$GHDIR"; fi
  GHDIR=""
  if [ "$DRY" = 0 ] && [ "$REVOKE_DONE" = 0 ]; then revoke_flow; fi
}
cleanup() {
  local rc=$?
  trap - EXIT INT TERM HUP
  set +e
  teardown
  tty_close
  [ -z "$WORKD" ] || rm -rf "$WORKD"
  if [ "$DRY" = 0 ] && [ "$RV_OK" != 1 ] && [ "$rc" = 0 ]; then rc=1; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

# ---------- GitHub API helpers (the throwaway OAuth login only) ----------
ghq() { gh api -H "X-GitHub-Api-Version: $API_VERSION" "$@"; }
redact() { local t; t=$(cat; printf x); t=${t%x}; if [ -n "$TOKEN" ]; then t=${t//"$TOKEN"/[redacted]}; fi; printf '%s' "$t" | sed -E 's/(gh[pousr]_|github_pa[t]_)[A-Za-z0-9_]+/[redacted]/g'; }
errline() { head -c 300 | tr '\n' ' ' | redact; }
gw() { # METHOD ENDPOINT [JSON]: the body goes through stdin; the call is retried once with API_VERSION_ALT unless it is a POST
  local m=$1 ep=$2 body=${3-} v err="" vers=$API_VERSION
  if [ "$DRY" = 1 ]; then say "  DRY-RUN $m $ep $body"; return 0; fi
  if [ "$m" != POST ]; then vers="$API_VERSION $API_VERSION_ALT"; fi
  for v in $vers; do
    if [ -n "$body" ]; then
      if err=$(printf '%s' "$body" | gh api -H "X-GitHub-Api-Version: $v" -X "$m" "$ep" --input - 2>&1 >/dev/null); then return 0; fi
    elif err=$(gh api -H "X-GitHub-Api-Version: $v" -X "$m" "$ep" 2>&1 >/dev/null); then return 0; fi
    err=$(printf '%s' "$err" | errline)
  done
  printf '  API ERROR: %s %s -> %s\n' "$m" "$ep" "$err" >&2
  return 1
}
gr() { # ENDPOINT JQ [quiet]: prints the value, or ERR
  local out err="" ver ef=/dev/null
  if [ -n "$GHDIR" ] && [ -d "$GHDIR" ]; then ef=$GHDIR/gr.err; fi
  for ver in $API_VERSION $API_VERSION_ALT; do
    if out=$(gh api -H "X-GitHub-Api-Version: $ver" "$1" --jq "$2" 2>"$ef"); then printf '%s' "$out"; return 0; fi
    err=$(errline <"$ef")
  done
  if [ "${3-}" != quiet ]; then printf '  API ERROR: GET %s -> %s\n' "$1" "$err" >&2; fi
  printf 'ERR'
}
short_of() { case "$1" in */eve-serving) echo serving;; */eve-web) echo web;; *) die "unknown repo $1";; esac; }
keytitle_of() { case "$1" in */eve-serving) echo release-tagger;; */eve-web) echo eve-web-release-tagger;; esac; }
body_of() { jq -c . "$SETTINGS/$1"; }

# ---------- step 0: pins (read-only; the only network use before the login is public GitHub, no token) ----------
pub_get() { curl -fsSL --proto '=https' --tlsv1.2 -m 60 -H 'Accept: application/vnd.github+json' "$1"; }
step_pins() {
  step "0. pins: merge commit and sha256 of every file that will run as root or change a setting"
  local r f t pr
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN GET https://api.github.com/repos/{serving}/commits/<merge>, compare/<merge>...main (status identical|ahead), commits/<merge>/pulls (merged PR, base main)"
    say "  DRY-RUN download setup-deploy-nopasswd.sh, setup-unattended.sh, settings/SHA256SUMS and the settings files at <merge>; require sha256: installer=$PIN_INSTALLER_SHA256 unattended=$PIN_UNATTENDED_SHA256 settings-manifest=$PIN_SETTINGS_SHA256"
    if [ -n "$PIM_VER" ]; then say "  DRY-RUN download setup-pim-adapter.sh at <merge>; require sha256: pim-adapter=$PIN_PIMADAPTER_SHA256"; fi
    say "  DRY-RUN GET repos/{serving,web}: private must be false (the poller reads releases/latest without a token)"
    return 0
  fi
  need curl jq shasum
  r=gun025bba/eve-serving
  [ "$(pub_get "https://api.github.com/repos/$r/commits/$MERGE" | jq -r .sha)" = "$MERGE" ] || die "$MERGE is not a commit of $r"
  t=$(pub_get "https://api.github.com/repos/$r/compare/$MERGE...main" | jq -r .status)
  case "$t" in identical|ahead) ;; *) die "$MERGE is not in the history of main (compare says '$t')";; esac
  pr=$(pub_get "https://api.github.com/repos/$r/commits/$MERGE/pulls" | jq -r --arg m "$MERGE" '[.[]|select(.merged_at != null and .merge_commit_sha == $m and .base.ref == "main")]|length')
  [ "$pr" -ge 1 ] 2>/dev/null || die "$MERGE is not the merge commit of a merged pull request into main"
  for r in $REPOS $IOS_REPO; do [ "$(pub_get "https://api.github.com/repos/$r" | jq -r .private)" = false ] || die "$r is not public: the poller design needs public releases (U-10)"; done
  WORKD=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot.XXXXXX") || die "mktemp failed"; chmod 700 "$WORKD"
  SETTINGS=$WORKD/settings; mkdir "$SETTINGS"
  INST_TEXT=$(pub_get "$RAW/$MERGE/deploy/nopasswd/setup-deploy-nopasswd.sh"); [ "$(printf '%s\n' "$INST_TEXT" | sha_of)" = "$PIN_INSTALLER_SHA256" ] || die "installer sha256 does not match the pin: NOT running anything"
  SU_TEXT=$(pub_get "$RAW/$MERGE/deploy/nopasswd/setup-unattended.sh"); [ "$(printf '%s\n' "$SU_TEXT" | sha_of)" = "$PIN_UNATTENDED_SHA256" ] || die "setup-unattended.sh sha256 does not match the pin: NOT running anything"
  if [ -n "$PIM_VER" ]; then
    PIM_TEXT=$(pub_get "$RAW/$MERGE/deploy/nopasswd/setup-pim-adapter.sh")
    [ "$(printf '%s\n' "$PIM_TEXT" | sha_of)" = "$PIN_PIMADAPTER_SHA256" ] || die "setup-pim-adapter.sh sha256 does not match the pin: NOT running anything"
  fi
  pub_get "$RAW/$MERGE/deploy/nopasswd/settings/SHA256SUMS" >"$SETTINGS/SHA256SUMS"; [ "$(sha_of <"$SETTINGS/SHA256SUMS")" = "$PIN_SETTINGS_SHA256" ] || die "settings manifest sha256 does not match the pin"
  for f in $SETTINGS_FILES; do
    pub_get "$RAW/$MERGE/deploy/nopasswd/settings/$f" >"$SETTINGS/$f" || die "cannot download settings/$f"
    [ "$(sha_of <"$SETTINGS/$f")" = "$(awk -v f="$f" '$2==f {print $1}' "$SETTINGS/SHA256SUMS")" ] || die "settings/$f does not match the pinned manifest"
    jq -e . "$SETTINGS/$f" >/dev/null || die "settings/$f is not valid JSON"
  done
  say "  merge commit on main, merged PR, installer + setup-unattended$([ -z "$PIM_VER" ] || printf ' + setup-pim-adapter') + $(printf '%s' "$SETTINGS_FILES" | wc -w | tr -d ' ') settings files match their pins; the three repositories are public"
}
temp_token_precheck() { # the temporary token (if given) must NOT see admin-only settings; one read, no change
  local c pat=$TEMP_TOKEN
  step "0b. temporary token conditions (read-only check)"
  if [ "$DRY" = 1 ]; then say "  DRY-RUN if EVE_TEMP_TOKEN is set: GET repos/{serving}/actions/permissions with it must be refused (403/404): a token with Administration is rejected"; return 0; fi
  if [ -z "$pat" ]; then say "  EVE_TEMP_TOKEN not set: the conditions cannot be checked here (it is asked for at the end, to prove its revocation)"; return 0; fi
  c=$(printf 'Authorization: Bearer %s\n' "$pat" | curl -sS -o /dev/null -w '%{http_code}' -m 30 --proto '=https' --tlsv1.2 -H @- -H 'Accept: application/vnd.github+json' -H "X-GitHub-Api-Version: $API_VERSION" https://api.github.com/repos/gun025bba/eve-serving/actions/permissions 2>/dev/null || true)
  pat=""
  case "$c" in 401) die "the temporary token is already invalid (401): unset EVE_TEMP_TOKEN or use the right one";; 403|404) say "  the token is refused on an admin-only endpoint ($c): no Administration, as required";; *) die "the temporary token can read admin-only settings (HTTP $c): it violates the conditions. Revoke it, make a new one without Administration, and re-run";; esac
}

# ---------- step 1: login ----------
git_ok() { local v maj min; v=$(git --version 2>/dev/null | sed -n 's/^git version \([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p'); [ -n "$v" ] || return 1; read -r maj min <<EOF2
$v
EOF2
  [ "$maj" -gt 2 ] || { [ "$maj" -eq 2 ] && [ "$min" -ge 32 ]; }; }
step_login() {
  step "1. GitHub login (throwaway gh config, browser)"
  local who r admin
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN GH_CONFIG_DIR=<mktemp -d, 0700> gh auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage (GH_TOKEN, GITHUB_TOKEN and EVE_TEMP_TOKEN are NOT used for it)"
    say "  DRY-RUN require: login == $GH_USER and admin on each of: $REPOS $IOS_REPO"
    return 0
  fi
  need gh git curl
  git_ok || die "git >= 2.32 is required"
  GHDIR=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-gh.XXXXXX") || die "cannot create the temp dir"; chmod 700 "$GHDIR"
  export GH_CONFIG_DIR=$GHDIR GIT_CONFIG_GLOBAL=$GHDIR/gitconfig GIT_CONFIG_NOSYSTEM=1 GH_PAGER=cat GH_NO_UPDATE_NOTIFIER=1 NO_COLOR=1
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
  gh auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage || die "gh login failed"
  LOGIN_OK=1
  TOKEN=$(gh auth token --hostname github.com 2>/dev/null) || TOKEN=""
  [ -n "$TOKEN" ] || die "cannot read the token of this login (needed to prove its revocation at the end)"
  who=$(ghq user --jq .login) || die "cannot read the logged-in user"
  [ "$who" = "$GH_USER" ] || die "logged in as '$who', not $GH_USER: aborting (nothing was changed)"
  OWNER_ID=$(ghq user --jq .id); [[ "$OWNER_ID" =~ ^[0-9]+$ ]] || die "cannot read the user id"
  for r in $REPOS $IOS_REPO; do admin=$(ghq "repos/$r" --jq .permissions.admin) || die "cannot read $r"; [ "$admin" = true ] || die "$GH_USER is not an admin of $r: aborting (nothing was changed)"; done
  say "  logged in as $who, admin of all three repositories"
}

# ---------- step 2: settings ----------
preflight_keys() { # $1 repo: READ-ONLY. A write deploy key other than the expected one would inherit the DeployKey bypass: abort with no change. Sets KEYMODE_<short> to key|nokey.
  local r=$1 s title rows id rk t foreign=0 ours=0
  s=$(short_of "$r"); title=$(keytitle_of "$r")
  if [ "$DRY" = 1 ]; then say "  DRY-RUN list deploy keys of $r; abort if a write key other than '$title' exists; DeployKey bypass only if exactly one write key '$title' exists"; eval "KEYMODE_$s=key"; return 0; fi
  rows=$(ghq "repos/$r/keys?per_page=100" --jq '.[] | "\(.id)\t\(.read_only)\t\(.title)\t\(.key)"') || die "cannot list the deploy keys of $r: aborting (nothing was changed)"
  while IFS=$'\t' read -r id rk t _; do
    [ -n "${id:-}" ] || continue
    if [ "$rk" = false ]; then if [ "$t" = "$title" ]; then ours=$((ours + 1)); else foreign=$((foreign + 1)); say "  FOREIGN WRITE deploy key on $r: id $id, title '$t'"; fi; fi
  done <<EOF2
$rows
EOF2
  [ "$foreign" = 0 ] || die "$foreign write deploy key(s) other than '$title' on $r: the DeployKey bypass would cover them. Aborting with NO change; delete them or make them read-only, then re-run"
  [ "$ours" -le 1 ] || die "$ours write deploy keys titled '$title' on $r: aborting with no change"
  if [ "$ours" = 1 ]; then eval "KEYMODE_$s=key"; say "  $r: exactly one write deploy key '$title': release-tags keeps the DeployKey bypass"
  else eval "KEYMODE_$s=nokey"; say "  $r: NO write deploy key '$title': release-tags is applied WITHOUT the DeployKey bypass (admin only) and the table shows it"; fi
}
upsert_ruleset() { # REPO NAME BODY
  local r=$1 name=$2 body=$3 ids n
  if [ "$DRY" = 1 ]; then say "  DRY-RUN create-or-update ruleset '$name' on $r: $body"; return 0; fi
  ids=$(ghq "repos/$r/rulesets?per_page=100" --jq ".[] | select(.name==\"$name\") | .id")
  n=$(printf '%s' "$ids" | grep -c . || true)
  case "$n" in
    0) say "  ruleset $name: creating"; gw POST "repos/$r/rulesets" "$body";;
    1) say "  ruleset $name: updating (id $ids)"; gw PUT "repos/$r/rulesets/$ids" "$body";;
    *) die "ruleset name '$name' exists $n times on $r: remove the duplicates by hand and re-run";;
  esac
}
sync_env_policies() { # REPO ENV: exactly one deployment policy, tag v*
  local r=$1 e=$2 rows id t n nt=0
  if [ "$DRY" = 1 ]; then gw POST "repos/$r/environments/$e/deployment-branch-policies" '{"name":"v*","type":"tag"}'; return 0; fi
  rows=$(ghq "repos/$r/environments/$e/deployment-branch-policies?per_page=100" --jq '.branch_policies[] | "\(.id) \(.type) \(.name)"')
  while read -r id t n; do
    [ -n "${id:-}" ] || continue
    if [ "$t" = tag ] && [ "$n" = 'v*' ] && [ "$nt" = 0 ]; then nt=1; continue; fi
    say "  removing other deployment policy $id ($t $n)"; gw DELETE "repos/$r/environments/$e/deployment-branch-policies/$id"
  done <<EOF2
$rows
EOF2
  if [ "$nt" = 0 ]; then gw POST "repos/$r/environments/$e/deployment-branch-policies" '{"name":"v*","type":"tag"}'; fi
}
apply_repo() {
  local r=$1 s cur major
  s=$(short_of "$r")
  say "  -- $r"
  if [ "$(eval "echo \$KEYMODE_$s")" = key ]; then upsert_ruleset "$r" release-tags "$(body_of release-tags.json)"; else upsert_ruleset "$r" release-tags "$(body_of release-tags.nokey.json)"; fi
  upsert_ruleset "$r" main-protect "$(body_of "main-protect.$([ "$s" = serving ] && echo eve-serving || echo eve-web).json")"
  if [ "$s" = serving ]; then upsert_ruleset "$r" release-branches "$(body_of release-branches.eve-serving.json)"; fi   # refs/heads/release/*: no creation/deletion/force-push except admin bypass; PR + test + deploy-nopasswd-macos
  gw PUT "repos/$r/environments/deploy" "$(body_of env-deploy.json)" || say "  warning: environment deploy PUT failed; the table decides"
  sync_env_policies "$r" deploy || say "  warning: policies of deploy not synced; the table decides"
  major=$(jq -c --argjson id "${OWNER_ID:-0}" '.reviewers[0].id = $id' "$SETTINGS/env-deploy-major.json")
  gw PUT "repos/$r/environments/deploy-major" "$major" || say "  warning: environment deploy-major PUT failed; the table decides"
  sync_env_policies "$r" deploy-major || say "  warning: policies of deploy-major not synced; the table decides"
  gw PATCH "repos/$r" "$(body_of security-and-analysis.json)" || say "  warning: secret scanning PATCH failed; the table decides"
  if [ "$DRY" = 1 ] || [ "$(gr "repos/$r/immutable-releases" .enabled quiet)" != true ]; then gw PUT "repos/$r/immutable-releases"; else say "  immutable releases: already on"; fi
  if [ "$DRY" = 1 ]; then gw PUT "repos/$r/actions/permissions" '{"enabled":true,"allowed_actions":"<current>","sha_pinning_required":true}'
  else
    cur=$(gr "repos/$r/actions/permissions" .allowed_actions)
    case "$cur" in all|local_only|selected) gw PUT "repos/$r/actions/permissions" '{"enabled":true,"allowed_actions":"'"$cur"'","sha_pinning_required":true}' || say "  FAILED; the table decides";; *) say "  FAILED: cannot read allowed_actions of $r (got '$cur'); SHA pinning NOT changed";; esac
  fi
  gw PUT "repos/$r/actions/permissions/workflow" "$(body_of actions-workflow.json)" || say "  FAILED; the table decides"
  gw PUT "repos/$r/actions/permissions/fork-pr-contributor-approval" "$(body_of actions-fork-approval.json)" || say "  FAILED; the table decides"
}
apply_ios() { # eve-ios: ONLY the main-protect ruleset (required status check "compile" from the Actions app, PR required, 0 approvals, squash/rebase, no force-push, no deletion)
  say "  -- $IOS_REPO (main-protect only)"
  upsert_ruleset "$IOS_REPO" main-protect "$(body_of main-protect.eve-ios.json)"
}
step_settings() {
  local r
  step "2. settings of the repositories (idempotent; applied ONLY with the throwaway OAuth login, never with the temporary token)"
  for r in $REPOS; do preflight_keys "$r"; done
  # FIRST of all writes (before release-tags is rewritten and before any PUT of another setting): the lock on tags v* (no update, delete or force-push), so that a failure later in the run can never leave the tags unprotected
  for r in $REPOS; do upsert_ruleset "$r" release-tags-lock "$(body_of release-tags-lock.json)"; done
  for r in $REPOS; do apply_repo "$r"; done
  apply_ios
}

# ---------- step 3: verify ----------
chk() { if [ "$2" = "$3" ]; then printf '  PASS  %s: %s\n' "$1" "$3"; else printf '  FAIL  %s: got [%s] want [%s]\n' "$1" "$3" "$2"; FAILS=$((FAILS + 1)); fi; }
RS_SUM='"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[]|if .type=="pull_request" then "pull_request(\(.parameters.required_approving_review_count),codeowner=\(.parameters.require_code_owner_review),dismiss_stale=\(.parameters.dismiss_stale_reviews_on_push),threads=\(.parameters.required_review_thread_resolution),extra=\(.parameters.require_extra_approval_for_unattributed_changes),methods=\([.parameters.allowed_merge_methods[]?]|sort|join("+")))" elif .type=="required_status_checks" then "checks(\(.parameters.strict_required_status_checks_policy):\([.parameters.required_status_checks[]|"\(.context)@\(.integration_id)"]|sort|join("+")))" else .type end]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"'
ENV_SUM='"wait=\(([.protection_rules[]?|select(.type=="wait_timer")|.wait_timer]|.[0]) // 0) admins_bypass=\(.can_admins_bypass) custom=\(.deployment_branch_policy.custom_branch_policies) reviewers=\([.protection_rules[]?|select(.type=="required_reviewers")|.reviewers[]?|"\(.type):\(.reviewer.id)"]|sort|join(","))"'
rs_check() { # REPO NAME FILE
  local id g want
  id=$(gr "repos/$1/rulesets?per_page=100" ".[] | select(.name==\"$2\") | .id")
  g=$(gr "repos/$1/rulesets/$id" "$RS_SUM")
  want=$(jq -r "$RS_SUM" "$SETTINGS/$3")
  chk "$1 ruleset $2" "$want" "$g"
}
verify_repo() {
  local r=$1 s g
  s=$(short_of "$r")
  if [ "$(eval "echo \$KEYMODE_$s")" = key ]; then rs_check "$r" release-tags release-tags.json; else rs_check "$r" release-tags release-tags.nokey.json; chk "$r DeployKey bypass present (tagging with the key works)" key nokey; fi
  rs_check "$r" release-tags-lock release-tags-lock.json
  rs_check "$r" main-protect "main-protect.$([ "$s" = serving ] && echo eve-serving || echo eve-web).json"
  if [ "$s" = serving ]; then rs_check "$r" release-branches release-branches.eve-serving.json; fi
  chk "$r environment deploy" "wait=$(jq -r .wait_timer "$SETTINGS/env-deploy.json") admins_bypass=false custom=true reviewers=" "$(gr "repos/$r/environments/deploy" "$ENV_SUM")"
  chk "$r environment deploy policies" "tag:v*" "$(gr "repos/$r/environments/deploy/deployment-branch-policies?per_page=100" '[.branch_policies[]|"\(.type):\(.name)"]|sort|join(",")')"
  chk "$r environment deploy-major" "wait=0 admins_bypass=false custom=true reviewers=User:$OWNER_ID" "$(gr "repos/$r/environments/deploy-major" "$ENV_SUM")"
  chk "$r environment deploy-major policies" "tag:v*" "$(gr "repos/$r/environments/deploy-major/deployment-branch-policies?per_page=100" '[.branch_policies[]|"\(.type):\(.name)"]|sort|join(",")')"
  chk "$r secret scanning + push protection" "enabled enabled" "$(gr "repos/$r" '"\(.security_and_analysis.secret_scanning.status) \(.security_and_analysis.secret_scanning_push_protection.status)"')"
  chk "$r immutable releases" true "$(gr "repos/$r/immutable-releases" .enabled)"
  chk "$r Actions SHA pinning required" "true true" "$(gr "repos/$r/actions/permissions" '"\(.sha_pinning_required) \(.enabled)"')"
  chk "$r default workflow permissions" "read false" "$(gr "repos/$r/actions/permissions/workflow" '"\(.default_workflow_permissions) \(.can_approve_pull_request_reviews)"')"
  chk "$r fork PR approval" all_external_contributors "$(gr "repos/$r/actions/permissions/fork-pr-contributor-approval" .approval_policy)"
  g=$(gr "repos/$r/keys?per_page=100" '[.[]|select(.read_only==false)|.title]|sort|join(",")')
  if [ "$(eval "echo \$KEYMODE_$s")" = key ]; then chk "$r write deploy keys (only the tagger)" "$(keytitle_of "$r")" "$g"; else chk "$r write deploy keys (none)" "" "$g"; fi
}
verify_ios() { rs_check "$IOS_REPO" main-protect main-protect.eve-ios.json; }
step_verify() {
  local r
  step "3. verify (read back from GitHub)"
  if [ "$DRY" = 1 ]; then say "  DRY-RUN read back: rulesets (release-tags, release-tags-lock, main-protect, [eve-serving only] release-branches; eve-ios: main-protect with check compile@15368), environments deploy (wait timer, no admin bypass, tag v*) and deploy-major (reviewer = you), secret scanning + push protection, immutable releases, 3 Actions settings, write deploy keys; compared with the pinned JSON files"; return 0; fi
  for r in $REPOS; do verify_repo "$r"; done
  verify_ios
  if [ "$FAILS" != 0 ]; then say "  $FAILS CHECK(S) FAILED: the sudo step is NOT run. Fix the cause (see the FAIL and API ERROR lines) and re-run; the run is idempotent."; return 1; fi
  say "  ALL SETTINGS CHECKS PASSED"
}

# ---------- step 4: the one sudo session ----------
# Runs as root: only checked texts and values that were validated above. $1 = setup-unattended text, $2 = installer text, $3 web version or "-", $4 rehearsal kind or "-", $5 rehearsal version, $6 unpause 0|1,
# $7 = the verified main merge commit (both scripts download only from it, and trust only their pinned sha256), $8 = the checked setup-pim-adapter text or "-", $9 = its release version or "-", ${10} = gateway version to deploy or "-". The installer creates PAUSE and the floors BEFORE the sudoers rule goes live.
# shellcheck disable=SC2016  # the session text is meant to be passed literally to the root shell
ROOT_SESSION='set -e
SU=$1; INST=$2; WEB=$3; RK=$4; RV=$5; UP=$6; MC=$7; PIM=$8; PV=$9; GWV=${10}
su_() { /bin/bash -c "$SU" setup-unattended --src-commit "$MC" "$@"; }
su_ accounts
/bin/bash -c "$INST" setup-deploy-nopasswd --src-commit "$MC" --pause --deploy-user evedeploy
if [ "$PV" != - ]; then /bin/bash -c "$PIM" setup-pim-adapter --version "$PV"; fi
if [ "$GWV" != - ]; then su_ deploy gateway "$GWV"; fi
if [ "$WEB" != - ]; then su_ deploy web "$WEB"; fi
su_ state
if [ "$RK" != - ]; then su_ rehearse "$RK" "$RV"; if [ "$UP" = 1 ]; then su_ unpause; fi; fi
su_ load
su_ verify'
step_sudo() {
  step "4. install (sudo, ONE password prompt): accounts, entrypoint with PAUSE/floor/blocklist and WEB_ENABLED, poller, PAUSE"
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN sudo /bin/bash -c '<session>' _ <setup-unattended text> <installer text> ${WEB_VER:--} ${REH_KIND:--} ${REH_VER:--} $UNPAUSE ${MERGE:-<merge>} <setup-pim-adapter text or -> ${PIM_VER:--} ${GW_VER:--}   with the session:"
    printf '%s\n' "$ROOT_SESSION" | sed 's/^/    | /'
    return 0
  fi
  need sudo
  say "  checked texts, one sudo call (it asks for your password once)"
  sudo /bin/bash -c "$ROOT_SESSION" _ "$SU_TEXT" "$INST_TEXT" "${WEB_VER:--}" "${REH_KIND:--}" "${REH_VER:--}" "$UNPAUSE" "$MERGE" "${PIM_TEXT:--}" "${PIM_VER:--}" "${GW_VER:--}" || { SUDO_RC=$?; FAILS=$((FAILS + 1)); die "the sudo session failed (exit $SUDO_RC): read the output above; the run is idempotent"; }
}

main() {
  if [ "$DRY" = 1 ]; then say "DRY-RUN: nothing is changed, no login, no sudo, no network"; fi
  step_pins
  temp_token_precheck
  step_login
  step_settings
  step_verify || die "settings table has FAIL rows: stopped before sudo"
  if [ "$SETTINGS_ONLY" = 1 ]; then say "(--settings-only: no sudo step)"; else
    if [ "$DRY" = 0 ]; then teardown_local_only; fi
    step_sudo
  fi
  if [ "$DRY" = 1 ]; then say; say "DRY-RUN done. A real run ends by making you revoke the OAuth token and the temporary token and proving both dead with HTTP 401."; fi
}
teardown_local_only() { # the local copy of the login is gone before sudo runs; the token stays in memory for the 401 proof
  if [ -n "$GHDIR" ] && [ -d "$GHDIR" ]; then GH_CONFIG_DIR=$GHDIR gh auth logout --hostname github.com >/dev/null 2>&1 || true; rm -rf "$GHDIR"; fi
  GHDIR=""
}
main
