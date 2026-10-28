#!/bin/bash
# Packs one release artifact and writes its MANIFEST (the file CI then signs keylessly). Runs in CI (.github/workflows/deploy-sign.yml);
# no key material is involved anywhere. The MANIFEST is the ONLY thing that is signed; it carries the sha256 of the tar and of
# every file inside it, so a signature on the MANIFEST covers all artifacts.
#
#   make-release.sh --kind gateway --version X.Y.Z --git-sha <40 hex> --repo owner/name --ref refs/tags/vX.Y.Z --branch <main|release/X.Y.Z> \
#                   --schema N --gateway <file> [--eve-gw <file>] --out <dir>
#   make-release.sh --kind web     ... --web-dir <checkout> [--web-exclude <ERE>] --out <dir>
#
# Output: <out>/eve-<kind>-<version>.tar and <out>/eve-<kind>-<version>.MANIFEST (key=value lines, allow-listed by eve-deploy).
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
export LC_ALL=C COPYFILE_DISABLE=1
die() { echo "make-release: $*" >&2; exit 1; }
sha_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }
FIXTURE=0
KIND="" VER="" SHA="" REPO="" REF="" BRANCH="" SCHEMA="" GW="" EG="" WEBDIR="" OUT=""
EXCL='^(\.github/|tests/|review-shots/|README\.md$|\.gitignore$|\.gitattributes$)'
while [ $# -gt 0 ]; do
  case "$1" in
    --kind) KIND=${2:-}; shift 2;; --version) VER=${2:-}; shift 2;; --git-sha) SHA=${2:-}; shift 2;; --repo) REPO=${2:-}; shift 2;;
    --ref) REF=${2:-}; shift 2;; --branch) BRANCH=${2:-}; shift 2;; --schema) SCHEMA=${2:-}; shift 2;;
    --gateway) GW=${2:-}; shift 2;; --eve-gw) EG=${2:-}; shift 2;; --web-dir) WEBDIR=${2:-}; shift 2;;
    --web-exclude) EXCL=${2:-}; shift 2;; --out) OUT=${2:-}; shift 2;;
    --fixture) FIXTURE=1; shift;;       # CI test fixtures only: any ref/branch name (the verifier rejects such a manifest in production)
    *) die "unknown argument: $1";;
  esac
done
[ "$KIND" = gateway ] || [ "$KIND" = web ] || die "--kind must be gateway or web"
[[ "$VER" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]] || die "--version must be X.Y.Z"
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "--git-sha must be 40 lowercase hex"
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "--repo must be owner/name"
if [ "$FIXTURE" = 1 ]; then
  [[ "$REF" =~ ^refs/[A-Za-z0-9/_.-]+$ ]] && [[ "$BRANCH" =~ ^[A-Za-z0-9/_.-]+$ ]] || die "bad --ref/--branch"
else
  [ "$REF" = "refs/tags/v$VER" ] || die "--ref must be refs/tags/v$VER"
  [[ "$BRANCH" =~ ^(main|release/(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8}))$ ]] || die "--branch must be main or release/X.Y.Z"
fi
[ -n "$OUT" ] || die "--out is required"
mkdir -p "$OUT" && OUT=$(cd "$OUT" && pwd -P) || die "cannot create --out"
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
T=$OUT/eve-$KIND-$VER.tar; M=$OUT/eve-$KIND-$VER.MANIFEST
rm -f "$T" "$M"
EPOCH=${SOURCE_DATE_EPOCH:-$(date +%s)}
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
  tarflags=(--owner=0 --group=0 --numeric-owner --sort=name "--mtime=@$EPOCH" --format=gnu)
else tarflags=(--uid 0 --gid 0); fi
{
  echo "eve-deploy-manifest=2"; echo "kind=$KIND"; echo "version=$VER"; echo "git_sha=$SHA"; echo "repo=$REPO"; echo "ref=$REF"
  echo "branch=$BRANCH"; echo "built_at=$(date -u -d "@$EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
} >"$W/MANIFEST"
if [ "$KIND" = gateway ]; then
  [[ "$SCHEMA" =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "--schema must be a number"
  [ -f "$GW" ] && [ -s "$GW" ] && [ ! -L "$GW" ] || die "gateway file missing or empty"
  [ -z "$EG" ] || { [ -f "$EG" ] && [ -s "$EG" ] && [ ! -L "$EG" ]; } || die "eve-gw file missing or empty"
  mkdir "$W/pack"; cp "$GW" "$W/pack/gateway"; chmod 0755 "$W/pack/gateway"
  [ -z "$EG" ] || { cp "$EG" "$W/pack/eve-gw"; chmod 0755 "$W/pack/eve-gw"; }
  members=(gateway); [ -z "$EG" ] || members=(eve-gw gateway)
  ( cd "$W/pack" && tar "${tarflags[@]}" -cf "$T" "${members[@]}" )
  { echo "schema=$SCHEMA"; echo "target=darwin-arm64"; echo "sha256.gateway=$(sha_of "$W/pack/gateway")"
    [ -z "$EG" ] || echo "sha256.eve-gw=$(sha_of "$W/pack/eve-gw")"; } >>"$W/MANIFEST"
else
  [ -d "$WEBDIR" ] || die "--web-dir is not a directory"
  # tracked files only (never untracked/ignored ones), regular files only: no symlinks, no submodules, no odd names
  ( cd "$WEBDIR" && git ls-files -s -z ) | tr '\0' '\n' >"$W/stage.txt"
  if grep -E '^(120000|160000) ' "$W/stage.txt" >/dev/null; then die "web tree has symlinks or submodules"; fi
  ( cd "$WEBDIR" && git ls-files -z ) | tr '\0' '\n' | grep -Ev "$EXCL" >"$W/list.txt" || true
  [ -s "$W/list.txt" ] || die "no files to pack"
  grep -qx 'index.html' "$W/list.txt" || die "no index.html in the web tree"
  grep -Ev '^[A-Za-z0-9_@+-][A-Za-z0-9._@+/-]*$' "$W/list.txt" | head -3 | grep . >/dev/null && die "web tree has file names the deploy entrypoint would refuse (only [A-Za-z0-9._@+/-], not starting with . or -)"
  [ "$(wc -l <"$W/list.txt" | tr -d ' ')" -le 5000 ] || die "too many files"
  ( cd "$WEBDIR" && tar "${tarflags[@]}" -cf "$T" -T "$W/list.txt" )
fi
echo "sha256.bundle=$(sha_of "$T")" >>"$W/MANIFEST"
cp "$W/MANIFEST" "$M"
echo "packed: $T"; echo "manifest: $M"; cat "$M"
