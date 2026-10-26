#!/bin/bash
# Daily logical backup of the conversation DB (custom format, restorable with
# pg_restore). Connects as the local OS superuser over the unix socket (peer
# auth), so no password is needed or exposed. Keeps RETENTION_DAYS (14).
set -euo pipefail
PG_FORMULA="${PG_FORMULA:-postgresql@18}"
PREFIX="${HOMEBREW_PREFIX:-/opt/homebrew}"
export PATH="$PREFIX/opt/$PG_FORMULA/bin:$PREFIX/bin:/usr/bin:/bin"
BASE="${BASE:-$HOME/llm-serving}"
DIR="${BACKUP_DIR:-$BASE/backups}"
DB="${DB:-llmchat}"
RETENTION_DAYS="${RETENTION_DAYS:-14}"
umask 077
mkdir -p "$DIR"
out="$DIR/$DB-$(date +%Y%m%d-%H%M%S).dump"
pg_dump -Fc -d "$DB" -p "${PGPORT:-5432}" -f "$out.tmp"
mv "$out.tmp" "$out"
find "$DIR" -name "$DB-*.dump" -type f -mtime +"$RETENTION_DAYS" -delete
echo "$(date '+%F %T') backup ok: $out ($(du -h "$out" | cut -f1))"
