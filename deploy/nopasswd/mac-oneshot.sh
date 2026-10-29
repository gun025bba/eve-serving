#!/bin/bash
# mac-oneshot.sh: the one-time setup of the passwordless deploy, run ONCE by the owner on the Mac (tonight); it can be re-run safely.
#
#   clone-free (the pinned one-liner is in deploy/nopasswd/README.md):  s=$(curl ... mac-oneshot.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = <sha256> ] && bash -c "$s" mac-oneshot --src-commit <main commit>
#   from a clone:   bash deploy/nopasswd/mac-oneshot.sh --src-commit <main commit> [--dry-run] [--skip-install] [--skip-github] [--only-fix]      (bash, not zsh)
#
#   --dry-run       prints every step and every GitHub API call it WOULD make; changes nothing, asks nothing, needs no login, no sudo, no network
#   --skip-install  skip step 1;   --skip-github  skip the GitHub steps (login, eve-web settings, deploy-key registration, ssh test, verify)
#   --only-fix      the repair run after a first run whose verify table had FAIL rows: NO sudo install, the key under ~/.eve-web-release-tagger is only READ (never created, changed or chmod-ed),
#                   environment `deploy`, `release-tags-lock`, `main-protect` and immutable releases are NOT re-applied (they are only read back); it logs in again (temporary), checks for foreign write deploy keys (M-1),
#                   gets the existing public key registered as the write deploy key (step 3b), checks for foreign write keys AGAIN (M-1b), re-applies ONLY `release-tags` (its DeployKey bypass depends on
#                   whether our key is registered), applies the three Actions settings, runs ssh -T, logs out, makes you revoke the token (401 proof) and prints the whole verify table.
#
# What it does, in this order (the only things you type: your sudo password once, the GitHub login in the browser, and "revoked" at the end):
#   0. KEY, local, FIRST: generates ~/.eve-web-release-tagger/id_ed25519 (ed25519, no passphrase, dir 0700, key 0600) unless it exists (an existing key is kept, but only if it and its dir are
#      owned by you and not accessible to others), so a problem with an existing key stops everything before sudo or any GitHub change. The private key is never printed, copied or sent.
#   1. INSTALL (sudo): downloads the installer from the main commit given with --src-commit, refuses unless its sha256 matches the pin below, and pipes exactly the checked text to `sudo /bin/bash -s`
#      (the same thing as the README line; the installer gets the same --src-commit). kind=web is accepted by the entrypoint (WEB_ENABLED=yes in eve-deploy) once the installer has been re-run with these pins.
#      Skipped if /usr/local/libexec/eve/deploy already has the pinned bytes.
#   2. GITHUB: `gh auth login --web` as gun025bba in a THROWAWAY gh config dir (token stored only there, git credential config redirected there; your own gh login is untouched).
#      Aborts unless `gh api user` says gun025bba and that account is admin of gun025bba/eve-web.
#      PREFLIGHT (read-only, before ANY change): lists the deploy keys of eve-web; if a WRITE key other than ours (same public key or same title eve-web-release-tagger) exists, it aborts with no
#      change at all, because the DeployKey bypass below applies to EVERY write deploy key of the repository.
#      Then gets our public key registered as a WRITE deploy key `eve-web-release-tagger` (a stale key with our title, or a read-only copy of our key, is replaced; the new key is added by you in the
#      BROWSER, see 3b: a deploy key created through the API with this OAuth token is DELETED by GitHub when the token is revoked at the end). M-1b: right after you added it the
#      deploy keys are read AGAIN and any write key other than this public key aborts the run before a ruleset is touched. If our key is NOT registered, `release-tags` is applied
#      WITHOUT the DeployKey bypass (admin bypass only). Then, idempotently
#      (create or update; safe to re-run), on gun025bba/eve-web:
#        - ruleset `release-tags` on refs/tags/v*: only CREATING a v* tag is blocked, with bypass = repository admin role + the DeployKey actor (any write deploy key, see above)
#        - ruleset `release-tags-lock` on refs/tags/v*: update/delete/non-fast-forward blocked; bypass = repository admin role ONLY (the deploy key can create a tag, never move or delete one)
#        - ruleset `main-protect` on the default branch: no delete, no force-push, linear history, required check `test` (strict)
#        - environment `deploy`: only v* tags may deploy, admins cannot bypass (whether GitHub applied it is decided by the verify table, not by the PUT's status)
#        - immutable releases ON; Actions SHA pinning required; default GITHUB_TOKEN permission read (Actions may not approve PRs); fork PR workflows need approval from every external contributor
#   3. TEST: `ssh -T` with the new key against GitHub's published host keys (deploy/nopasswd/github_known_hosts: the local copy if its sha256 matches the pin, otherwise downloaded from the
#      pinned commit and checked; StrictHostKeyChecking=yes).
#   4. VERIFY: reads every setting back and prints a PASS/FAIL table. Exit status is non-zero if anything is not as intended.
#   5. REVOKE: `gh auth logout` only deletes the LOCAL copy of the OAuth token; GitHub keeps honouring it. So the script logs out, deletes the throwaway dir, prints
#      https://github.com/settings/applications (Authorized OAuth Apps, "GitHub CLI", Revoke), waits for you to type "revoked", and then PROVES the token is dead by calling `gh api user`
#      with it (sent to curl through stdin, never in the environment or on a command line) and expecting HTTP 401 (a FAIL row and a non-zero exit otherwise). The revoke instructions are shown
#      even if the token could not be read (the revocation is then reported as unproven). This also runs when the script stops early (error, Ctrl-C) once a login exists.
#      Not covered: SIGKILL (kill -9) or a power loss cannot run any cleanup: the throwaway dir under $TMPDIR (eve-oneshot-gh.*, mode 0700) then stays with the token inside. Delete it by hand
#      and revoke the authorization at the link above.
#
# Bash 3.2 / BSD tools compatible. Needs: curl, shasum, sudo, git >= 2.32, gh, ssh, ssh-keygen. It uses `gh api --jq` only (no jq needed).
# Meant to be read before it is run: every pin is a constant right below. No secret is ever printed or logged (xtrace, SHELLOPTS, GH_DEBUG and DEBUG are switched off first; error text is masked).
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
# N-1: the very first commands. An xtrace inherited from the caller (the x option, SHELLOPTS=xtrace, BASH_XTRACEFD) would print every command with the OAuth token to stderr, and GH_DEBUG / DEBUG make gh
# log requests: switch all of it off before anything else runs. SHELLOPTS is read-only inside bash, so it is only removed from the environment of the children.
set +x +v; unset BASH_XTRACEFD GH_DEBUG DEBUG 2>/dev/null || true; export -n SHELLOPTS 2>/dev/null || true; unset SHELLOPTS 2>/dev/null || true
set -euo pipefail

# ---------- pins and constants (the tests check these against the files in the repository) ----------
INSTALL_RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
SRC_COMMIT=""                                             # from --src-commit: ONE commit on main that the installer and github_known_hosts are downloaded from; both are trusted only by the sha256 below
INSTALL_SHA256=a198272686d1be9a46e53656c0539be254a021c5602c25e13203d9c5a6f39d26
ENTRY_SHA256=5cc95f749dbe4b441d3858f5749d3698c8be0772a0cf7f4c1fe4f6f251d2a10a   # sha256 of the installed /usr/local/libexec/eve/deploy (SHA_DEPLOY in the installer)
KNOWN_HOSTS_SHA256=f455f6ec65126db336c18d44d7c73b8066e6c91e9529a92dea2712cd1552bfee
LIBEXEC=${ONESHOT_LIBEXEC:-/usr/local/libexec/eve}       # only decides whether step 1 can be skipped
GH_USER=gun025bba
WEB_REPO=gun025bba/eve-web
KEY_TITLE=eve-web-release-tagger
KEY_DIR=$HOME/.eve-web-release-tagger
KEY=$KEY_DIR/id_ed25519
TTY=${ONESHOT_TTY:-/dev/tty}                              # where the "revoked" confirmation is read from (tests point it at a file)
API_VERSION=2022-11-28
ACTIONS_APP_ID=15368                                      # github-actions: the required check must come from Actions

API_VERSION_ALT=2026-03-10                                # retried once (not for POST) if a call fails with $API_VERSION; the error text of the failing call is always printed
KEYS_URL=https://github.com/$WEB_REPO/settings/keys/new
ACTIONS_URL=https://github.com/$WEB_REPO/settings/actions

DRY=0; DO_INSTALL=1; DO_GITHUB=1; ONLY_FIX=0; MANUAL_ACTIONS=0; MANUAL_KEY=0; TTY_FD=0
HERE=$(cd "$(dirname "$0")" && pwd)
GHDIR=""; FAILS=0; TOKEN=""; LOGIN_OK=0; REVOKE_DONE=0; REVOKE_RESULT="not run"; KHDIR=""; KH=$HERE/github_known_hosts

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die()  { printf 'mac-oneshot: %s\n' "$*" >&2; exit 1; }
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1; else sha256sum | cut -d' ' -f1; fi; }
need() {
  local c
  for c in "$@"; do
    if ! command -v "$c" >/dev/null 2>&1; then
      if [ "$DRY" = 1 ]; then say "  (dry-run) missing tool: $c"; else die "missing tool: $c"; fi
    fi
  done
}
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }   # GNU first: BSD stat rejects -c, GNU stat misreads -f

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1;;
    --skip-install) DO_INSTALL=0;;
    --skip-github) DO_GITHUB=0;;
    --only-fix) ONLY_FIX=1; DO_INSTALL=0;;
    --src-commit) SRC_COMMIT=${2:-}; shift;;
    -h|--help) say "usage: mac-oneshot.sh --src-commit <40-hex main commit> [--dry-run] [--skip-install] [--skip-github] [--only-fix]   (see the comment block at the top of the script)"; exit 0;;
    *) die "unknown argument: $1 (see --help)";;
  esac
  shift
done
[ -z "$SRC_COMMIT" ] || [[ "$SRC_COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "--src-commit needs the full 40-hex sha of a commit on main"
need_commit() { [ "$DRY" = 1 ] || [[ "$SRC_COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "--src-commit <40-hex commit on main> is required (the README line has it); nothing is downloaded without it"; }
if [ "$ONLY_FIX" = 1 ] && [ "$DO_GITHUB" = 0 ]; then die "--only-fix and --skip-github exclude each other (the fix IS the GitHub part)"; fi

# ---------- cleanup: log the throwaway login out, delete the throwaway dir, make the owner revoke the token ----------
owner_of() { stat -c %u "$1" 2>/dev/null || stat -f %u "$1"; }
tty_open() { # the terminal (a file in the tests) is opened once, as fd 3, and read by every prompt of the run
  if [ "$TTY_FD" = 0 ]; then if { exec 3<"$TTY"; } 2>/dev/null; then TTY_FD=1; else TTY_FD=-1; fi; fi
  [ "$TTY_FD" = 1 ]
}
tty_close() { if [ "$TTY_FD" = 1 ]; then exec 3<&-; fi; TTY_FD=0; }
ask_word() { # PROMPT WORD: 0 once the owner typed WORD (3 tries); 1 on a wrong word 3 times, EOF or no terminal
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
revoke_flow() { # the OAuth token is still valid on GitHub's side after `gh auth logout`: the owner revokes it, then we PROVE it is dead (401)
  local confirmed=no code=other out
  REVOKE_DONE=1
  printf '\n== 5. revoke the GitHub CLI authorization (gh auth logout only deleted the LOCAL copy of the token)\n'
  printf '   Open https://github.com/settings/applications -> "Authorized OAuth Apps" -> "GitHub CLI" -> Revoke.\n'
  if tty_open; then
    if ask_word '   Type "revoked" and press Enter once you have revoked it: ' revoked; then confirmed=yes; fi
    tty_close
  else printf '   (no terminal to ask on: not confirmed)\n'; fi
  if [ -z "$TOKEN" ]; then
    code=unproven
    printf '   (the token of this login could not be read, so its revocation cannot be proven here: check the list at that link by hand)\n'
  else
    # N-3: the token goes to curl through stdin (-H @-), never through the environment or a command line (printf is a builtin: nothing shows in ps)
    if out=$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -o /dev/null -w '%{http_code}' -m 30 --proto '=https' --tlsv1.2 -H @- -H 'Accept: application/vnd.github+json' -H "X-GitHub-Api-Version: $API_VERSION" https://api.github.com/user 2>/dev/null); then
      case "$out" in 401) code=401;; 200) code=200;; *) code=other;; esac
    else code=other; fi
  fi
  TOKEN=""
  REVOKE_RESULT="confirmed=$confirmed http=$code"
  if [ "$confirmed" = yes ] && [ "$code" = 401 ]; then say "   token is dead (GitHub answered 401): OK"
  else
    printf '\n   !!!!  WARNING: the GitHub CLI token of this run may STILL BE VALID (%s).  !!!!\n   Revoke it at https://github.com/settings/applications NOW.\n' "$REVOKE_RESULT"
  fi
}
teardown() { # idempotent: logout + delete the throwaway dir, then the revoke flow (once)
  if [ -n "$GHDIR" ] && [ -d "$GHDIR" ]; then
    GH_CONFIG_DIR=$GHDIR gh auth logout --hostname github.com >/dev/null 2>&1 || true
    rm -rf "$GHDIR"
  fi
  GHDIR=""
  if { [ -n "$TOKEN" ] || [ "$LOGIN_OK" = 1 ]; } && [ "$REVOKE_DONE" = 0 ]; then revoke_flow; fi   # N-2: also when the token could not be read
}
cleanup() {
  local rc=$?
  trap - EXIT INT TERM HUP
  set +e
  teardown
  tty_close
  [ -z "$KHDIR" ] || rm -rf "$KHDIR"
  if [ "$REVOKE_DONE" = 1 ] && [ "$REVOKE_RESULT" != "confirmed=yes http=401" ] && [ "$rc" = 0 ]; then rc=1; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

# ---------- GitHub API helpers (used only with the throwaway login) ----------
ghq() { gh api -H "X-GitHub-Api-Version: $API_VERSION" "$@"; }
redact() { # N-1: the exact value of this login's token is replaced first (whatever its format), then anything that looks like a GitHub token
  local t; t=$(cat; printf x); t=${t%x}
  if [ -n "$TOKEN" ]; then t=${t//"$TOKEN"/[redacted]}; fi
  printf '%s' "$t" | sed -E 's/(gh[pousr]_|github_pa[t]_)[A-Za-z0-9_]+/[redacted]/g'
}
errline() { head -c 300 | tr '\n' ' ' | redact; }   # the first part of gh's error text (HTTP status and GitHub's message), never a token
# write: METHOD ENDPOINT [JSON]; the JSON body goes through stdin (never on a command line). On failure the error text is PRINTED (it used to be swallowed); the call is
# retried once with API_VERSION_ALT unless it is a POST (not idempotent)
gw() {
  local m=$1 ep=$2 body=${3-} v err="" vers=$API_VERSION
  if [ "$DRY" = 1 ]; then say "  DRY-RUN $m $ep $body"; return 0; fi
  if [ "$m" != POST ]; then vers="$API_VERSION $API_VERSION_ALT"; fi
  for v in $vers; do
    if [ -n "$body" ]; then
      if err=$(printf '%s' "$body" | gh api -H "X-GitHub-Api-Version: $v" -X "$m" "$ep" --input - 2>&1 >/dev/null); then return 0; fi
    else
      if err=$(gh api -H "X-GitHub-Api-Version: $v" -X "$m" "$ep" 2>&1 >/dev/null); then return 0; fi
    fi
    err=$(printf '%s' "$err" | errline)
  done
  printf '  API ERROR: %s %s -> %s\n' "$m" "$ep" "$err" >&2
  return 1
}
# read one value; prints ERR if the call fails (for example 404) and says why on stderr (QUIET=1: no message); retried once with API_VERSION_ALT
gr() {
  local out err="" ver ef=/dev/null
  if [ -n "$GHDIR" ] && [ -d "$GHDIR" ]; then ef=$GHDIR/gr.err; fi
  for ver in $API_VERSION $API_VERSION_ALT; do
    if out=$(gh api -H "X-GitHub-Api-Version: $ver" "$1" --jq "$2" 2>"$ef"); then printf '%s' "$out"; return 0; fi
    err=$(errline <"$ef")
  done
  if [ "${3-}" != quiet ]; then printf '  API ERROR: GET %s -> %s\n' "$1" "$err" >&2; fi
  printf 'ERR'
}

upsert_ruleset() { # NAME JSON
  local name=$1 body=$2 ids n
  if [ "$DRY" = 1 ]; then say "  DRY-RUN create-or-update ruleset '$name' on $WEB_REPO: $body"; return 0; fi
  ids=$(ghq "repos/$WEB_REPO/rulesets?per_page=100" --jq ".[] | select(.name==\"$name\") | .id")
  n=$(printf '%s' "$ids" | grep -c . || true)
  case "$n" in
    0) say "  ruleset $name: creating"; gw POST "repos/$WEB_REPO/rulesets" "$body";;
    1) say "  ruleset $name: updating (id $ids)"; gw PUT "repos/$WEB_REPO/rulesets/$ids" "$body";;
    *) die "ruleset name '$name' exists $n times on $WEB_REPO: remove the duplicates by hand and re-run";;
  esac
}

RULESET_TAGS='{"name":"release-tags","target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]}},"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"},{"actor_id":null,"actor_type":"DeployKey","bypass_mode":"always"}],"rules":[{"type":"creation"}]}'
RULESET_TAGS_NOKEY='{"name":"release-tags","target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]}},"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],"rules":[{"type":"creation"}]}'   # admin bypass ONLY (used while our key is not registered)
RULESET_LOCK='{"name":"release-tags-lock","target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]}},"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],"rules":[{"type":"update"},{"type":"deletion"},{"type":"non_fast_forward"}]}'
RULESET_MAIN='{"name":"main-protect","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_linear_history"},{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"do_not_enforce_on_create":false,"required_status_checks":[{"context":"test","integration_id":'"$ACTIONS_APP_ID"'}]}}]}'
ENV_BODY='{"can_admins_bypass":false,"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}'

# the pinned GitHub host keys: the local copy if it matches the pinned sha256, otherwise the copy from the pinned commit (checked against the same sha256)
get_known_hosts() {
  KH=$HERE/github_known_hosts
  if [ -f "$KH" ] && [ "$(sha_of <"$KH")" = "$KNOWN_HOSTS_SHA256" ]; then return 0; fi
  if [ "$DRY" = 1 ]; then say "  DRY-RUN fetch github_known_hosts from the pinned commit and check its sha256"; return 0; fi
  need curl; need_commit
  KHDIR=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-kh.XXXXXX") || die "cannot create a temp dir"
  chmod 700 "$KHDIR"; KH=$KHDIR/github_known_hosts
  curl -fsSL --proto '=https' --tlsv1.2 -o "$KH" "$INSTALL_RAW/$SRC_COMMIT/deploy/nopasswd/github_known_hosts" || die "cannot download github_known_hosts"
  [ "$(sha_of <"$KH")" = "$KNOWN_HOSTS_SHA256" ] || die "downloaded github_known_hosts does not match its pinned sha256: NOT using it"
}

# ---------- step 1: the one-time sudo install ----------
step_install() {
  step "1. install the passwordless-deploy entrypoint (sudo)"
  local url=$INSTALL_RAW/${SRC_COMMIT:-<commit>}/deploy/nopasswd/setup-deploy-nopasswd.sh cur="" s got
  say "  pinned installer: sha256 $INSTALL_SHA256, downloaded from main commit ${SRC_COMMIT:-<--src-commit>}"
  if [ -f "$LIBEXEC/deploy" ]; then cur=$(sha_of <"$LIBEXEC/deploy" 2>/dev/null || true); fi
  if [ "$cur" = "$ENTRY_SHA256" ]; then say "  already installed with the pinned entrypoint ($LIBEXEC/deploy): skipping"; return 0; fi
  if [ "$DRY" = 1 ]; then say "  DRY-RUN download $url, check sha256, pipe to: sudo /bin/bash -s"; return 0; fi
  need curl sudo; need_commit
  s=$(curl -fsSL --proto '=https' --tlsv1.2 "$url") || die "cannot download the installer"
  got=$(printf '%s\n' "$s" | sha_of)
  [ "$got" = "$INSTALL_SHA256" ] || die "installer sha256 mismatch (got $got): NOT running it"
  say "  sha256 OK; running the installer with sudo (it asks for your password)"
  printf '%s\n' "$s" | sudo /bin/bash -s -- --src-commit "$SRC_COMMIT"
}

# ---------- step 2: login + eve-web settings ----------
git_ok() { # git >= 2.32 (GIT_CONFIG_GLOBAL is honoured from there)
  local v maj min
  v=$(git --version 2>/dev/null | sed -n 's/^git version \([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p')
  [ -n "$v" ] || return 1
  read -r maj min <<EOF2
$v
EOF2
  [ "$maj" -gt 2 ] || { [ "$maj" -eq 2 ] && [ "$min" -ge 32 ]; }
}

step_login() {
  step "2a. GitHub login (throwaway gh config, browser)"
  local who admin
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN GH_CONFIG_DIR=<mktemp -d, 0700> gh auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage"
    say "  DRY-RUN require: gh api user -> login == $GH_USER and admin on $WEB_REPO; on exit: gh auth logout + rm -rf the temp dir"
    return 0
  fi
  need gh git curl
  git_ok || die "git >= 2.32 is required"
  GHDIR=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-gh.XXXXXX") || die "cannot create the temp dir"
  chmod 700 "$GHDIR"
  export GH_CONFIG_DIR=$GHDIR GIT_CONFIG_GLOBAL=$GHDIR/gitconfig GIT_CONFIG_NOSYSTEM=1 GH_PAGER=cat GH_NO_UPDATE_NOTIFIER=1 NO_COLOR=1
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
  say "  log in as $GH_USER in the browser window that opens (the one-time code is shown below)"
  gh auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage || die "gh login failed"
  LOGIN_OK=1   # from here on the owner is always told to revoke, even if the token cannot be read below
  TOKEN=$(gh auth token --hostname github.com 2>/dev/null) || TOKEN=""   # kept in memory only, to prove its revocation at the end
  [ -n "$TOKEN" ] || die "cannot read the token of this login (needed to prove its revocation at the end)"
  who=$(ghq user --jq .login) || die "cannot read the logged-in user"
  [ "$who" = "$GH_USER" ] || die "logged in as '$who', not $GH_USER: aborting (nothing was changed)"
  admin=$(ghq "repos/$WEB_REPO" --jq .permissions.admin) || die "cannot read $WEB_REPO"
  [ "$admin" = true ] || die "$GH_USER is not an admin of $WEB_REPO: aborting (nothing was changed)"
  say "  logged in as $who, admin of $WEB_REPO"
}

preflight_keys() { # READ-ONLY. A write deploy key other than ours would inherit the DeployKey bypass. Before step 3b ("loose"): ours = this public key OR this title (3b replaces a stale one).
  # M-1b, again right after 3b ("strict"): the owner has just pasted a key in the browser, so ours = exactly this public key; ANY other write key aborts the run before a ruleset is touched.
  local mode=${1-loose} chg=change type blob pub rows id rk title key foreign=0
  if [ "$mode" = strict ]; then chg="ruleset change (the keys are registered, the run stops before any ruleset)"; fi
  if [ "$DRY" = 1 ]; then
    if [ "$mode" = strict ]; then say "  DRY-RUN M-1b: list the deploy keys again after 3b; abort if ANY write key other than this public key exists (no ruleset is changed then)"
    else say "  DRY-RUN list deploy keys of $WEB_REPO; abort with no change if a write key other than ours exists"; fi
    return 0
  fi
  read -r type blob _ <"$KEY.pub"
  pub="$type $blob"
  rows=$(ghq "repos/$WEB_REPO/keys?per_page=100" --jq '.[] | "\(.id)\t\(.read_only)\t\(.title)\t\(.key)"') || die "cannot list the deploy keys of $WEB_REPO: aborting (nothing was changed)"
  while IFS=$'\t' read -r id rk title key; do
    [ -n "${id:-}" ] || continue
    if [ "$rk" = false ] && [ "$key" != "$pub" ] && { [ "$mode" = strict ] || [ "$title" != "$KEY_TITLE" ]; }; then
      foreign=$((foreign + 1)); say "  FOREIGN WRITE deploy key: id $id, title '$title'"
    fi
  done <<EOF2
$rows
EOF2
  [ "$foreign" = 0 ] || die "$foreign write deploy key(s) other than $KEY_TITLE exist on $WEB_REPO: the DeployKey bypass would apply to them too. Aborting with NO $chg; delete them in the repository settings (Deploy keys) or make them read-only, then re-run"
  say "  preflight OK ($mode): no write deploy key other than ours"
}

sync_env_policies() { # exactly one deployment policy: tag v*
  local rows id t n nt=0
  if [ "$DRY" = 1 ]; then gw POST "repos/$WEB_REPO/environments/deploy/deployment-branch-policies" '{"name":"v*","type":"tag"}'; return 0; fi
  rows=$(ghq "repos/$WEB_REPO/environments/deploy/deployment-branch-policies?per_page=100" --jq '.branch_policies[] | "\(.id) \(.type) \(.name)"')
  while read -r id t n; do
    [ -n "${id:-}" ] || continue
    if [ "$t" = tag ] && [ "$n" = 'v*' ] && [ "$nt" = 0 ]; then nt=1; continue; fi
    say "  removing other deployment policy $id ($t $n)"
    gw DELETE "repos/$WEB_REPO/environments/deploy/deployment-branch-policies/$id"
  done <<EOF2
$rows
EOF2
  if [ "$nt" = 0 ]; then gw POST "repos/$WEB_REPO/environments/deploy/deployment-branch-policies" '{"name":"v*","type":"tag"}'; fi
}

upsert_tags() { # release-tags: the DeployKey bypass only if our key IS registered (MANUAL_KEY=0); otherwise admin bypass only, so no key can create a v* tag
  if [ "$MANUAL_KEY" = 1 ]; then
    say "  release-tags: our key is NOT registered: applying it WITHOUT the DeployKey bypass (admin bypass only)"
    upsert_ruleset release-tags "$RULESET_TAGS_NOKEY"
  else upsert_ruleset release-tags "$RULESET_TAGS"; fi
}

step_settings() {
  step "2b. settings on $WEB_REPO (idempotent)"
  local cur
  upsert_tags
  upsert_ruleset release-tags-lock "$RULESET_LOCK"
  upsert_ruleset main-protect "$RULESET_MAIN"
  say "  environment deploy: v* tags only, admins cannot bypass"
  gw PUT "repos/$WEB_REPO/environments/deploy" "$ENV_BODY" || say "  warning: the environment PUT failed; the verify table decides"
  sync_env_policies || say "  warning: the environment policies could not be synced; the verify table decides"
  say "  immutable releases: on"
  if [ "$DRY" = 1 ] || [ "$(gr "repos/$WEB_REPO/immutable-releases" .enabled quiet)" != true ]; then gw PUT "repos/$WEB_REPO/immutable-releases"; else say "  (already on)"; fi
  step_actions
}

step_actions() { # the three repository Actions settings; a failing call does not stop the run: its error is printed and the verify table decides
  local cur
  say "  Actions: SHA pinning required"
  if [ "$DRY" = 1 ]; then
    gw PUT "repos/$WEB_REPO/actions/permissions" '{"enabled":true,"allowed_actions":"<current>","sha_pinning_required":true}'
  else
    cur=$(gr "repos/$WEB_REPO/actions/permissions" .allowed_actions)
    case "$cur" in
      all|local_only|selected) gw PUT "repos/$WEB_REPO/actions/permissions" '{"enabled":true,"allowed_actions":"'"$cur"'","sha_pinning_required":true}' || say "  FAILED (see the API ERROR above); the verify table decides";;
      *) say "  FAILED: cannot read the current allowed_actions of $WEB_REPO (got '$cur'); SHA pinning NOT changed (see the API ERROR above)";;
    esac
  fi
  say "  Actions: default GITHUB_TOKEN permission read; Actions may not approve PRs"
  gw PUT "repos/$WEB_REPO/actions/permissions/workflow" '{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}' || say "  FAILED (see the API ERROR above); the verify table decides"
  say "  Actions: fork PR workflows need approval (all external contributors)"
  gw PUT "repos/$WEB_REPO/actions/permissions/fork-pr-contributor-approval" '{"approval_policy":"all_external_contributors"}' || say "  FAILED (see the API ERROR above); the verify table decides"
}

# ---------- step 3: the web tag key ----------
step_keygen() {
  local pt pb kt kb
  step "3a. web release-tag key $KEY"
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN mkdir 0700 $KEY_DIR; ssh-keygen -q -t ed25519 -N '' -C $KEY_TITLE -f $KEY (only if it does not exist); chmod 0600 the key"
    return 0
  fi
  need ssh-keygen
  [ ! -L "$KEY_DIR" ] || die "$KEY_DIR is a symlink"
  if [ -e "$KEY_DIR" ]; then [ "$(owner_of "$KEY_DIR")" = "$(id -u)" ] || die "$KEY_DIR is not owned by you: refusing to use it"; fi
  ( umask 077; mkdir -p "$KEY_DIR" )
  chmod 700 "$KEY_DIR"
  [ "$(mode_of "$KEY_DIR")" = 700 ] || die "$KEY_DIR is not mode 0700"
  if [ -e "$KEY" ] || [ -L "$KEY" ]; then
    [ -f "$KEY" ] && [ ! -L "$KEY" ] || die "$KEY is not a regular file"
    [ "$(owner_of "$KEY")" = "$(id -u)" ] || die "$KEY is not owned by you: refusing to reuse it"
    chmod 600 "$KEY"
    ssh-keygen -y -P '' -f "$KEY" >"$KEY_DIR/.pub.check" 2>/dev/null </dev/null || { rm -f "$KEY_DIR/.pub.check"; die "existing $KEY is not a readable key without passphrase: refusing to touch it"; }
    if [ -f "$KEY.pub" ]; then   # N-3: the public key that gets registered must be the one of THIS private key
      read -r pt pb _ <"$KEY.pub"; read -r kt kb _ <"$KEY_DIR/.pub.check"
      [ "$kt $kb" = "$pt $pb" ] || { rm -f "$KEY_DIR/.pub.check"; die "$KEY.pub is not the public key of $KEY: refusing to register it (delete the .pub or fix it by hand)"; }
    fi
    rm -f "$KEY_DIR/.pub.check"
    say "  key already exists: kept"
  else
    ( umask 077; ssh-keygen -q -t ed25519 -N '' -C "$KEY_TITLE" -f "$KEY" </dev/null >/dev/null )
    say "  generated an ed25519 key without passphrase"
  fi
  chmod 600 "$KEY"
  [ -f "$KEY.pub" ] || ssh-keygen -y -P '' -f "$KEY" >"$KEY.pub" </dev/null
  chmod 644 "$KEY.pub"
  [ "$(mode_of "$KEY")" = 600 ] || die "$KEY is not mode 0600"
  say "  public key fingerprint: $(ssh-keygen -lf "$KEY.pub" | cut -d' ' -f2)"
}

step_keycheck() { # --only-fix: the key made by the first run is only READ here: nothing is created, chmod-ed, rewritten or deleted
  step "3a. existing web release-tag key $KEY (read-only check; not modified)"
  local pt pb kt kb
  if [ "$DRY" = 1 ]; then say "  DRY-RUN require: $KEY_DIR (0700) and $KEY (0600) exist, owned by you, no passphrase, and $KEY.pub is its public key; nothing is written"; return 0; fi
  need ssh-keygen
  [ -d "$KEY_DIR" ] && [ ! -L "$KEY_DIR" ] || die "$KEY_DIR does not exist (or is a symlink): run the full line first (without --only-fix)"
  [ "$(owner_of "$KEY_DIR")" = "$(id -u)" ] || die "$KEY_DIR is not owned by you: refusing to use it"
  [ "$(mode_of "$KEY_DIR")" = 700 ] || die "$KEY_DIR is not mode 0700: --only-fix does not change files; fix it by hand (chmod 700) or run the full line"
  [ -f "$KEY" ] && [ ! -L "$KEY" ] || die "$KEY is not a regular file: run the full line first (without --only-fix)"
  [ "$(owner_of "$KEY")" = "$(id -u)" ] || die "$KEY is not owned by you: refusing to use it"
  [ "$(mode_of "$KEY")" = 600 ] || die "$KEY is not mode 0600: --only-fix does not change files; fix it by hand (chmod 600) or run the full line"
  [ -f "$KEY.pub" ] || die "no public key at $KEY.pub"
  read -r pt pb _ <"$KEY.pub"
  { [ "$pt" = ssh-ed25519 ] && [ -n "$pb" ]; } || die "$KEY.pub is not an ed25519 public key"
  read -r kt kb _ <<EOF2 || die "existing $KEY is not a readable key without passphrase"
$(ssh-keygen -y -P '' -f "$KEY" </dev/null 2>/dev/null)
EOF2
  [ "$kt $kb" = "$pt $pb" ] || die "$KEY.pub is not the public key of $KEY (or $KEY is not readable): refusing to register it"
  say "  key found, private key untouched; public key fingerprint: $(ssh-keygen -lf "$KEY.pub" | cut -d' ' -f2)"
}

# Why the key is added in the BROWSER and not with `POST /repos/{o}/{r}/keys`: GitHub deletes a deploy key that was created with an OAuth app token as soon as that token is revoked
# (docs: REST API endpoints for deploy keys). The last step of this script revokes the token, so an API-created key would be gone again right after the verify table said PASS.
# A key added through the web page belongs to the repository, not to the token.
list_keys() { ghq "repos/$WEB_REPO/keys?per_page=100" --jq '.[] | "\(.id)\t\(.read_only)\t\(.title)\t\(.key)"'; }
step_register() {
  step "3b. register the PUBLIC key as write deploy key '$KEY_TITLE' on $WEB_REPO (you add it in the browser)"
  local type blob pub rows id rk title key found tries=0
  if [ "$DRY" = 1 ]; then
    say "  DRY-RUN list the deploy keys; delete a stale key (same title or same key, but not write+titled as intended); if ours is missing: copy $KEY.pub to the clipboard,"
    say "  DRY-RUN open $KEYS_URL, ask you to paste it (title $KEY_TITLE, tick 'Allow write access', Add key) and type 'added', then read the key list back (up to 3 tries)"
    say "  DRY-RUN ssh -F /dev/null -i $KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$KH ... -T git@github.com"
    return 0
  fi
  [ -f "$KEY.pub" ] || die "no public key at $KEY.pub"
  read -r type blob _ <"$KEY.pub"
  { [ "$type" = ssh-ed25519 ] && [ -n "$blob" ]; } || die "$KEY.pub is not an ed25519 public key"
  pub="$type $blob"
  while :; do
    found=0
    rows=$(list_keys) || die "cannot list the deploy keys of $WEB_REPO"
    while IFS=$'\t' read -r id rk title key; do
      [ -n "${id:-}" ] || continue
      if [ "$key" = "$pub" ] && [ "$rk" = false ] && [ "$title" = "$KEY_TITLE" ]; then found=1; say "  deploy key registered (id $id, write, title $KEY_TITLE)"; continue; fi
      if [ "$key" = "$pub" ] || [ "$title" = "$KEY_TITLE" ]; then
        say "  replacing deploy key $id ('$title', read_only=$rk): same key or same title, but not as intended"
        gw DELETE "repos/$WEB_REPO/keys/$id" || die "cannot delete the stale deploy key $id"
      fi
    done <<EOF2
$rows
EOF2
    [ "$found" = 0 ] || return 0
    tries=$((tries + 1))
    if [ "$tries" -gt 3 ]; then break; fi
    printf '\n  The key is not registered yet. In the browser (signed in as %s):\n' "$GH_USER"
    printf '    %s\n' "$KEYS_URL"
    printf '    Title: %s     Key: paste the public key below     tick "Allow write access"     then click "Add key"\n' "$KEY_TITLE"
    printf '    public key (safe to show; it is copied to the clipboard when pbcopy exists):\n    %s %s\n' "$pub" "$KEY_TITLE"
    if command -v pbcopy >/dev/null 2>&1; then printf '%s %s\n' "$pub" "$KEY_TITLE" | pbcopy 2>/dev/null || true; fi
    if command -v open >/dev/null 2>&1; then open "$KEYS_URL" >/dev/null 2>&1 || true; elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$KEYS_URL" >/dev/null 2>&1 || true; fi
    if ! ask_word '  Type "added" and press Enter once the key is added: ' added; then say "  (not confirmed: reading the key list anyway)"; tries=4; fi
  done
  MANUAL_KEY=1
  say "  the key is still NOT registered as intended: the verify table will FAIL for it"
}

ssh_test() { # prints GitHub's greeting (ssh -T exits 1 even on success)
  ssh -F /dev/null -i "$KEY" -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes \
    -o "UserKnownHostsFile=$KH" -o GlobalKnownHostsFile=/dev/null -o UpdateHostKeys=no -o ConnectTimeout=20 -T git@github.com </dev/null 2>&1 || true
}

# ---------- step 4: verify ----------
CHK_OK=1
chk() { # NAME WANT GOT
  if [ "$2" = "$3" ]; then CHK_OK=1; printf '  PASS  %s: %s\n' "$1" "$3"; else CHK_OK=0; printf '  FAIL  %s: got [%s] want [%s]\n' "$1" "$3" "$2"; FAILS=$((FAILS + 1)); fi
}
rs_id() { gr "repos/$WEB_REPO/rulesets?per_page=100" ".[] | select(.name==\"$1\") | .id"; }

verify() {
  step "4. verify (read back from GitHub)"
  local id g hello fp type blob want
  id=$(rs_id release-tags)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"')
  if [ "$MANUAL_KEY" = 1 ]; then want="tag active refs/tags/v* creation bypass=RepositoryRole:5:always"; else want="tag active refs/tags/v* creation bypass=DeployKey:null:always,RepositoryRole:5:always"; fi   # no key registered: no DeployKey bypass
  chk "ruleset release-tags" "$want" "$g"
  id=$(rs_id release-tags-lock)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"')
  chk "ruleset release-tags-lock" "tag active refs/tags/v* deletion,non_fast_forward,update bypass=RepositoryRole:5:always" "$g"
  id=$(rs_id main-protect)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) strict=\(first(.rules[]|select(.type=="required_status_checks")|.parameters.strict_required_status_checks_policy)) checks=\([.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks[].context]|sort|join(",")) integration=\([.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks[]|.integration_id|tostring]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"')
  chk "ruleset main-protect" "branch active ~DEFAULT_BRANCH deletion,non_fast_forward,required_linear_history,required_status_checks strict=true checks=test integration=$ACTIONS_APP_ID bypass=" "$g"
  g=$(gr "repos/$WEB_REPO/environments/deploy" '"admins_bypass=\(.can_admins_bypass) custom_policies=\(.deployment_branch_policy.custom_branch_policies) protected_branches=\(.deployment_branch_policy.protected_branches)"')
  chk "environment deploy" "admins_bypass=false custom_policies=true protected_branches=false" "$g"
  g=$(gr "repos/$WEB_REPO/environments/deploy/deployment-branch-policies?per_page=100" '[.branch_policies[]|"\(.type):\(.name)"]|sort|join(",")')
  chk "environment deploy policies" "tag:v*" "$g"
  chk "immutable releases" true "$(gr "repos/$WEB_REPO/immutable-releases" .enabled)"
  chk "Actions SHA pinning required" "true true" "$(gr "repos/$WEB_REPO/actions/permissions" '"\(.sha_pinning_required) \(.enabled)"')"; [ "$CHK_OK" = 1 ] || MANUAL_ACTIONS=1
  chk "default workflow permissions" "read false" "$(gr "repos/$WEB_REPO/actions/permissions/workflow" '"\(.default_workflow_permissions) \(.can_approve_pull_request_reviews)"')"; [ "$CHK_OK" = 1 ] || MANUAL_ACTIONS=1
  chk "fork PR approval" all_external_contributors "$(gr "repos/$WEB_REPO/actions/permissions/fork-pr-contributor-approval" .approval_policy)"; [ "$CHK_OK" = 1 ] || MANUAL_ACTIONS=1
  # the DeployKey bypass covers EVERY write deploy key of the repo: the intended one must be the only one
  read -r type blob _ <"$KEY.pub"
  g=$(gr "repos/$WEB_REPO/keys?per_page=100" '[.[]|select(.read_only==false)|"\(.title)|\(.key)"]|sort|join(",")')
  chk "only write deploy key" "$KEY_TITLE|$type $blob" "$g"; [ "$CHK_OK" = 1 ] || MANUAL_KEY=1
  hello=$(ssh_test)
  case "$hello" in
    *"Hi $WEB_REPO!"*) chk "ssh -T with pinned host keys" ok ok;;
    *) chk "ssh -T with pinned host keys" "greeting for $WEB_REPO" "$(printf '%s' "$hello" | head -c 200 | tr '\n' ' ')";;
  esac
  chk "key file modes" "700 600" "$(mode_of "$KEY_DIR") $(mode_of "$KEY")"
  fp=$(ssh-keygen -lf "$KEY.pub" | cut -d' ' -f2)
  say "  deploy-key fingerprint: $fp"
  teardown
  chk "GitHub CLI OAuth token revoked" "confirmed=yes http=401" "$REVOKE_RESULT"
  step "summary"
  if [ "$FAILS" = 0 ]; then say "  ALL CHECKS PASSED"; else say "  $FAILS CHECK(S) FAILED: see the FAIL lines above and the API ERROR lines (they say why)"; fi
  if [ "$FAILS" != 0 ] && [ "$ONLY_FIX" = 1 ]; then say "  (a FAIL row other than the key, ssh and the three Actions settings is not repaired by --only-fix: run the full line without it)"; fi
  if [ "$MANUAL_ACTIONS" = 1 ]; then
    say "  By hand (signed in as $GH_USER, no token needed): $ACTIONS_URL"
    say "    - Actions > General > Actions permissions: tick \"Require actions to be pinned to a full-length commit SHA\", Save"
    say "    - Workflow permissions: \"Read repository contents and packages permissions\"; leave \"Allow GitHub Actions to create and approve pull requests\" OFF, Save"
    say "    - Fork pull request workflows from outside collaborators: \"Require approval for all external contributors\", Save"
  fi
  if [ "$MANUAL_KEY" = 1 ]; then say "  By hand (signed in as $GH_USER): $KEYS_URL  title $KEY_TITLE, key = the contents of $KEY.pub, tick \"Allow write access\", Add key (then: ssh -T the key, or run --only-fix again)"; fi
  [ "$FAILS" = 0 ]
}

# ---------- main ----------
main() {
  if [ "$DRY" = 1 ]; then say "DRY-RUN: nothing is changed, no login, no sudo, no network"; fi
  if [ "$DO_GITHUB" = 1 ]; then get_known_hosts; fi
  if [ "$ONLY_FIX" = 1 ]; then
    say "ONLY-FIX: no sudo install, the key files are only read, the rulesets / environment / immutable releases are only read back"
    step_keycheck        # read-only: a problem with the existing key stops everything before any GitHub change
  else
    step_keygen          # local and first: a problem with an existing key stops everything before sudo or any GitHub change
  fi
  if [ "$DO_INSTALL" = 1 ]; then step_install; fi
  if [ "$DO_GITHUB" = 1 ]; then
    step_login
    step "2a'. preflight: deploy keys of $WEB_REPO (read-only)"
    preflight_keys
    step_register
    step "3b'. re-check right after the key was added (M-1b): deploy keys of $WEB_REPO (read-only)"
    preflight_keys strict
    if [ "$ONLY_FIX" = 1 ]; then
      step "2b. release-tags (its DeployKey bypass depends on 3b) and the Actions settings on $WEB_REPO (the only settings written by --only-fix)"
      upsert_tags
      step_actions
    else
      step_settings
    fi
  fi
  if [ "$DRY" = 1 ]; then say; say "DRY-RUN done: a real run reads every setting back (PASS/FAIL table), runs ssh -T and makes you revoke the login's OAuth token"
  elif [ "$DO_GITHUB" = 1 ]; then verify; fi
}
main
