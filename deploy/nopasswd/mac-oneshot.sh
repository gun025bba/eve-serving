#!/bin/bash
# mac-oneshot.sh: the one-time setup of the passwordless deploy, run ONCE by the owner on the Mac (tonight); it can be re-run safely.
#
#   clone-free (the pinned one-liner is in deploy/nopasswd/README.md):  s=$(curl ... mac-oneshot.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = <sha256> ] && bash -c "$s" mac-oneshot
#   from a clone:   bash deploy/nopasswd/mac-oneshot.sh [--dry-run] [--skip-install] [--skip-github]      (bash, not zsh)
#
#   --dry-run       prints every step and every GitHub API call it WOULD make; changes nothing, asks nothing, needs no login, no sudo, no network
#   --skip-install  skip step 1;   --skip-github  skip the GitHub steps (login, eve-web settings, deploy-key registration, ssh test, verify)
#
# What it does, in this order (the only things you type: your sudo password once, the GitHub login in the browser, and "revoked" at the end):
#   0. KEY, local, FIRST: generates ~/.eve-web-release-tagger/id_ed25519 (ed25519, no passphrase, dir 0700, key 0600) unless it exists (an existing key is kept, but only if it and its dir are
#      owned by you and not accessible to others), so a problem with an existing key stops everything before sudo or any GitHub change. The private key is never printed, copied or sent.
#   1. INSTALL (sudo): downloads the installer from a FIXED commit, refuses unless its sha256 matches the pin below, and pipes exactly the checked text to `sudo /bin/bash -s`
#      (the same thing as the README line). The installed entrypoint REFUSES kind=web (WEB_ENABLED=no in eve-deploy) until a reviewed change turns it on.
#      Skipped if /usr/local/libexec/eve/deploy already has the pinned bytes.
#   2. GITHUB: `gh auth login --web` as gun025bba in a THROWAWAY gh config dir (token stored only there, git credential config redirected there; your own gh login is untouched).
#      Aborts unless `gh api user` says gun025bba and that account is admin of gun025bba/eve-web.
#      PREFLIGHT (read-only, before ANY change): lists the deploy keys of eve-web; if a WRITE key other than ours (same public key or same title eve-web-release-tagger) exists, it aborts with no
#      change at all, because the DeployKey bypass below applies to EVERY write deploy key of the repository.
#      Then registers our public key as a WRITE deploy key `eve-web-release-tagger` (a stale key with our title, or a read-only copy of our key, is replaced), and then, idempotently
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
#      with it and expecting HTTP 401 (a FAIL row and a non-zero exit otherwise). This also runs when the script stops early (error, Ctrl-C) once a login exists.
#      Not covered: SIGKILL (kill -9) or a power loss cannot run any cleanup: the throwaway dir under $TMPDIR (eve-oneshot-gh.*, mode 0700) then stays with the token inside. Delete it by hand
#      and revoke the authorization at the link above.
#
# Bash 3.2 / BSD tools compatible. Needs: curl, shasum, sudo, git >= 2.32, gh, ssh, ssh-keygen. It uses `gh api --jq` only (no jq needed).
# Meant to be read before it is run: every pin is a constant right below. No secret is ever printed or logged.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -euo pipefail

# ---------- pins and constants (the tests check these against the files in the repository) ----------
INSTALL_RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
INSTALL_COMMIT=77ab92731d9ee77d1fda63947a6261bba78b8c04
INSTALL_SHA256=e4e574abffbce0c0f48eff68524c6ccb428b7b5e36cc790f0bc6b30da29edaf7
ENTRY_SHA256=2cd051c1b9c140e49927eff97b95d4d12515d21c5ed2255d47274e35b7e17c4f   # sha256 of the installed /usr/local/libexec/eve/deploy (SHA_DEPLOY in the installer)
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

DRY=0; DO_INSTALL=1; DO_GITHUB=1
HERE=$(cd "$(dirname "$0")" && pwd)
GHDIR=""; FAILS=0; TOKEN=""; REVOKE_DONE=0; REVOKE_RESULT="not run"; KHDIR=""; KH=$HERE/github_known_hosts

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
    -h|--help) say "usage: mac-oneshot.sh [--dry-run] [--skip-install] [--skip-github]   (see the comment block at the top of the script)"; exit 0;;
    *) die "unknown argument: $1 (see --help)";;
  esac
  shift
done

# ---------- cleanup: log the throwaway login out, delete the throwaway dir, make the owner revoke the token ----------
owner_of() { stat -c %u "$1" 2>/dev/null || stat -f %u "$1"; }
revoke_flow() { # the OAuth token is still valid on GitHub's side after `gh auth logout`: the owner revokes it, then we PROVE it is dead (401)
  local ans="" tries=0 confirmed=no code=other out tmp
  REVOKE_DONE=1
  printf '\n== 5. revoke the GitHub CLI authorization (gh auth logout only deleted the LOCAL copy of the token)\n'
  printf '   Open https://github.com/settings/applications -> "Authorized OAuth Apps" -> "GitHub CLI" -> Revoke.\n'
  if { exec 3<"$TTY"; } 2>/dev/null; then
    while [ "$tries" -lt 3 ]; do
      printf '   Type "revoked" and press Enter once you have revoked it: '
      if read -r ans <&3; then :; else printf '\n'; break; fi
      if [ "$ans" = revoked ]; then confirmed=yes; break; fi
      tries=$((tries + 1))
    done
    exec 3<&-
  else printf '   (no terminal to ask on: not confirmed)\n'; fi
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-chk.XXXXXX") || tmp=""
  if out=$(GH_CONFIG_DIR=${tmp:-/nonexistent} GH_TOKEN=$TOKEN GH_PROMPT_DISABLED=1 gh api -i user 2>&1); then code=200
  else case "$out" in *"HTTP/"*" 401"*|*"(HTTP 401)"*) code=401;; *) code=other;; esac; fi
  [ -z "$tmp" ] || rm -rf "$tmp"
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
  if [ -n "$TOKEN" ] && [ "$REVOKE_DONE" = 0 ]; then revoke_flow; fi
}
cleanup() {
  local rc=$?
  trap - EXIT INT TERM HUP
  set +e
  teardown
  [ -z "$KHDIR" ] || rm -rf "$KHDIR"
  if [ "$REVOKE_DONE" = 1 ] && [ "$REVOKE_RESULT" != "confirmed=yes http=401" ] && [ "$rc" = 0 ]; then rc=1; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

# ---------- GitHub API helpers (used only with the throwaway login) ----------
ghq() { gh api -H "X-GitHub-Api-Version: $API_VERSION" "$@"; }
# write: METHOD ENDPOINT [JSON]; the JSON body goes through stdin (never on a command line)
gw() {
  local m=$1 ep=$2 body=${3-}
  if [ "$DRY" = 1 ]; then say "  DRY-RUN $m $ep $body"; return 0; fi
  if [ -n "$body" ]; then printf '%s' "$body" | ghq -X "$m" "$ep" --input - >/dev/null; else ghq -X "$m" "$ep" >/dev/null; fi
}
# read one value; prints ERR if the call fails (for example 404)
gr() { local v; v=$(ghq "$1" --jq "$2" 2>/dev/null) || v=ERR; printf '%s' "$v"; }

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
RULESET_LOCK='{"name":"release-tags-lock","target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]}},"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],"rules":[{"type":"update"},{"type":"deletion"},{"type":"non_fast_forward"}]}'
RULESET_MAIN='{"name":"main-protect","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_linear_history"},{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"do_not_enforce_on_create":false,"required_status_checks":[{"context":"test","integration_id":'"$ACTIONS_APP_ID"'}]}}]}'
ENV_BODY='{"can_admins_bypass":false,"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}'

# the pinned GitHub host keys: the local copy if it matches the pinned sha256, otherwise the copy from the pinned commit (checked against the same sha256)
get_known_hosts() {
  KH=$HERE/github_known_hosts
  if [ -f "$KH" ] && [ "$(sha_of <"$KH")" = "$KNOWN_HOSTS_SHA256" ]; then return 0; fi
  if [ "$DRY" = 1 ]; then say "  DRY-RUN fetch github_known_hosts from the pinned commit and check its sha256"; return 0; fi
  need curl
  KHDIR=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-kh.XXXXXX") || die "cannot create a temp dir"
  chmod 700 "$KHDIR"; KH=$KHDIR/github_known_hosts
  curl -fsSL --proto '=https' --tlsv1.2 -o "$KH" "$INSTALL_RAW/$INSTALL_COMMIT/deploy/nopasswd/github_known_hosts" || die "cannot download github_known_hosts"
  [ "$(sha_of <"$KH")" = "$KNOWN_HOSTS_SHA256" ] || die "downloaded github_known_hosts does not match its pinned sha256: NOT using it"
}

# ---------- step 1: the one-time sudo install ----------
step_install() {
  step "1. install the passwordless-deploy entrypoint (sudo)"
  local url=$INSTALL_RAW/$INSTALL_COMMIT/deploy/nopasswd/setup-deploy-nopasswd.sh cur="" s got
  say "  pinned installer: commit $INSTALL_COMMIT, sha256 $INSTALL_SHA256"
  if [ -f "$LIBEXEC/deploy" ]; then cur=$(sha_of <"$LIBEXEC/deploy" 2>/dev/null || true); fi
  if [ "$cur" = "$ENTRY_SHA256" ]; then say "  already installed with the pinned entrypoint ($LIBEXEC/deploy): skipping"; return 0; fi
  if [ "$DRY" = 1 ]; then say "  DRY-RUN download $url, check sha256, pipe to: sudo /bin/bash -s"; return 0; fi
  need curl sudo
  s=$(curl -fsSL --proto '=https' --tlsv1.2 "$url") || die "cannot download the installer"
  got=$(printf '%s\n' "$s" | sha_of)
  [ "$got" = "$INSTALL_SHA256" ] || die "installer sha256 mismatch (got $got): NOT running it"
  say "  sha256 OK; running the installer with sudo (it asks for your password)"
  printf '%s\n' "$s" | sudo /bin/bash -s
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
  need gh git
  git_ok || die "git >= 2.32 is required"
  GHDIR=$(mktemp -d "${TMPDIR:-/tmp}/eve-oneshot-gh.XXXXXX") || die "cannot create the temp dir"
  chmod 700 "$GHDIR"
  export GH_CONFIG_DIR=$GHDIR GIT_CONFIG_GLOBAL=$GHDIR/gitconfig GIT_CONFIG_NOSYSTEM=1 GH_PAGER=cat GH_NO_UPDATE_NOTIFIER=1 NO_COLOR=1
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
  say "  log in as $GH_USER in the browser window that opens (the one-time code is shown below)"
  gh auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage || die "gh login failed"
  TOKEN=$(gh auth token --hostname github.com 2>/dev/null) || TOKEN=""   # kept in memory only, to prove its revocation at the end
  [ -n "$TOKEN" ] || die "cannot read the token of this login (needed to prove its revocation at the end)"
  who=$(ghq user --jq .login) || die "cannot read the logged-in user"
  [ "$who" = "$GH_USER" ] || die "logged in as '$who', not $GH_USER: aborting (nothing was changed)"
  admin=$(ghq "repos/$WEB_REPO" --jq .permissions.admin) || die "cannot read $WEB_REPO"
  [ "$admin" = true ] || die "$GH_USER is not an admin of $WEB_REPO: aborting (nothing was changed)"
  say "  logged in as $who, admin of $WEB_REPO"
}

preflight_keys() { # READ-ONLY, before any change: a write deploy key other than ours would inherit the DeployKey bypass
  local type blob pub rows id rk title key foreign=0
  if [ "$DRY" = 1 ]; then say "  DRY-RUN list deploy keys of $WEB_REPO; abort with no change if a write key other than ours exists"; return 0; fi
  read -r type blob _ <"$KEY.pub"
  pub="$type $blob"
  rows=$(ghq "repos/$WEB_REPO/keys?per_page=100" --jq '.[] | "\(.id)\t\(.read_only)\t\(.title)\t\(.key)"') || die "cannot list the deploy keys of $WEB_REPO: aborting (nothing was changed)"
  while IFS=$'\t' read -r id rk title key; do
    [ -n "${id:-}" ] || continue
    if [ "$rk" = false ] && [ "$key" != "$pub" ] && [ "$title" != "$KEY_TITLE" ]; then
      foreign=$((foreign + 1)); say "  FOREIGN WRITE deploy key: id $id, title '$title'"
    fi
  done <<EOF2
$rows
EOF2
  [ "$foreign" = 0 ] || die "$foreign write deploy key(s) other than $KEY_TITLE exist on $WEB_REPO: the DeployKey bypass would apply to them too. Aborting with NO change; delete them in the repository settings (Deploy keys) or make them read-only, then re-run"
  say "  preflight OK: no write deploy key other than ours"
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

step_settings() {
  step "2b. settings on $WEB_REPO (idempotent)"
  local cur
  upsert_ruleset release-tags "$RULESET_TAGS"
  upsert_ruleset release-tags-lock "$RULESET_LOCK"
  upsert_ruleset main-protect "$RULESET_MAIN"
  say "  environment deploy: v* tags only, admins cannot bypass"
  gw PUT "repos/$WEB_REPO/environments/deploy" "$ENV_BODY" || say "  warning: the environment PUT failed; the verify table decides"
  sync_env_policies || say "  warning: the environment policies could not be synced; the verify table decides"
  say "  immutable releases: on"
  if [ "$DRY" = 1 ] || [ "$(gr "repos/$WEB_REPO/immutable-releases" .enabled)" != true ]; then gw PUT "repos/$WEB_REPO/immutable-releases"; else say "  (already on)"; fi
  say "  Actions: SHA pinning required"
  if [ "$DRY" = 1 ]; then
    gw PUT "repos/$WEB_REPO/actions/permissions" '{"enabled":true,"allowed_actions":"<current>","sha_pinning_required":true}'
  else
    cur=$(gr "repos/$WEB_REPO/actions/permissions" .allowed_actions)
    case "$cur" in all|local_only|selected) ;; *) die "cannot read the current allowed_actions of $WEB_REPO (got '$cur')";; esac
    gw PUT "repos/$WEB_REPO/actions/permissions" '{"enabled":true,"allowed_actions":"'"$cur"'","sha_pinning_required":true}'
  fi
  say "  Actions: default GITHUB_TOKEN permission read; Actions may not approve PRs"
  gw PUT "repos/$WEB_REPO/actions/permissions/workflow" '{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}'
  say "  Actions: fork PR workflows need approval (all external contributors)"
  gw PUT "repos/$WEB_REPO/actions/permissions/fork-pr-contributor-approval" '{"approval_policy":"all_external_contributors"}'
}

# ---------- step 3: the web tag key ----------
step_keygen() {
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

step_register() {
  step "3b. register the PUBLIC key as write deploy key '$KEY_TITLE' on $WEB_REPO"
  local type blob pub rows id rk title key found=0
  if [ "$DRY" = 1 ]; then
    gw POST "repos/$WEB_REPO/keys" '{"title":"'"$KEY_TITLE"'","key":"<contents of '"$KEY"'.pub>","read_only":false}'
    say "  DRY-RUN ssh -F /dev/null -i $KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$KH ... -T git@github.com"
    return 0
  fi
  [ -f "$KEY.pub" ] || die "no public key at $KEY.pub"
  read -r type blob _ <"$KEY.pub"
  { [ "$type" = ssh-ed25519 ] && [ -n "$blob" ]; } || die "$KEY.pub is not an ed25519 public key"
  pub="$type $blob"
  rows=$(ghq "repos/$WEB_REPO/keys?per_page=100" --jq '.[] | "\(.id)\t\(.read_only)\t\(.title)\t\(.key)"')
  while IFS=$'\t' read -r id rk title key; do
    [ -n "${id:-}" ] || continue
    if [ "$key" = "$pub" ] && [ "$rk" = false ] && [ "$title" = "$KEY_TITLE" ]; then found=1; say "  deploy key already registered (id $id)"; continue; fi
    if [ "$key" = "$pub" ] || [ "$title" = "$KEY_TITLE" ]; then
      say "  replacing deploy key $id ('$title', read_only=$rk): same key or same title, but not as intended"
      gw DELETE "repos/$WEB_REPO/keys/$id"
    fi
  done <<EOF2
$rows
EOF2
  if [ "$found" = 0 ]; then say "  registering the public key"; gw POST "repos/$WEB_REPO/keys" '{"title":"'"$KEY_TITLE"'","key":"'"$pub"'","read_only":false}'; fi
}

ssh_test() { # prints GitHub's greeting (ssh -T exits 1 even on success)
  ssh -F /dev/null -i "$KEY" -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes \
    -o "UserKnownHostsFile=$KH" -o GlobalKnownHostsFile=/dev/null -o UpdateHostKeys=no -o ConnectTimeout=20 -T git@github.com </dev/null 2>&1 || true
}

# ---------- step 4: verify ----------
chk() { # NAME WANT GOT
  if [ "$2" = "$3" ]; then printf '  PASS  %s: %s\n' "$1" "$3"; else printf '  FAIL  %s: got [%s] want [%s]\n' "$1" "$3" "$2"; FAILS=$((FAILS + 1)); fi
}
rs_id() { gr "repos/$WEB_REPO/rulesets?per_page=100" ".[] | select(.name==\"$1\") | .id"; }

verify() {
  step "4. verify (read back from GitHub)"
  local id g hello fp type blob
  id=$(rs_id release-tags)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"')
  chk "ruleset release-tags" "tag active refs/tags/v* creation bypass=DeployKey:null:always,RepositoryRole:5:always" "$g"
  id=$(rs_id release-tags-lock)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) bypass=\([.bypass_actors[]?|"\(.actor_type):\(.actor_id):\(.bypass_mode)"]|sort|join(","))"')
  chk "ruleset release-tags-lock" "tag active refs/tags/v* deletion,non_fast_forward,update bypass=RepositoryRole:5:always" "$g"
  id=$(rs_id main-protect)
  g=$(gr "repos/$WEB_REPO/rulesets/$id" '"\(.target) \(.enforcement) \(.conditions.ref_name.include|join(",")) \([.rules[].type]|sort|join(",")) strict=\(first(.rules[]|select(.type=="required_status_checks")|.parameters.strict_required_status_checks_policy)) checks=\([.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks[].context]|sort|join(","))"')
  chk "ruleset main-protect" "branch active ~DEFAULT_BRANCH deletion,non_fast_forward,required_linear_history,required_status_checks strict=true checks=test" "$g"
  g=$(gr "repos/$WEB_REPO/environments/deploy" '"admins_bypass=\(.can_admins_bypass) custom_policies=\(.deployment_branch_policy.custom_branch_policies) protected_branches=\(.deployment_branch_policy.protected_branches)"')
  chk "environment deploy" "admins_bypass=false custom_policies=true protected_branches=false" "$g"
  g=$(gr "repos/$WEB_REPO/environments/deploy/deployment-branch-policies?per_page=100" '[.branch_policies[]|"\(.type):\(.name)"]|sort|join(",")')
  chk "environment deploy policies" "tag:v*" "$g"
  chk "immutable releases" true "$(gr "repos/$WEB_REPO/immutable-releases" .enabled)"
  chk "Actions SHA pinning required" "true true" "$(gr "repos/$WEB_REPO/actions/permissions" '"\(.sha_pinning_required) \(.enabled)"')"
  chk "default workflow permissions" "read false" "$(gr "repos/$WEB_REPO/actions/permissions/workflow" '"\(.default_workflow_permissions) \(.can_approve_pull_request_reviews)"')"
  chk "fork PR approval" all_external_contributors "$(gr "repos/$WEB_REPO/actions/permissions/fork-pr-contributor-approval" .approval_policy)"
  # the DeployKey bypass covers EVERY write deploy key of the repo: the intended one must be the only one
  read -r type blob _ <"$KEY.pub"
  g=$(gr "repos/$WEB_REPO/keys?per_page=100" '[.[]|select(.read_only==false)|"\(.title)|\(.key)"]|sort|join(",")')
  chk "only write deploy key" "$KEY_TITLE|$type $blob" "$g"
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
  if [ "$FAILS" = 0 ]; then say "  ALL CHECKS PASSED"; else say "  $FAILS CHECK(S) FAILED: see the FAIL lines above"; return 1; fi
}

# ---------- main ----------
main() {
  if [ "$DRY" = 1 ]; then say "DRY-RUN: nothing is changed, no login, no sudo, no network"; fi
  if [ "$DO_GITHUB" = 1 ]; then get_known_hosts; fi
  step_keygen            # local and first: a problem with an existing key stops everything before sudo or any GitHub change
  if [ "$DO_INSTALL" = 1 ]; then step_install; fi
  if [ "$DO_GITHUB" = 1 ]; then
    step_login
    step "2a'. preflight: deploy keys of $WEB_REPO (read-only)"
    preflight_keys
    step_register
    step_settings
  fi
  if [ "$DRY" = 1 ]; then say; say "DRY-RUN done: a real run reads every setting back (PASS/FAIL table), runs ssh -T and makes you revoke the login's OAuth token"
  elif [ "$DO_GITHUB" = 1 ]; then verify; fi
}
main
