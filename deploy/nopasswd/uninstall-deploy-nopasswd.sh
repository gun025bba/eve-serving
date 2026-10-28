#!/bin/bash
# Removes the passwordless deploy path (sudoers FIRST, so the privilege is gone before anything else).
#   sudo bash uninstall-deploy-nopasswd.sh            # keeps /var/db/eve-deploy (version state, kept previous binaries) and the log
#   sudo bash uninstall-deploy-nopasswd.sh --purge    # also deletes that state, the staging dir and the log (data deletion: only on purpose)
set -Eeuo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
LIBEXEC=${EVE_LIBEXEC:-/usr/local/libexec/eve}
STATE=${EVE_STATE:-/var/db/eve-deploy}
SUDOERS_D=${EVE_SUDOERS_D:-/etc/sudoers.d}
SUDOERS_MAIN=${EVE_SUDOERS_MAIN:-/etc/sudoers}
LOGF=${EVE_LOGF:-/var/log/eve-deploy.log}
die() { echo "uninstall-deploy-nopasswd: $*" >&2; exit 1; }
[ "${EVE_SKIP_ROOT:-0}" = 1 ] || [ "$(id -u)" = 0 ] || die "run with sudo"
PURGE=0
case "${1:-}" in "") ;; --purge) PURGE=1;; *) die "unknown argument: $1";; esac
[ $# -le 1 ] || die "too many arguments"

rm -f "$SUDOERS_D/eve-deploy"                       # 1. the privilege
if command -v visudo >/dev/null 2>&1; then visudo -c -q -f "$SUDOERS_MAIN" || die "visudo -c fails after removal: check $SUDOERS_D by hand"; fi
rm -f "$LIBEXEC/deploy" "$LIBEXEC/allowed_signers"  # 2. the entrypoint and the pinned key
if [ "$PURGE" = 1 ]; then
  rm -rf "$STATE" "$LIBEXEC/staging" "$LOGF"
  rmdir "$LIBEXEC" 2>/dev/null || true
  echo "purged state, staging and log"
else
  echo "kept $STATE and $LOGF (use --purge to delete)"
fi
echo "OK: passwordless deploy removed. Check: sudo -n -l $LIBEXEC/deploy  (should say a password is required / not allowed)"
