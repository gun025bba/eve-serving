#!/bin/bash
# Release tagger: creates and pushes ONE annotated tag vX.Y.Z, using a deploy key that exists for this purpose only.
# Design, key location, permissions and revocation: docs/deploy-signing.md (section "The release-tag deploy key").
#
#   bash deploy/nopasswd/release-tag.sh X.Y.Z [FULL_SHA]        (run inside a clone of the repository)
#   ... --dry-run                                                (all checks, no tag, no push)
#   ... --repo eve-web                                           (the UI repository gun025bba/eve-web: run it inside a clean clone of eve-web, main checked out. Its repository, remote, required check and key
#                                                                 path are NOT in this file: they are the constants of release-tag.eve-web.constants next to it, read with a strict parser, never sourced.
#                                                                 Allowed values of --repo: eve-serving (default, the constants below) and eve-web, nothing else. The checkout of THIS script must also be a clean main.)
#
# FULL_SHA defaults to the tip of origin/main. NO HUMAN REVIEW BEFORE TAGGING (태그 전 사람 리뷰 없음): the script does not look for any reviewer or approval; what stands between a
# merge and a tag is the branch protection of main (PR + required checks) and the three gates below. The tag is pushed only when ALL of this holds, otherwise the script refuses
# (exit 1) and pushes nothing:
#   1. the target SHA equals the tip of main on the remote (read with `git ls-remote`, and read again just before the push);
#   2. the required checks `test` and `deploy-nopasswd-macos` finished with conclusion success for that SHA: the newest run of each name that was created by GitHub Actions AND belongs to
#      a workflow run of .github/workflows/ci.yml of this repository on exactly that commit (a same-named job of another workflow is ignored);
#   3. the version is strictly greater than the highest existing vX.Y.Z tag on the remote, and the remote has no other tag matching v* that is not strictly vX.Y.Z (refused: the order of
#      such a tag cannot be trusted).
# It also refuses unless it runs from a clean clone of main (main checked out, no modified tracked files), with git 2.32 or newer, and `git ls-remote --get-url` of the remote
# is exactly the constant below (no URL rewriting).
#
# What it touches: the remote is reached ONLY through GIT_SSH_COMMAND (deploy key, no ssh config, GitHub's published host keys from deploy/nopasswd/github_known_hosts with
# StrictHostKeyChecking=yes, no agent, no prompts) and git's global/system config is switched off (no url.*.insteadOf rewriting); the only ref it ever pushes is
# refs/tags/vX.Y.Z (no branch, no --force, no --tags/--all/--mirror/--delete). `gh` is used for read-only GET requests and is never given the key. The key is never read, copied,
# printed or committed; only its path and mode (and its directory's) are checked.
# Portable: bash 3.2 and BSD tools (macOS); start it with `bash deploy/nopasswd/release-tag.sh ...` (not zsh). Needs jq (macOS 15 and newer ship it; otherwise `brew install jq`).
#
# Environment (the ONLY ones): EVE_RELEASE_TAG_KEY = path of the private key (default: $HOME/.eve-release-tagger/id_ed25519); it must be a regular file, mode 0600 or stricter,
# owned by you, in a directory owned by you without group/other access. The repository and the remote are constants below and cannot be set from the environment.
# Needs: bash, git, gh (authenticated for reads), jq, and ssh (openssh-client).
set -Eeuo pipefail
export LC_ALL=C
umask 077
die() { echo "release-tag: refused: $*" >&2; exit 1; }
note() { echo "release-tag: $*" >&2; }

# ================================================================================================================================================================
# CONSTANTS (not overridable from the environment). Changing any of them is a security-relevant change.
REPO="gun025bba/eve-serving"
REMOTE="git@github.com:${REPO}.git"
# ================================================================================================================================================================

DRY=0 VERSION="" SHA="" PROFILE=eve-serving WANT_PROFILE=0
# the checks of the default profile (eve-serving); the eve-web constants file may replace the list of checks, the workflow path and the key directory (see load_constants)
CI_PATH=".github/workflows/ci.yml"   # the workflow file the required checks must come from
REQUIRED_CHECKS="test deploy-nopasswd-macos"
KEY_DIRNAME=.eve-release-tagger   # the key is $HOME/$KEY_DIRNAME/id_ed25519
while [ $# -gt 0 ]; do
  a=$1; shift
  case "$a" in
    --dry-run) DRY=1 ;;
    --repo) [ "$WANT_PROFILE" = 0 ] || die "--repo given twice"; [ $# -gt 0 ] || die "--repo needs a value (eve-serving or eve-web)"; PROFILE=$1; WANT_PROFILE=1; shift
            case "$PROFILE" in eve-serving|eve-web) ;; *) die "--repo must be eve-serving or eve-web" ;; esac ;;
    -*) die "unknown option '$a'" ;;
    *) if [ -z "$VERSION" ]; then VERSION=$a; elif [ -z "$SHA" ]; then SHA=$a; else die "too many arguments"; fi ;;
  esac
done
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
# the constants of the eve-web profile: a separate file, KEY="value" lines only (comments and blank lines allowed), parsed here and NEVER sourced or evaluated. Every key must be present exactly once.
load_constants() { # $1 file
  local f=$1 line k v seen=" "
  [ -f "$f" ] && [ ! -L "$f" ] || die "the constants file is missing: ${f##*/}"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; esac
    [[ "$line" =~ ^([A-Z_]+)=\"([^\"\$\`\\]*)\"$ ]] || die "bad line in ${f##*/} (only KEY=\"value\" with no \$ \` or backslash): $line"
    k=${BASH_REMATCH[1]} v=${BASH_REMATCH[2]}
    case "$seen" in *" $k "*) die "duplicate key $k in ${f##*/}" ;; esac
    seen="$seen$k "
    case "$k" in
      REPO) [[ "$v" =~ ^gun025bba/[a-z0-9][a-z0-9-]*$ ]] || die "REPO in ${f##*/} is not gun025bba/<name>"; REPO=$v ;;
      REMOTE) [[ "$v" =~ ^git@github\.com:gun025bba/[a-z0-9][a-z0-9-]*\.git$ ]] || die "REMOTE in ${f##*/} is not git@github.com:gun025bba/<name>.git"; REMOTE=$v ;;
      CI_PATH) [[ "$v" =~ ^\.github/workflows/[A-Za-z0-9_.-]+\.yml$ ]] || die "CI_PATH in ${f##*/} is not a workflow file path"; CI_PATH=$v ;;
      REQUIRED_CHECKS) [[ "$v" =~ ^[A-Za-z0-9_-]+( [A-Za-z0-9_-]+)*$ ]] || die "REQUIRED_CHECKS in ${f##*/} is not a space-separated list of check names"; REQUIRED_CHECKS=$v ;;
      KEY_DIRNAME) [[ "$v" =~ ^\.[a-z0-9-]+$ ]] || die "KEY_DIRNAME in ${f##*/} is not a dot-directory name (letters, digits, -)"; KEY_DIRNAME=$v ;;
      *) die "unknown key $k in ${f##*/}" ;;
    esac
  done <"$f"
  for k in REPO REMOTE CI_PATH REQUIRED_CHECKS KEY_DIRNAME; do case "$seen" in *" $k "*) ;; *) die "key $k is missing in ${f##*/}" ;; esac; done
  [ "$REMOTE" = "git@github.com:$REPO.git" ] || die "REMOTE and REPO of ${f##*/} do not match"
}
if [ "$PROFILE" = eve-web ]; then load_constants "$SCRIPT_DIR/release-tag.eve-web.constants"; fi
[ -n "$VERSION" ] || die "usage: release-tag.sh [--dry-run] X.Y.Z [FULL_SHA]"
VERSION=${VERSION#v}
NUM='(0|[1-9][0-9]{0,8})'
[[ "$VERSION" =~ ^$NUM\.$NUM\.$NUM$ ]] || die "version must be X.Y.Z without leading zeros: $VERSION"
TAG=v$VERSION

KEY=${EVE_RELEASE_TAG_KEY:-$HOME/$KEY_DIRNAME/id_ed25519}
KNOWN_HOSTS=$SCRIPT_DIR/github_known_hosts
# GIT_SSH_COMMAND is parsed by a shell: keep the paths to plain characters
[[ "$KEY" =~ ^[A-Za-z0-9_./~+@:-]+$ ]] || die "key path has characters that are not allowed (letters, digits and _ . / ~ + @ : - only)"
[[ "$KNOWN_HOSTS" =~ ^[A-Za-z0-9_./~+@:-]+$ ]] || die "the path of the script has characters that are not allowed (letters, digits and _ . / ~ + @ : - only)"

for c in git gh jq; do command -v "$c" >/dev/null 2>&1 || die "'$c' is required but not installed"; done
git rev-parse --git-dir >/dev/null 2>&1 || die "run this inside a clone of the repository"
# git 2.32 or newer: older versions ignore GIT_CONFIG_GLOBAL/GIT_CONFIG_NOSYSTEM, so the config neutralisation below would not hold
GITV=$(git --version 2>/dev/null | sed -n 's/^git version \([0-9][0-9]*\)\.\([0-9][0-9]*\).*$/\1 \2/p' | head -n1)
[ -n "$GITV" ] || die "cannot determine the git version"
read -r GITMAJ GITMIN <<<"$GITV"
{ [ "$GITMAJ" -gt 2 ] || { [ "$GITMAJ" -eq 2 ] && [ "$GITMIN" -ge 32 ]; }; } || die "git $GITMAJ.$GITMIN is too old (2.32 or newer is required: older git ignores GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM)"
export GIT_TERMINAL_PROMPT=0
# no global/system git config: no url.*.insteadOf / pushInsteadOf rewriting of the remote, no helper, no hooks path (git 2.32 and newer honour these two variables)
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
unset GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_SSH GIT_PROXY_COMMAND
if git config --get-regexp '^url\..*\.(insteadof|pushinsteadof)$' >/dev/null 2>&1; then die "this clone's own git config rewrites URLs (url.*.insteadOf); use a clean clone"; fi
# what git would really contact for the remote (after any rewriting) must be exactly the constant
[ "$(git ls-remote --get-url "$REMOTE" 2>/dev/null)" = "$REMOTE" ] || die "git would not contact exactly $REMOTE (a URL rewrite is active); use a clean clone"
# a clean clone on main: no modified tracked file (this script included) and the branch checked out is main
[ "$(git symbolic-ref -q --short HEAD 2>/dev/null || true)" = main ] || die "run this from a clone with the main branch checked out"
[ -z "$(git status --porcelain --untracked-files=no 2>/dev/null)" ] || die "the working tree has uncommitted changes to tracked files; use a clean clone of main"
if [ "$PROFILE" != eve-serving ]; then
  # the script (and its constants file) live in ANOTHER checkout than the one being tagged: that checkout must be a clean main too, or it could be an edited copy
  [ "$(git -C "$SCRIPT_DIR" symbolic-ref -q --short HEAD 2>/dev/null || true)" = main ] || die "the checkout that holds this script must be a clean clone with main checked out"
  [ -z "$(git -C "$SCRIPT_DIR" status --porcelain --untracked-files=no 2>/dev/null)" ] || die "the checkout that holds this script has uncommitted changes to tracked files; use a clean clone of main"
fi

command -v ssh >/dev/null 2>&1 || die "'ssh' is not installed (install openssh-client); the tag is pushed over ssh with the deploy key"
modeof() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
ownerof() { stat -c %u "$1" 2>/dev/null || stat -f %u "$1"; }
KEYDIR=$(dirname "$KEY")
[ -f "$KEY" ] && [ ! -L "$KEY" ] || die "the deploy key is not a regular file at the configured path"
[ "$(ownerof "$KEY")" = "$(id -u)" ] || die "the deploy key is not owned by the current user"
case "$(modeof "$KEY")" in 600|400) ;; *) die "the deploy key must have mode 0600 (or 0400)" ;; esac
[ -d "$KEYDIR" ] && [ ! -L "$KEYDIR" ] || die "the directory of the deploy key is not a real directory"
[ "$(ownerof "$KEYDIR")" = "$(id -u)" ] || die "the directory of the deploy key is not owned by the current user"
case "$(modeof "$KEYDIR")" in 700|500) ;; *) die "the directory of the deploy key must have mode 0700 (no access for group or others)" ;; esac
[ -f "$KNOWN_HOSTS" ] && [ ! -L "$KNOWN_HOSTS" ] || die "deploy/nopasswd/github_known_hosts is missing (GitHub's published ssh host keys)"
export GIT_SSH_COMMAND="ssh -F /dev/null -i $KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$KNOWN_HOSTS -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes -o IdentityAgent=none -o UpdateHostKeys=no"

remote_main_tip() {
  local out line
  out=$(git ls-remote "$REMOTE" refs/heads/main) || die "cannot read main from the remote"
  [ "$(printf '%s\n' "$out" | grep -c .)" = 1 ] || die "expected exactly one refs/heads/main on the remote"
  line=$(printf '%s\n' "$out" | head -n1)
  [[ "$line" =~ ^([0-9a-f]{40})[[:space:]]+refs/heads/main$ ]] || die "unexpected ls-remote answer for main"
  printf '%s\n' "${BASH_REMATCH[1]}"
}

# highest well-formed vX.Y.Z tag on the remote, as "X Y Z", or empty when there is none; refuses when the remote cannot be read
highest_tag() {
  local out line ref x y z hx=-1 hy=-1 hz=-1 first
  out=$(git ls-remote --tags "$REMOTE" 'refs/tags/v*') || die "cannot list the tags on the remote"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    ref=${line#*[[:space:]]}
    case "$ref" in *'^{}') continue ;; esac
    if [[ "$ref" =~ ^refs/tags/v$NUM\.$NUM\.$NUM$ ]]; then
      x=$((10#${BASH_REMATCH[1]})) y=$((10#${BASH_REMATCH[2]})) z=$((10#${BASH_REMATCH[3]}))
      if [ "$x" -gt "$hx" ] || { [ "$x" -eq "$hx" ] && { [ "$y" -gt "$hy" ] || { [ "$y" -eq "$hy" ] && [ "$z" -gt "$hz" ]; }; }; }; then hx=$x hy=$y hz=$z; fi
    else
      die "the remote has a tag that is not strictly vX.Y.Z (${ref#refs/tags/}); refusing because the version order cannot be trusted (see docs/deploy-signing.md)"
    fi
  done <<<"$out"
  first=$hx
  if [ "$first" -ge 0 ]; then echo "$hx $hy $hz"; fi
}

check_version() {
  local hi x y z
  hi=$(highest_tag)
  if [ -z "$hi" ]; then note "no vX.Y.Z tag exists yet: $TAG is the first"; return 0; fi
  read -r x y z <<<"$hi"
  [[ "$VERSION" =~ ^$NUM\.$NUM\.$NUM$ ]]
  local a=$((10#${BASH_REMATCH[1]})) b=$((10#${BASH_REMATCH[2]})) c=$((10#${BASH_REMATCH[3]}))
  if [ "$a" -gt "$x" ] || { [ "$a" -eq "$x" ] && { [ "$b" -gt "$y" ] || { [ "$b" -eq "$y" ] && [ "$c" -gt "$z" ]; }; }; }; then
    note "version ok: $VERSION is above the highest existing tag v$x.$y.$z"
  else
    die "version $VERSION is not strictly greater than the highest existing tag v$x.$y.$z"
  fi
}

# ---- 1. target = tip of main -------------------------------------------------------------------------------------------------------------------------
TIP=$(remote_main_tip)
[ -n "$SHA" ] || SHA=$TIP
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "the target must be a full lowercase 40-character commit sha"
[ "$SHA" = "$TIP" ] || die "$SHA is not the tip of main ($TIP)"
note "target $SHA is the tip of main"

# ---- 2. version (cheap, before the API calls) --------------------------------------------------------------------------------------------------------
check_version

# ---- 3. required checks passed for this commit, from the pinned workflow file (every request is a plain GET) -----------------------------------------------------------------------
ghget() { gh api --paginate "$1"; }
RUNS=$(ghget "repos/$REPO/commits/$SHA/check-runs?per_page=100" | jq -c '.check_runs[]') || die "cannot read the check runs of $SHA"
for want in $REQUIRED_CHECKS; do
  # candidates: runs of that name from the GitHub Actions app, newest first, each as "<check id> <workflow run id> <status>/<conclusion>"
  cands=$(printf '%s\n' "$RUNS" | jq -rs --arg n "$want" '[.[] | select(.name == $n and (.app.slug // "") == "github-actions")] | sort_by(.id) | reverse | .[] |
          "\(.id) \((.details_url // "") | capture("/actions/runs/(?<r>[0-9]+)/") | .r // "0") \(.status)/\(.conclusion // "none")"') || die "cannot read the check runs of $SHA"
  got=missing
  while read -r cid rid st; do
    [ -n "$cid" ] || continue
    [[ "$rid" =~ ^[1-9][0-9]*$ ]] || continue
    # only a run of the pinned workflow file, of this repository, on exactly this commit is a candidate; a same-named job of any other workflow is ignored
    wf=$(gh api "repos/$REPO/actions/runs/$rid" | jq -r --arg p "$CI_PATH" --arg s "$SHA" --arg r "$REPO" 'if .path == $p and .head_sha == $s and (.repository.full_name // "") == $r then "ok" else "other" end') || die "cannot read workflow run $rid of check '$want'"
    if [ "$wf" = ok ]; then got=$st; break; fi   # the newest valid run decides (a re-run supersedes the earlier one)
  done <<<"$cands"
  [ "$got" = "completed/success" ] || die "required check '$want' is $got for $SHA from $CI_PATH (needs completed/success)"
done
note "checks $REQUIRED_CHECKS passed for the commit"

if [ "$DRY" = 1 ]; then note "dry run: every condition holds; would push refs/tags/$TAG at $SHA"; exit 0; fi

# ---- the tag: re-check, create, push exactly this one ref --------------------------------------------------------------------------------------------
[ "$(remote_main_tip)" = "$SHA" ] || die "main moved while checking; $SHA is no longer the tip (nothing was pushed)"
check_version
if ! git cat-file -e "$SHA^{commit}" 2>/dev/null; then
  git fetch --no-tags --quiet "$REMOTE" refs/heads/main || die "cannot fetch the commit"
  git cat-file -e "$SHA^{commit}" 2>/dev/null || die "the commit is not available locally"
fi
export GIT_COMMITTER_NAME=eve-release-tagger
export GIT_COMMITTER_EMAIL=eve-release-tagger@users.noreply.github.com
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null 2>&1 && die "a local tag $TAG already exists; delete it first (git tag -d $TAG)"
git -c tag.gpgSign=false tag -a -m "Release $TAG" "$TAG" "$SHA"
if ! git push --no-verify "$REMOTE" "refs/tags/$TAG:refs/tags/$TAG"; then
  git tag -d "$TAG" >/dev/null 2>&1 || true
  die "the push of refs/tags/$TAG was rejected by the remote"
fi
note "pushed refs/tags/$TAG at $SHA; the release workflow takes it from here"
