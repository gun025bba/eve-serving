#!/bin/bash
# Release tagger: creates and pushes ONE annotated tag vX.Y.Z, using a deploy key that exists for this purpose only.
# Design, key location, permissions and revocation: docs/deploy-signing.md (section "The release-tag deploy key").
#
#   bash deploy/nopasswd/release-tag.sh X.Y.Z [FULL_SHA]        (run inside a clone of the repository)
#   ... --dry-run                                                (all checks, no tag, no push)
#
# FULL_SHA defaults to the tip of origin/main. The tag is pushed only when ALL of this holds, otherwise the script refuses (exit 1) and pushes nothing:
#   1. the target SHA equals the tip of main on the remote (read with `git ls-remote`, and read again just before the push);
#   2. a SUBMITTED review (state APPROVED or COMMENTED; never an issue/PR comment, a review line comment, a PENDING or DISMISSED review) of a PR of that commit, written by the
#      security reviewer (login AND numeric id, see the REVIEWER block below), has a line exactly `SECURITY-APPROVED: <the 40-hex target SHA>`, and no later submitted review
#      of the same reviewer has a line exactly `SECURITY-BLOCKED: <same SHA>`; the reviewer must not be the PR author, an author or committer of any commit of the PR or of the
#      target commit, the account `gh` is logged in as, or gallix-grok (the release bot);
#   3. the required checks `test` and `deploy-nopasswd-macos` finished with conclusion success for that SHA (latest run of each name, created by GitHub Actions);
#   4. the version is strictly greater than the highest existing vX.Y.Z tag on the remote.
#
# What it touches: the remote is reached ONLY through GIT_SSH_COMMAND (deploy key, no ssh config, GitHub's published host keys from deploy/nopasswd/github_known_hosts with
# StrictHostKeyChecking=yes, no agent, no prompts) and git's global/system config is switched off (no url.*.insteadOf rewriting); the only ref it ever pushes is
# refs/tags/vX.Y.Z (no branch, no --force, no --tags/--all/--mirror/--delete). `gh` is used for read-only GET requests and is never given the key. The key is never read, copied,
# printed or committed; only its path and mode (and its directory's) are checked.
# Portable: bash 3.2 and BSD tools (macOS); start it with `bash deploy/nopasswd/release-tag.sh ...` (not zsh). Needs jq (macOS 15 and newer ship it; otherwise `brew install jq`).
#
# Environment (the ONLY ones): EVE_RELEASE_TAG_KEY = path of the private key (default: $HOME/.eve-release-tagger/id_ed25519); it must be a regular file, mode 0600 or stricter,
# owned by you, in a directory owned by you without group/other access. The repository, the remote and the reviewer are constants below and cannot be set from the environment.
# Needs: bash, git, gh (authenticated for reads), jq, and ssh (openssh-client).
set -Eeuo pipefail
export LC_ALL=C
umask 077
die() { echo "release-tag: refused: $*" >&2; exit 1; }
note() { echo "release-tag: $*" >&2; }

# ================================================================================================================================================================
# CONSTANTS (not overridable from the environment). Changing any of them is a security-relevant change and needs a security review.
REPO="gun025bba/eve-serving"
REMOTE="git@github.com:${REPO}.git"
RELEASE_BOT_LOGIN="gallix-grok"     # the account that prepares releases; it can never be the approving reviewer
# ---- REVIEWER IDENTITY -------------------------------------------------------------------------------------------------------------------------------------------
# TODO(reviewer decision): the owner decides which GitHub account posts the security approval, and that account must be a different one from every account that writes
# code or runs releases. Replace BOTH values (login and numeric user id, `gh api users/<login> --jq .id`) in one reviewed change. Until then the script fails closed.
REVIEWER_LOGIN="__TODO_REVIEWER_DECISION__"
REVIEWER_ID="0"
# ---- END REVIEWER IDENTITY ---------------------------------------------------------------------------------------------------------------------------------------
# ================================================================================================================================================================

DRY=0 VERSION="" SHA=""
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    -*) die "unknown option '$a'" ;;
    *) if [ -z "$VERSION" ]; then VERSION=$a; elif [ -z "$SHA" ]; then SHA=$a; else die "too many arguments"; fi ;;
  esac
done
[ -n "$VERSION" ] || die "usage: release-tag.sh [--dry-run] X.Y.Z [FULL_SHA]"
VERSION=${VERSION#v}
NUM='(0|[1-9][0-9]{0,8})'
[[ "$VERSION" =~ ^$NUM\.$NUM\.$NUM$ ]] || die "version must be X.Y.Z without leading zeros: $VERSION"
TAG=v$VERSION

[[ "$REVIEWER_LOGIN" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || die "the reviewer identity is not set in this script yet (REVIEWER_LOGIN is a placeholder: TODO reviewer decision)"
[[ "$REVIEWER_ID" =~ ^[1-9][0-9]{0,15}$ ]] || die "the reviewer identity is not set in this script yet (REVIEWER_ID is a placeholder: TODO reviewer decision)"
LC_REVIEWER=$(printf '%s' "$REVIEWER_LOGIN" | tr 'A-Z' 'a-z')
[ "$LC_REVIEWER" != "$(printf '%s' "$RELEASE_BOT_LOGIN" | tr 'A-Z' 'a-z')" ] || die "the reviewer must not be the release bot account"
KEY=${EVE_RELEASE_TAG_KEY:-$HOME/.eve-release-tagger/id_ed25519}
KNOWN_HOSTS=$(cd "$(dirname "$0")" && pwd)/github_known_hosts
# GIT_SSH_COMMAND is parsed by a shell: keep the paths to plain characters
[[ "$KEY" =~ ^[A-Za-z0-9_./~+@:-]+$ ]] || die "key path has characters that are not allowed (letters, digits and _ . / ~ + @ : - only)"
[[ "$KNOWN_HOSTS" =~ ^[A-Za-z0-9_./~+@:-]+$ ]] || die "the path of the script has characters that are not allowed (letters, digits and _ . / ~ + @ : - only)"

for c in git gh jq; do command -v "$c" >/dev/null 2>&1 || die "'$c' is required but not installed"; done
git rev-parse --git-dir >/dev/null 2>&1 || die "run this inside a clone of the repository"
export GIT_TERMINAL_PROMPT=0
# no global/system git config: no url.*.insteadOf / pushInsteadOf rewriting of the remote, no helper, no hooks path (git 2.32 and newer honour these two variables)
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
unset GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_SSH GIT_PROXY_COMMAND
if git config --get-regexp '^url\..*\.(insteadof|pushinsteadof)$' >/dev/null 2>&1; then die "this clone's own git config rewrites URLs (url.*.insteadOf); use a clean clone"; fi

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
export GIT_SSH_COMMAND="ssh -F /dev/null -i $KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$KNOWN_HOSTS -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes -o IdentityAgent=none"

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
      note "ignoring a tag that is not vX.Y.Z: ${ref#refs/tags/}"
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

# ---- 4. version (cheap, before the API calls) --------------------------------------------------------------------------------------------------------
check_version

# ---- 2. the security reviewer approved this exact commit ----------------------------------------------------------------------------------------------
# Every request below is a plain GET. Only SUBMITTED PR reviews count (never issue comments, PR comments or review line comments).
ghget() { gh api --paginate "$1"; }
# who `gh` is logged in as on this machine
ME=$(ghget "user" | jq -r '"\(.login // "")|\(.id // 0)"') || die "cannot read the account gh is logged in as"
[[ "$ME" =~ ^[A-Za-z0-9-]+\|[1-9][0-9]*$ ]] || die "cannot determine the account gh is logged in as"
ME_LOGIN=${ME%%|*} ME_ID=${ME##*|}
# the reviewer is never the gh account of this machine (the release bot itself is rejected above, from the constants)
[ "$(printf '%s' "$ME_LOGIN" | tr 'A-Z' 'a-z')" != "$LC_REVIEWER" ] && [ "$ME_ID" != "$REVIEWER_ID" ] || die "the reviewer is the account gh is logged in as on this machine"
SHA_PEOPLE=$(ghget "repos/$REPO/commits/$SHA" | jq -r '[.author, .committer] | .[] | select(. != null) | "\((.login // "") | ascii_downcase)|\(.id // 0)"') || die "cannot read the target commit"
PRS=$(ghget "repos/$REPO/commits/$SHA/pulls" | jq -r '.[].number') || die "cannot list the pull requests of $SHA"
[ -n "$PRS" ] || die "no pull request is associated with $SHA, so there is no review to find"
APPROVE="SECURITY-APPROVED: $SHA"
BLOCK="SECURITY-BLOCKED: $SHA"
FOUND=""
for n in $PRS; do
  [[ "$n" =~ ^[0-9]+$ ]] || die "unexpected pull request number"
  # the reviewer is not a person who wrote this change: PR author, every commit author and committer (login or id), and the target commit
  PRAUTHOR=$(ghget "repos/$REPO/pulls/$n" | jq -r '.user | select(. != null and (.login // "") != "") | "\(.login | ascii_downcase)|\(.id // 0)"') || die "cannot read #$n"
  [ -n "$PRAUTHOR" ] || die "#$n has no author in the API answer"
  PRCOMMITS=$(ghget "repos/$REPO/pulls/$n/commits" | jq -r '.[] | (.author, .committer) | select(. != null) | "\((.login // "") | ascii_downcase)|\(.id // 0)"') || die "cannot read the commits of #$n"
  PEOPLE=$(printf '%s\n%s\n%s\n' "$PRAUTHOR" "$PRCOMMITS" "$SHA_PEOPLE")
  while IFS='|' read -r pl pid; do
    [ -n "$pl$pid" ] || continue
    [ "$pl" != "$LC_REVIEWER" ] && [ "$pid" != "$REVIEWER_ID" ] || die "the reviewer $REVIEWER_LOGIN wrote part of #$n (author, commit author or committer): its own review proves nothing"
  done <<<"$PEOPLE"
  # submitted reviews of the reviewer (login and id both match; PENDING never counts): the newest approval and the newest block of exactly this sha
  VERDICT=$(ghget "repos/$REPO/pulls/$n/reviews" | jq -rs --arg l "$LC_REVIEWER" --argjson i "$REVIEWER_ID" --arg a "$APPROVE" --arg b "$BLOCK" '
      def key: ((.submitted_at // "") + "|" + ("00000000000000000000" + (.id | tostring))[-20:]);
      def lines: (.body // "") | split("\n") | map(rtrimstr("\r"));
      [ .[][] | select((.user.login // "" | ascii_downcase) == $l and (.user.id // 0) == $i) | select(.state != "PENDING" and (.submitted_at // "") != "") ] as $mine
      | ([ $mine[] | select(.state == "APPROVED" or .state == "COMMENTED") | select(lines | any(. == $a)) | key ] | max) as $ok
      | ([ $mine[] | select(lines | any(. == $b)) | key ] | max) as $no
      | if $ok == null then "none" elif ($no != null and $no >= $ok) then "blocked" else "approved" end') || die "cannot read the reviews of #$n"
  [ "$VERDICT" = blocked ] && die "a later review by $REVIEWER_LOGIN on #$n blocks $SHA (SECURITY-BLOCKED)"
  if [ "$VERDICT" = approved ]; then FOUND=$n; break; fi
done
[ -n "$FOUND" ] || die "no submitted review by $REVIEWER_LOGIN has the line '$APPROVE' on the pull request(s) of that commit"
note "security review by $REVIEWER_LOGIN approves the commit (PR #$FOUND)"

# ---- 3. required checks passed for this commit -------------------------------------------------------------------------------------------------------
RUNS=$(ghget "repos/$REPO/commits/$SHA/check-runs?per_page=100" | jq -c '.check_runs[]') || die "cannot read the check runs of $SHA"
for want in test deploy-nopasswd-macos; do
  # newest run of that name from GitHub Actions (a re-run supersedes the earlier one); it must be completed with success
  got=$(printf '%s\n' "$RUNS" | jq -rs --arg n "$want" \
        '[.[] | select(.name == $n and (.app.slug // "") == "github-actions")] | sort_by(.id) | last | if . == null then "missing" else "\(.status)/\(.conclusion // "none")" end')
  [ "$got" = "completed/success" ] || die "required check '$want' is $got for $SHA (needs completed/success)"
done
note "checks test and deploy-nopasswd-macos passed for the commit"

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
