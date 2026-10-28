#!/bin/bash
# Release tagger: creates and pushes ONE annotated tag vX.Y.Z, using a deploy key that exists for this purpose only.
# Design, key location, permissions and revocation: docs/deploy-signing.md (section "The release-tag deploy key").
#
#   EVE_SECURITY_REVIEWER=<github login> bash deploy/nopasswd/release-tag.sh X.Y.Z [FULL_SHA]      (run inside a clone of the repository)
#   ... --dry-run                                                                                     (all checks, no tag, no push)
#
# FULL_SHA defaults to the tip of origin/main. The tag is pushed only when ALL of this holds, otherwise the script refuses (exit 1) and pushes nothing:
#   1. the target SHA equals the tip of main on the remote (read with `git ls-remote`, and read again just before the push);
#   2. a PR for that commit carries a review, a review comment or a PR comment by the security reviewer whose text names the full 40-character SHA
#      (state DISMISSED, CHANGES_REQUESTED and PENDING reviews never count; the login is compared case-insensitively);
#   3. the required checks `test` and `deploy-nopasswd-macos` finished with conclusion success for that SHA (latest run of each name, created by GitHub Actions);
#   4. the version is strictly greater than the highest existing vX.Y.Z tag on the remote.
#
# What it touches: the remote is reached ONLY through GIT_SSH_COMMAND='ssh -i <key> -o IdentitiesOnly=yes'; the only ref it ever pushes is refs/tags/vX.Y.Z (no
# branch, no --force, no --tags/--all/--mirror/--delete). `gh` is used for read-only GET requests and is never given the key. The key is never read, copied,
# printed or committed; only its path and mode are checked.
# Portable: bash 3.2 and BSD tools (macOS); start it with `bash deploy/nopasswd/release-tag.sh ...` (not zsh). Needs jq (macOS 15 and newer ship it; otherwise `brew install jq`).
#
# Environment:
#   EVE_SECURITY_REVIEWER   REQUIRED. GitHub login of the security reviewer (no default; see docs/deploy-signing.md for how to determine it).
#   EVE_RELEASE_TAG_KEY     path of the private key (default: $HOME/.eve-release-tagger/id_ed25519); must be a regular file, mode 0600 or stricter, owned by you.
#   EVE_REPO                owner/name (default: gun025bba/eve-serving).
#   EVE_TAGGER_NAME / EVE_TAGGER_EMAIL   identity written into the annotated tag (defaults: eve-release-tagger / a noreply address).
#   EVE_RELEASE_TAG_REMOTE  TEST HOOK ONLY: replaces git@github.com:<repo>.git (the offline tests point it at a local bare repository).
# Needs: bash, git, gh (authenticated for reads), jq, and ssh (openssh-client) for the real remote.
set -Eeuo pipefail
export LC_ALL=C
umask 077
die() { echo "release-tag: refused: $*" >&2; exit 1; }
note() { echo "release-tag: $*" >&2; }

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

REVIEWER=${EVE_SECURITY_REVIEWER:-}
[ -n "$REVIEWER" ] || die "EVE_SECURITY_REVIEWER is not set (the GitHub login whose review must name the commit)"
[[ "$REVIEWER" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\[bot\])?$ ]] || die "EVE_SECURITY_REVIEWER is not a valid GitHub login"
REPO=${EVE_REPO:-gun025bba/eve-serving}
[[ "$REPO" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || die "EVE_REPO must be owner/name"
REMOTE=${EVE_RELEASE_TAG_REMOTE:-git@github.com:$REPO.git}
KEY=${EVE_RELEASE_TAG_KEY:-$HOME/.eve-release-tagger/id_ed25519}
# GIT_SSH_COMMAND is parsed by a shell: keep the path to plain characters
[[ "$KEY" =~ ^[A-Za-z0-9_./~+@:-]+$ ]] || die "key path has characters that are not allowed (letters, digits and _ . / ~ + @ : - only)"

for c in git gh jq; do command -v "$c" >/dev/null 2>&1 || die "'$c' is required but not installed"; done
git rev-parse --git-dir >/dev/null 2>&1 || die "run this inside a clone of the repository"
export GIT_TERMINAL_PROMPT=0

command -v ssh >/dev/null 2>&1 || die "'ssh' is not installed (install openssh-client); the tag is pushed over ssh with the deploy key"
modeof() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
ownerof() { stat -c %u "$1" 2>/dev/null || stat -f %u "$1"; }
[ -f "$KEY" ] && [ ! -L "$KEY" ] || die "the deploy key is not a regular file at the configured path"
[ "$(ownerof "$KEY")" = "$(id -u)" ] || die "the deploy key is not owned by the current user"
case "$(modeof "$KEY")" in 600|400) ;; *) die "the deploy key must have mode 0600 (or 0400)" ;; esac
export GIT_SSH_COMMAND="ssh -i $KEY -o IdentitiesOnly=yes"

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

# ---- 2. the security reviewer named this exact commit ------------------------------------------------------------------------------------------------
# Every request below is a plain GET.
ghget() { gh api --paginate "$1"; }
PRS=$(ghget "repos/$REPO/commits/$SHA/pulls" | jq -r '.[].number') || die "cannot list the pull requests of $SHA"
[ -n "$PRS" ] || die "no pull request is associated with $SHA, so there is no review to find"
FOUND=""
for n in $PRS; do
  [[ "$n" =~ ^[0-9]+$ ]] || die "unexpected pull request number"
  # reviews: the body names the sha; DISMISSED, CHANGES_REQUESTED and PENDING do not count
  r1=$(ghget "repos/$REPO/pulls/$n/reviews" | jq -r --arg u "$REVIEWER" --arg s "$SHA" \
       '.[] | select((.user.login // "" | ascii_downcase) == ($u | ascii_downcase)) | select(.state == "APPROVED" or .state == "COMMENTED") | select((.body // "" | ascii_downcase) | contains($s)) | .id') || die "cannot read the reviews of #$n"
  r2=$(ghget "repos/$REPO/pulls/$n/comments" | jq -r --arg u "$REVIEWER" --arg s "$SHA" \
       '.[] | select((.user.login // "" | ascii_downcase) == ($u | ascii_downcase)) | select((.body // "" | ascii_downcase) | contains($s)) | .id') || die "cannot read the review comments of #$n"
  r3=$(ghget "repos/$REPO/issues/$n/comments" | jq -r --arg u "$REVIEWER" --arg s "$SHA" \
       '.[] | select((.user.login // "" | ascii_downcase) == ($u | ascii_downcase)) | select((.body // "" | ascii_downcase) | contains($s)) | .id') || die "cannot read the comments of #$n"
  if [ -n "$r1$r2$r3" ]; then FOUND=$n; break; fi
done
[ -n "$FOUND" ] || die "no review or comment by $REVIEWER names $SHA on the pull request(s) of that commit"
note "security review by $REVIEWER names the commit (PR #$FOUND)"

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
export GIT_COMMITTER_NAME=${EVE_TAGGER_NAME:-eve-release-tagger}
export GIT_COMMITTER_EMAIL=${EVE_TAGGER_EMAIL:-eve-release-tagger@users.noreply.github.com}
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null 2>&1 && die "a local tag $TAG already exists; delete it first (git tag -d $TAG)"
git -c tag.gpgSign=false tag -a -m "Release $TAG" "$TAG" "$SHA"
if ! git push --no-verify "$REMOTE" "refs/tags/$TAG:refs/tags/$TAG"; then
  git tag -d "$TAG" >/dev/null 2>&1 || true
  die "the push of refs/tags/$TAG was rejected by the remote"
fi
note "pushed refs/tags/$TAG at $SHA; the release workflow takes it from here"
