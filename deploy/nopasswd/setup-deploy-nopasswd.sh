#!/bin/bash
# One-time install of the passwordless deploy path. Run it YOURSELF with sudo (it changes sudoers = a privilege boundary):
#   sudo bash setup-deploy-nopasswd.sh --expect-sha256 <sha256 of eve-deploy> --pubkey <signing key .pub> --current <X.Y.Z> --schema <N>
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
SKIP_CHAIN=${EVE_SKIP_CHAIN:-0}
ENTRY=$LIBEXEC/deploy
SUDOERS_FILE=$SUDOERS_D/eve-deploy

die() { echo "setup-deploy-nopasswd: $*" >&2; exit 1; }
sha_of() { if [ -x /usr/bin/shasum ]; then /usr/bin/shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi; }

EXPECT="" PUB="" CUR="" SCHEMA=""
while [ $# -gt 0 ]; do
  case "$1" in
    --expect-sha256) EXPECT=${2:-}; shift 2;;
    --pubkey) PUB=${2:-}; shift 2;;
    --current) CUR=${2:-}; shift 2;;
    --schema) SCHEMA=${2:-}; shift 2;;
    *) die "unknown argument: $1";;
  esac
done
[ -n "$EXPECT" ] && [ -n "$PUB" ] && [ -n "$CUR" ] && [ -n "$SCHEMA" ] || die "need --expect-sha256 --pubkey --current --schema"
[ "$SKIP_ROOT" = 1 ] || [ "$(id -u)" = 0 ] || die "run with sudo"
USER_NAME=${SUDO_USER:-}
[ -n "$USER_NAME" ] && [ "$USER_NAME" != root ] && [ "$USER_NAME" != _evegw ] || die "run via 'sudo' from your own account (not root, not _evegw)"
[[ "$USER_NAME" =~ ^[a-z_][a-z0-9_.-]{0,31}$ ]] || die "unsupported user name"
U_UID=$(id -u "$USER_NAME") || die "unknown user"
[ "$U_UID" != 0 ] && [ "$U_UID" != 451 ] || die "user uid not allowed"
U_GID=$(id -g "$USER_NAME")
[[ "$CUR" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]] || die "--current must be X.Y.Z"
[[ "$SCHEMA" =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "--schema must be a number (newest goose migration)"

# 1. the entrypoint must be exactly the reviewed file
SRC=$HERE/eve-deploy
[ -f "$SRC" ] && [ ! -L "$SRC" ] || die "$SRC missing"
[ "$(sha_of "$SRC")" = "$EXPECT" ] || die "eve-deploy sha256 does not match --expect-sha256 (got $(sha_of "$SRC"))"
bash -n "$SRC" || die "eve-deploy has a syntax error"

# 2. the public key: one line, ed25519, nothing else
[ -f "$PUB" ] && [ ! -L "$PUB" ] || die "pubkey file missing"
[ "$(wc -l <"$PUB" | tr -d ' ')" -le 1 ] || die "pubkey file must hold exactly one key"
read -r KT KB _ <"$PUB" || true
[ "${KT:-}" = ssh-ed25519 ] && [[ "${KB:-}" =~ ^[A-Za-z0-9+/=]{60,100}$ ]] || die "pubkey must be a single ssh-ed25519 public key"
ssh-keygen -l -f "$PUB" >/dev/null 2>&1 || die "ssh-keygen cannot read the public key"

# 3. every parent of the install dir must be root-owned and not group/world writable, and not a symlink
chain_ok() { local d=$1 own mode
  while [ "$d" != / ] && [ -n "$d" ]; do
    if [ -e "$d" ]; then
      [ ! -L "$d" ] || die "$d is a symlink"
      own=$(stat -c %u "$d" 2>/dev/null || stat -f %u "$d"); mode=$(stat -c %a "$d" 2>/dev/null || stat -f %Lp "$d")
      [ "$own" = 0 ] || die "$d is not root-owned"
      [ $(( 0$mode & 0022 )) = 0 ] || die "$d is group/world writable"
    fi
    d=$(dirname "$d")
  done; }
[ "$SKIP_CHAIN" = 1 ] || { chain_ok "$LIBEXEC"; chain_ok "$STATE"; }
INC_RE=$(printf '%s' "$SUDOERS_D" | sed 's/[.[\*^$]/\\&/g')
grep -Eq "^[#@]includedir[[:space:]]+(/private)?$INC_RE\$" "$SUDOERS_MAIN" || die "$SUDOERS_MAIN does not include $SUDOERS_D"
[ ! -e "$SUDOERS_FILE" ] || echo "note: replacing existing $SUDOERS_FILE"

# 4. files
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$LIBEXEC"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0755 "$SRC" "$ENTRY.new" && mv -f "$ENTRY.new" "$ENTRY"
printf 'eve-release namespaces="eve-deploy" %s %s\n' "$KT" "$KB" >"$LIBEXEC/allowed_signers.new"
install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0644 "$LIBEXEC/allowed_signers.new" "$LIBEXEC/allowed_signers"; rm -f "$LIBEXEC/allowed_signers.new"
install -d -o "$USER_NAME" -g "$U_GID" -m 0700 "$LIBEXEC/staging"
install -d -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0700 "$STATE"
if [ ! -f "$STATE/current_version" ]; then
  ( umask 077; echo "$CUR" >"$STATE/current_version"; echo "$SCHEMA" >"$STATE/schema" )
else echo "note: keeping existing $STATE/current_version ($(head -1 "$STATE/current_version"))"; fi
( umask 077; echo "$USER_NAME" >"$STATE/allowed_user" )
[ -f "$LOGF" ] || install -o "${OWNER%:*}" -g "${OWNER#*:}" -m 0600 /dev/null "$LOGF"

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
visudo -c -q -f "$SUDOERS_MAIN" || { rm -f "$SUDOERS_FILE"; die "visudo -c failed on the full config: removed $SUDOERS_FILE"; }

echo "OK. From now on (no password): sudo $ENTRY deploy <X.Y.Z> | rollback   (user: $USER_NAME)"
echo "Stage bundles in $LIBEXEC/staging as eve-<X.Y.Z>.tar + eve-<X.Y.Z>.tar.sig (made by sign-deploy-bundle.sh on the signing machine)."
echo "Remove: sudo bash uninstall-deploy-nopasswd.sh"
