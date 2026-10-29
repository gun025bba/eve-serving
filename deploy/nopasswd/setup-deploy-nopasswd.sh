#!/bin/bash
# One-time install of the passwordless, CI-signed deploy path. Run it YOURSELF, ONCE, with sudo (it changes sudoers = a privilege boundary).
# The README gives the one line that downloads this file, checks its sha256 and pipes exactly the checked text into `sudo /bin/bash -s`.
# No key is generated or copied: releases are signed by CI (Sigstore keyless); this installs the verifier (cosign, pinned by sha256),
# the pinned trust root, the entrypoint, and a sudoers rule for exactly four fixed commands:
#   deploy | deploy-web | rollback | rollback-web      (exact arguments, no wildcard; all via /usr/local/libexec/eve/deploy)
# Remove it again with /usr/local/libexec/eve/uninstall, or by hand: sudo rm /etc/sudoers.d/eve-deploy. Never reads or prints secrets.
#   optional: --schema N  --current X.Y.Z  --current-web X.Y.Z   (otherwise: newest migration, the live gateway's own version, the live UI's version)
# Everything is inside main() and the file ends with the call, so when bash reads this script from a pipe nothing is left on stdin for a
# child command to swallow, and main() starts by pointing stdin at /dev/null.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
main() {
exec </dev/null
set -Eeuo pipefail
umask 022
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

# --- drop EVE_* overrides when running as root ---
# The EVE_* knobs below exist for the unprivileged tests. A root run must never honour them (they point installs, chain checks,
# sources and sudoers at other paths), so with uid 0 they are unset before anything reads them.
if [ "$(id -u)" = 0 ]; then for _v in $(compgen -e); do case "$_v" in EVE_*) unset "$_v";; esac; done; fi
unset _v
# --- end drop ---

# ----- pins: what gets installed, byte for byte (the README line pins the hash of THIS file) -----
SRC_COMMIT=b6e16d7e6bcfaafc2102f104487fe2023290be9e
SRC_RAW=https://raw.githubusercontent.com/gun025bba/eve-serving
COSIGN_URL=https://github.com/sigstore/cosign/releases/download/v3.1.4/cosign-darwin-arm64
SHA_COSIGN=9f9b8cd557cf2e5b3df3373a00baa005a86b02391e3e06dd0ce37b3a1533d3a4
SHA_DEPLOY=2cd051c1b9c140e49927eff97b95d4d12515d21c5ed2255d47274e35b7e17c4f
SHA_FETCH=fb90fc5252c7adba3263c1795a7353ada2ed29efe69001df914ab16482845c83
SHA_ROOT=6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66
SHA_UNINSTALL=2bde36730633cfe8624bcc3f5eec1924985a30563baa90e9290c56a9e9586d00
DEFAULT_SCHEMA=8

LIBEXEC=${EVE_LIBEXEC:-/usr/local/libexec/eve}            # EVE_* overrides exist for tests only; real runs use the defaults
STATE=${EVE_STATE:-/var/db/eve-deploy}
SUDOERS_D=${EVE_SUDOERS_D:-/etc/sudoers.d}
SUDOERS_MAIN=${EVE_SUDOERS_MAIN:-/etc/sudoers}
LOGF=${EVE_LOGF:-/var/log/eve-deploy.log}
OWNER=${EVE_OWNER:-root:wheel}
SKIP_ROOT=${EVE_SKIP_ROOT:-0}
BIN_DIR=${EVE_BIN_DIR:-/opt/eve-gw/bin}
WEB_DIR=${EVE_WEB_DIR:-/opt/eve-gw/web}
SRC_DIR=${EVE_SRC_DIR:-}                                   # tests: read the pinned files from here instead of downloading
COSIGN_FILE=${EVE_COSIGN_FILE:-}                           # tests: use this file instead of downloading cosign
CHAIN_UID=${EVE_CHAIN_UID:-0}                              # tests only: the sandbox is owned by the test user, not root
CHAIN_STOP=${EVE_CHAIN_STOP:-}                             # tests only: stop the parent walk here (/tmp is world-writable)
SKIP_FULL=${EVE_SKIP_FULL_VISUDO:-0}                       # tests only: a sandbox include dir is not root-owned
SKIP_ARCH=${EVE_SKIP_ARCH:-0}                              # tests only
ENTRY=$LIBEXEC/deploy
SUDOERS_FILE=$SUDOERS_D/eve-deploy

die() { echo "setup-deploy-nopasswd: $*" >&2; exit 1; }
sha_of() { if [ -x /usr/bin/shasum ]; then /usr/bin/shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }

SCHEMA=$DEFAULT_SCHEMA CUR="" CURW=""
while [ $# -gt 0 ]; do
  case "$1" in
    --schema) SCHEMA=${2:-}; shift 2;;
    --current) CUR=${2:-}; shift 2;;
    --current-web) CURW=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
V_RE='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'
[ "$SKIP_ROOT" = 1 ] || [ "$(id -u)" = 0 ] || die "run with sudo"
[ "$SKIP_ARCH" = 1 ] || { [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; } || die "this release path is for macOS on Apple silicon (darwin/arm64)"
USER_NAME=${SUDO_USER:-}
[ -n "$USER_NAME" ] && [ "$USER_NAME" != root ] && [ "$USER_NAME" != _evegw ] || die "run via 'sudo' from your own account (not root, not _evegw)"
[[ "$USER_NAME" =~ ^[A-Za-z_][A-Za-z0-9_.-]{0,31}$ ]] || die "unsupported user name"
U_UID=$(id -u "$USER_NAME") || die "unknown user"
[ "$U_UID" != 0 ] && [ "$U_UID" != 451 ] || die "user uid not allowed"
U_GID=$(id -g "$USER_NAME")
[[ "$SCHEMA" =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "--schema must be a number (newest goose migration)"
[ -z "$CUR" ] || [[ "$CUR" =~ $V_RE ]] || die "--current must be X.Y.Z"
[ -z "$CURW" ] || [[ "$CURW" =~ $V_RE ]] || die "--current-web must be X.Y.Z"

# 1. fetch the pinned files into a root-only dir and verify EVERY one against its pin before anything is installed
WORKD=""; TMP=""
cleanup() { [ -z "$WORKD" ] || rm -rf "$WORKD"; [ -z "$TMP" ] || rm -f "$TMP"; }   # ONE exit trap: a second `trap ... EXIT` would replace it
trap cleanup EXIT
WORKD=$(umask 077; mktemp -d "${TMPDIR:-/tmp}/eve-setup.XXXXXX") || die "mktemp failed"
chmod 0700 "$WORKD"
fetch() { # $1 name  $2 sha256  $3 url|file source
  if [ -n "$SRC_DIR" ] && [ "$1" != cosign ]; then cp "$SRC_DIR/$1" "$WORKD/$1" || die "cannot copy $1"
  elif [ "$1" = cosign ] && [ -n "$COSIGN_FILE" ]; then cp "$COSIGN_FILE" "$WORKD/$1" || die "cannot copy cosign"
  else curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --max-time 300 -o "$WORKD/$1" "$3" || die "download failed: $3"; fi
  [ "$(sha_of "$WORKD/$1")" = "$2" ] || die "$1: sha256 does not match the pin (got $(sha_of "$WORKD/$1"))"
}
fetch eve-deploy "$SHA_DEPLOY" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/eve-deploy"
fetch eve-fetch "$SHA_FETCH" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/eve-fetch"
fetch trusted_root.json "$SHA_ROOT" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/trusted_root.json"
fetch uninstall-deploy-nopasswd.sh "$SHA_UNINSTALL" "$SRC_RAW/$SRC_COMMIT/deploy/nopasswd/uninstall-deploy-nopasswd.sh"
fetch cosign "$SHA_COSIGN" "$COSIGN_URL"
bash -n "$WORKD/eve-deploy" && bash -n "$WORKD/eve-fetch" && bash -n "$WORKD/uninstall-deploy-nopasswd.sh" || die "a downloaded script has a syntax error"
# the entrypoint pins the same cosign and trust root that were just verified (so what is installed is what the entrypoint will accept)
grep -q "$SHA_COSIGN" "$WORKD/eve-deploy" && grep -q "^TRUSTED_ROOT_SHA256=$SHA_ROOT\$" "$WORKD/eve-deploy" || die "entrypoint pins differ from the installer's pins"
chmod 0755 "$WORKD/cosign"

# 2. every parent of what we install into must be root-owned and not group/world writable. Paths are resolved
#    physically first (macOS: /var is a symlink to /private/var), and both the logical and the physical chain are checked.
dir_ok() { local m; [ -d "$1" ] && [ "$(stat -L -c %u "$1" 2>/dev/null || stat -L -f %u "$1" 2>/dev/null)" = "$CHAIN_UID" ] || return 1
  m=$(stat -L -c %a "$1" 2>/dev/null || stat -L -f %Lp "$1" 2>/dev/null) || return 1; [ $(( 0$m & 0022 )) = 0 ]; }
walk_up() { local d=$1 stop=$2 stop_p=$3   # check d and every ancestor, up to (excluding) the test-only stop dir
  while :; do
    [ -z "$stop" ] || { [ "$d" != "$stop" ] && [ "$d" != "$stop_p" ] || return 0; }
    dir_ok "$d" || die "$d is not a root-owned directory that is closed to group/world writes"
    [ "$d" != / ] || return 0
    d=$(dirname "$d")
  done; }
chain_ok() { local p=$1 base=$1 phys stop_p=""
  [ ! -L "$p" ] || die "$p is a symlink"
  while [ ! -e "$base" ] && [ "$base" != / ]; do base=$(dirname "$base"); done
  [ -d "$base" ] || die "$base is not a directory"
  [ -z "$CHAIN_STOP" ] || stop_p=$(cd -P "$CHAIN_STOP" && pwd -P) || die "bad stop dir"
  phys=$(cd -P "$base" && pwd -P) || die "cannot resolve $base"
  walk_up "$base" "$CHAIN_STOP" "$stop_p"
  walk_up "$phys" "$stop_p" "$stop_p"; }
chain_ok "$LIBEXEC"; chain_ok "$STATE"
[ -d "$BIN_DIR" ] && [ -f "$BIN_DIR/gateway" ] && [ ! -L "$BIN_DIR/gateway" ] || die "$BIN_DIR/gateway not found: install the gateway first (the runbooks do)"
chain_ok "$BIN_DIR"; dir_ok "$BIN_DIR" || die "$BIN_DIR is not root-owned / is group-world writable"
[ -d "$WEB_DIR" ] && [ ! -L "$WEB_DIR" ] || die "$WEB_DIR not found: install the UI first (the runbooks do)"
chain_ok "$WEB_DIR"; dir_ok "$WEB_DIR" || die "$WEB_DIR is not root-owned / is group-world writable"
[ -f "$BIN_DIR/eve-gw" ] || echo "note: no $BIN_DIR/eve-gw on this host: bundles without eve-gw are fine, the entrypoint only touches it when present"
INC_RE=$(printf '%s' "$SUDOERS_D" | sed 's/[.[\*^$]/\\&/g')
grep -Eq "^[#@]includedir[[:space:]]+(/private)?$INC_RE\$" "$SUDOERS_MAIN" || die "$SUDOERS_MAIN does not include $SUDOERS_D"
[ ! -e "$SUDOERS_FILE" ] || echo "note: replacing existing $SUDOERS_FILE"

# 3. versions the monotonic check starts from: the live gateway's own version, the live UI's own version (the caller can override)
if [ ! -f "$STATE/current_version" ]; then
  if [ -z "$CUR" ]; then CUR=$("$BIN_DIR/gateway" -version 2>/dev/null </dev/null | head -1 | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/' || true); fi
  [[ "$CUR" =~ $V_RE ]] || die "cannot read the live gateway version: pass --current X.Y.Z"
fi
if [ ! -f "$STATE/current_web_version" ]; then
  if [ -z "$CURW" ] && [ -f "$WEB_DIR/sw.js" ]; then CURW=$(sed -n -E "s/^const CACHE = 'llmchat-([0-9]+\.[0-9]+\.[0-9]+)'.*/\1/p" "$WEB_DIR/sw.js" | head -1 || true); fi
  [[ "$CURW" =~ $V_RE ]] || die "cannot read the live UI version: pass --current-web X.Y.Z"
fi

# 4. files (only the verified copies)
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$LIBEXEC"
[ ! -L "$LIBEXEC/staging" ] || die "$LIBEXEC/staging is a symlink"
for pair in eve-deploy:deploy eve-fetch:fetch uninstall-deploy-nopasswd.sh:uninstall cosign:cosign; do
  install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$WORKD/${pair%%:*}" "$LIBEXEC/${pair##*:}.new" && mv -f "$LIBEXEC/${pair##*:}.new" "$LIBEXEC/${pair##*:}"
done
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0644 "$WORKD/trusted_root.json" "$LIBEXEC/trusted_root.json.new" && mv -f "$LIBEXEC/trusted_root.json.new" "$LIBEXEC/trusted_root.json"
rm -f "$LIBEXEC/allowed_signers"                                  # the old laptop-key trust root (PR #90) is gone: no key can sign a deploy any more
install -d -o "$USER_NAME" -g "$U_GID" -m 0700 "$LIBEXEC/staging"
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0700 "$STATE"
if [ ! -f "$STATE/current_version" ]; then ( umask 077; echo "$CUR" >"$STATE/current_version"; echo "$SCHEMA" >"$STATE/schema" )
else echo "note: keeping existing $STATE/current_version ($(head -1 "$STATE/current_version"))"; fi
if [ ! -f "$STATE/current_web_version" ]; then ( umask 077; echo "$CURW" >"$STATE/current_web_version" )
else echo "note: keeping existing $STATE/current_web_version ($(head -1 "$STATE/current_web_version"))"; fi
( umask 077; echo "$USER_NAME" >"$STATE/allowed_user" )
[ -L "$LOGF" ] && die "$LOGF is a symlink"
( umask 077; : >>"$LOGF" ); chown "$OWNER" "$LOGF"; chmod 0600 "$LOGF"          # BSD install rejects /dev/null as a source

# 5. sudoers: one user, one exact path, four exact argument lists (no wildcard), no SETENV, env reset. Validated before and after install.
TMP=$(mktemp "${TMPDIR:-/tmp}/eve-sudoers.XXXXXX") || die "mktemp failed"
cat >"$TMP" <<SUD
# Managed by setup-deploy-nopasswd.sh. Remove with $LIBEXEC/uninstall or: rm $SUDOERS_FILE
Defaults!$ENTRY env_reset, !setenv
$USER_NAME ALL=(root) NOPASSWD: NOSETENV: $ENTRY deploy, $ENTRY deploy-web, $ENTRY rollback, $ENTRY rollback-web
SUD
visudo -c -q -f "$TMP" || die "sudoers snippet failed visudo -c (nothing installed)"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0440 "$TMP" "$SUDOERS_FILE.new"
visudo -c -q -f "$SUDOERS_FILE.new" || { rm -f "$SUDOERS_FILE.new"; die "installed snippet failed visudo -c"; }
mv -f "$SUDOERS_FILE.new" "$SUDOERS_FILE"
[ "$SKIP_FULL" = 1 ] || visudo -c -q -f "$SUDOERS_MAIN" || { rm -f "$SUDOERS_FILE"; die "visudo -c failed on the full config: removed $SUDOERS_FILE"; }

echo "installed cosign $(sha_of "$LIBEXEC/cosign" | cut -c1-12)…, trust root $(sha_of "$LIBEXEC/trusted_root.json" | cut -c1-12)…, entrypoint $(sha_of "$ENTRY" | cut -c1-12)…"
echo "OK. From now on, WITHOUT a password and only for releases signed by the pinned CI workflow (user: $USER_NAME):"
echo "  $LIBEXEC/fetch gateway <X.Y.Z> && sudo $ENTRY deploy"
echo "  $LIBEXEC/fetch web <X.Y.Z>     && sudo $ENTRY deploy-web"
echo "  sudo $ENTRY rollback | rollback-web"
echo "Remove: sudo $LIBEXEC/uninstall   (or: sudo rm $SUDOERS_FILE)"
}
main "$@"
