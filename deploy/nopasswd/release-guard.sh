#!/bin/bash
# Release guard: the FIRST thing the signing workflow (.github/workflows/deploy-sign.yml) runs, unconditionally, in the build job. It
# refuses (exit 1) unless this run is the push of a tag vX.Y.Z whose commit is exactly the tip of origin/main. Always executed from the
# main checkout (`.eve-tools`), never from the tagged commit; the working directory is the checkout of the tagged commit.
#
# Inputs (environment, all taken from the workflow context): KIND EVENT REF SHA WEB_DIR MIGRATIONS_DIR; optional GITHUB_OUTPUT.
# Output: `version=X.Y.Z` (appended to $GITHUB_OUTPUT when set, always printed).
# There is no bypass: no fixture mode, no environment switch, and no branch other than the tip of main is ever signed (release/* branches never sign).
set -Eeuo pipefail
export LC_ALL=C
die() { echo "release-guard: $*" >&2; exit 1; }
: "${KIND:=}" "${EVENT:=}" "${REF:=}" "${SHA:=}" "${WEB_DIR:=.}" "${MIGRATIONS_DIR:=.}"
case "$KIND" in gateway|web) ;; *) die "bad kind";; esac
[ "$EVENT" = push ] || die "releases are built from a tag push only (got '$EVENT')"
[[ "$REF" =~ ^refs/tags/v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]] || die "ref is not refs/tags/vX.Y.Z: $REF"
VER=${REF#refs/tags/v}
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "bad commit sha"
# the caller picks these two paths: plain relative paths inside the checkout only
for p in "$WEB_DIR" "$MIGRATIONS_DIR"; do
  [[ "$p" =~ ^[A-Za-z0-9_./-]+$ ]] && [[ "$p" != /* ]] && [[ ! "/$p/" =~ /\.\./ ]] || die "unsafe path input: $p"
done
[ "$(git rev-parse HEAD)" = "$SHA" ] || die "checked-out commit != $SHA"
# main as it is now (the checkout just fetched all branches, no credentials are kept); fail closed when it is missing
TIP=$(git rev-parse --verify -q refs/remotes/origin/main) || die "origin/main is not available in this checkout"
git merge-base --is-ancestor "$SHA" "$TIP" || die "commit $SHA is not on main: refusing to sign"
[ "$SHA" = "$TIP" ] || die "commit $SHA is not the tip of main ($TIP): tag the current tip of main (main moved since this tag was created?)"
echo "version=$VER"
if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "version=$VER" >>"$GITHUB_OUTPUT"; fi
