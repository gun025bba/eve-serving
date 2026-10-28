#!/bin/bash
# One-time install of the passwordless deploy path. Run it YOURSELF with sudo (it changes sudoers = a privilege boundary):
#   sudo bash setup-deploy-nopasswd.sh --expect-sha256 <sha256 of eve-deploy> --expect-key-fingerprint SHA256:<...> \
#        --pubkey <signing key .pub> --current <X.Y.Z> --schema <N>
# After this, ONLY `sudo /usr/local/libexec/eve/deploy deploy <X.Y.Z>` and `... rollback` run without a password, and only
# for releases signed by the pinned key. Remove it with uninstall-deploy-nopasswd.sh. It never reads or prints secrets.
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
set -Eeuo pipefail
umask 022
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

HERE=$(cd "$(dirname "$0")" && pwd)
LIBEXEC=${EVE_LIBEXEC:-/usr/local/libexec/eve}            # EVE_* overrides exist for tests only; real runs use the defaults
STATE=${EVE_STATE:-/var/db/eve-deploy}
SUDOERS_D=${EVE_SUDOERS_D:-/etc/sudoers.d}
SUDOERS_MAIN=${EVE_SUDOERS_MAIN:-/etc/sudoers}
LOGF=${EVE_LOGF:-/var/log/eve-deploy.log}
OWNER=${EVE_OWNER:-root:wheel}
SKIP_ROOT=${EVE_SKIP_ROOT:-0}
BIN_DIR=${EVE_BIN_DIR:-/opt/eve-gw/bin}
CHAIN_UID=${EVE_CHAIN_UID:-0}                              # tests only: the sandbox is owned by the test user, not root
CHAIN_STOP=${EVE_CHAIN_STOP:-}                             # tests only: stop the parent walk here (/tmp is world-writable)
SKIP_FULL=${EVE_SKIP_FULL_VISUDO:-0}                       # tests only: a sandbox include dir is not root-owned
ENTRY=$LIBEXEC/deploy
SUDOERS_FILE=$SUDOERS_D/eve-deploy

die() { echo "setup-deploy-nopasswd: $*" >&2; exit 1; }
sha_of() { if [ -x /usr/bin/shasum ]; then /usr/bin/shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }

EXPECT="" PUB="" CUR="" SCHEMA="" EXPECT_FP=""
while [ $# -gt 0 ]; do
  case "$1" in
    --expect-sha256) EXPECT=${2:-}; shift 2;;
    --expect-key-fingerprint) EXPECT_FP=${2:-}; shift 2;;
    --pubkey) PUB=${2:-}; shift 2;;
    --current) CUR=${2:-}; shift 2;;
    --schema) SCHEMA=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
[ -n "$EXPECT" ] && [ -n "$EXPECT_FP" ] && [ -n "$PUB" ] && [ -n "$CUR" ] && [ -n "$SCHEMA" ] || die "need --expect-sha256 --expect-key-fingerprint --pubkey --current --schema"
[[ "$EXPECT_FP" =~ ^SHA256:[A-Za-z0-9+/]{43}$ ]] || die "--expect-key-fingerprint must look like SHA256:<43 chars> (ssh-keygen -l -f key.pub)"
[ "$SKIP_ROOT" = 1 ] || [ "$(id -u)" = 0 ] || die "run with sudo"
USER_NAME=${SUDO_USER:-}
[ -n "$USER_NAME" ] && [ "$USER_NAME" != root ] && [ "$USER_NAME" != _evegw ] || die "run via 'sudo' from your own account (not root, not _evegw)"
[[ "$USER_NAME" =~ ^[A-Za-z_][A-Za-z0-9_.-]{0,31}$ ]] || die "unsupported user name"
U_UID=$(id -u "$USER_NAME") || die "unknown user"
[ "$U_UID" != 0 ] && [ "$U_UID" != 451 ] || die "user uid not allowed"
U_GID=$(id -g "$USER_NAME")
[[ "$CUR" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]] || die "--current must be X.Y.Z"
[[ "$SCHEMA" =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "--schema must be a number (newest goose migration)"

# 1. take private copies first (root-only dir), then verify and install ONLY the copies: no check-then-use gap on user-owned files
WORKD=$(umask 077; mktemp -d "${TMPDIR:-/tmp}/eve-setup.XXXXXX") || die "mktemp failed"
trap 'rm -rf "$WORKD"' EXIT
chmod 0700 "$WORKD"
SRC=$HERE/eve-deploy
[ -f "$SRC" ] && [ ! -L "$SRC" ] || die "$SRC missing"
[ -f "$PUB" ] && [ ! -L "$PUB" ] || die "pubkey file missing"
cp "$SRC" "$WORKD/eve-deploy" && cp "$PUB" "$WORKD/pub" || die "cannot copy inputs"
COPY=$WORKD/eve-deploy
[ "$(sha_of "$COPY")" = "$EXPECT" ] || die "eve-deploy sha256 does not match --expect-sha256 (got $(sha_of "$COPY"))"
bash -n "$COPY" || die "eve-deploy has a syntax error"

# 2. the public key (the copy): one line, ed25519, and exactly the fingerprint you checked
[ "$(wc -l <"$WORKD/pub" | tr -d ' ')" -le 1 ] || die "pubkey file must hold exactly one key"
read -r KT KB _ <"$WORKD/pub" || true
[ "${KT:-}" = ssh-ed25519 ] && [[ "${KB:-}" =~ ^[A-Za-z0-9+/=]{60,100}$ ]] || die "pubkey must be a single ssh-ed25519 public key"
FP=$(ssh-keygen -l -f "$WORKD/pub" 2>/dev/null | awk '{print $2}') || FP=""
[ -n "$FP" ] || die "ssh-keygen cannot read the public key"
echo "signing key fingerprint: $FP"
[ "$FP" = "$EXPECT_FP" ] || die "key fingerprint $FP does not match --expect-key-fingerprint $EXPECT_FP"

# 3. every parent of what we install into must be root-owned and not group/world writable. Paths are resolved
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
[ -f "$BIN_DIR/eve-gw" ] || echo "note: no $BIN_DIR/eve-gw on this host: bundles without eve-gw are fine, the entrypoint only touches it when present"
INC_RE=$(printf '%s' "$SUDOERS_D" | sed 's/[.[\*^$]/\\&/g')
grep -Eq "^[#@]includedir[[:space:]]+(/private)?$INC_RE\$" "$SUDOERS_MAIN" || die "$SUDOERS_MAIN does not include $SUDOERS_D"
[ ! -e "$SUDOERS_FILE" ] || echo "note: replacing existing $SUDOERS_FILE"

# 4. files
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$LIBEXEC"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$COPY" "$ENTRY.new" && mv -f "$ENTRY.new" "$ENTRY"
[ ! -L "$LIBEXEC/staging" ] || die "$LIBEXEC/staging is a symlink"
printf 'eve-release namespaces="eve-deploy" %s %s\n' "$KT" "$KB" >"$LIBEXEC/allowed_signers.new"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0644 "$LIBEXEC/allowed_signers.new" "$LIBEXEC/allowed_signers"; rm -f "$LIBEXEC/allowed_signers.new"
install -d -o "$USER_NAME" -g "$U_GID" -m 0700 "$LIBEXEC/staging"
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0700 "$STATE"
if [ ! -f "$STATE/current_version" ]; then
  ( umask 077; echo "$CUR" >"$STATE/current_version"; echo "$SCHEMA" >"$STATE/schema" )
else echo "note: keeping existing $STATE/current_version ($(head -1 "$STATE/current_version"))"; fi
( umask 077; echo "$USER_NAME" >"$STATE/allowed_user" )
[ -L "$LOGF" ] && die "$LOGF is a symlink"
( umask 077; : >>"$LOGF" ); chown "$OWNER" "$LOGF"; chmod 0600 "$LOGF"          # BSD install rejects /dev/null as a source

# 5. sudoers: one user, one exact path, no wildcard, no SETENV, env reset. Validated before and after install.
TMP=$(mktemp "${TMPDIR:-/tmp}/eve-sudoers.XXXXXX"); trap 'rm -f "$TMP"' EXIT
cat >"$TMP" <<SUD
# Managed by setup-deploy-nopasswd.sh. Remove with uninstall-deploy-nopasswd.sh.
Defaults!$ENTRY env_reset, !setenv
$USER_NAME ALL=(root) NOPASSWD: NOSETENV: $ENTRY
SUD
visudo -c -q -f "$TMP" || die "sudoers snippet failed visudo -c (nothing installed)"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0440 "$TMP" "$SUDOERS_FILE.new"
visudo -c -q -f "$SUDOERS_FILE.new" || { rm -f "$SUDOERS_FILE.new"; die "installed snippet failed visudo -c"; }
mv -f "$SUDOERS_FILE.new" "$SUDOERS_FILE"
[ "$SKIP_FULL" = 1 ] || visudo -c -q -f "$SUDOERS_MAIN" || { rm -f "$SUDOERS_FILE"; die "visudo -c failed on the full config: removed $SUDOERS_FILE"; }

echo "OK. From now on (no password): sudo $ENTRY deploy <X.Y.Z> | rollback   (user: $USER_NAME)"
echo "Stage bundles in $LIBEXEC/staging as eve-<X.Y.Z>.tar + eve-<X.Y.Z>.tar.sig (made by sign-deploy-bundle.sh on the signing machine)."
echo "Remove: sudo bash uninstall-deploy-nopasswd.sh"
