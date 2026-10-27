#!/bin/bash
# QA helper: reconfigure ONLY the test gateway (127.0.0.1:8002, com.llmserving.gateway-test)
# for memory P1 tests. Never touches :8000. Never prints key contents.
#
# Usage: qa-mem-mode.sh <mode>
#   off | on | auditfail | key-ok | key-ok-nl | key-bad-short | key-bad-binary |
#   key-bad-space | key-bad-distinct | key-bad-perm   -> rewrite config, restart test gateway, verify
#   readonly          -> NO restart; gateway must already run in 'on'; chmod user files 0644 in place
#   readonly-restore  -> NO restart; chmod user files back to 0600
#   user-on | user-off -> NO restart; set users.settings memory.enabled=true|false for
#                        QA_USER (login) in the TEST DB only (llmchat_test). Prints only the
#                        resulting memory.enabled value. Refuses unless the server reports
#                        db=llmchat_test, a local host and port QA_PGPORT (default 5432).
#                        psql always uses PGHOST=QA_PGHOST (default /tmp), PGPORT=QA_PGPORT.
#
# Env overrides: QA_DIR (default /private/tmp/eve-qa-mem; must be a real path, no symlinks),
#                QA_MEM_DIR (default $QA_DIR/mem).
# Fixtures live in $QA_DIR/keys (create with: umask 077; openssl rand -hex 32 > ...).
# auditfail: the audit ledger is Postgres (tool_audit), so this revokes INSERT for the test
# role on the test DB; the gateway's startup privilege check then refuses to start.
# Every restart mode re-grants it first.
set -u
MODE=${1:?usage: $0 <mode>}
QA=${QA_DIR:-/private/tmp/eve-qa-mem}; KEYS=$QA/keys; MEM=${QA_MEM_DIR:-$QA/mem}; CFG=$QA/config.yaml
BASE=$HOME/llm-serving; LABEL=com.llmserving.gateway-test
PL=$HOME/Library/LaunchAgents/$LABEL.plist; PB=/usr/libexec/PlistBuddy
LOG=$BASE/gateway-test.log; LLOG=$BASE/gateway-test.launchd.log
DB=llmchat_test; APPROLE=llmchat_test
umask 077
# Explicit connection target: never inherit PGHOST/PGPORT/PGSERVICE etc. from the caller.
unset PGSERVICE PGSERVICEFILE PGDATABASE PGHOSTADDR PGOPTIONS PGUSER PGPASSFILE PGSSLMODE
export PGHOST=${QA_PGHOST:-/tmp} PGPORT=${QA_PGPORT:-5432}
# refuse a non-local target before any connection is made
# libpq accepts a comma-separated host list: every element must be local.
IFS=, read -r -a _qa_hosts <<<"$PGHOST,"
[ ${#_qa_hosts[@]} -gt 0 ] || { echo "refusing: empty QA_PGHOST" >&2; exit 2; }
for _h in "${_qa_hosts[@]}"; do
  case $_h in /*|localhost|127.0.0.1|::1) ;; *) echo "refusing: QA_PGHOST must be local (socket dir, localhost, 127.0.0.1, ::1)" >&2; exit 2;; esac
done

# Leak check: looks for each key's raw bytes, hex and base64 (prefixes) in the given text file.
# Prints only YES/no, never key material.
leak_check() { python3 - "$KEYS" "$@" <<'PY'
import sys, os, base64, binascii
kd, logs = sys.argv[1], sys.argv[2:]
data = b"".join(open(p, "rb").read() for p in logs if os.path.exists(p))
low = data.lower()
hit = False
for f in os.listdir(kd):
    raw = open(os.path.join(kd, f), "rb").read()
    body = raw.rstrip(b"\n")
    cands = []
    for b in {raw, body}:
        if len(b) >= 12:
            cands.append(b[:12]); cands.append(b[-12:])
        hx = binascii.hexlify(b)
        if len(hx) >= 16: cands.append(hx[:16]); cands.append(hx[-16:])
        b64 = base64.b64encode(b)
        if len(b64) >= 16: cands.append(b64[:16])
        u64 = base64.urlsafe_b64encode(b)
        if len(u64) >= 16: cands.append(u64[:16])
    # if the file itself is hex text, also look for its decoded bytes
    try:
        dec = binascii.unhexlify(b"".join(body.split()))
        if len(dec) >= 12: cands.append(dec[:12])
        if len(dec) >= 12: cands.append(base64.b64encode(dec)[:16])
    except Exception:
        pass
    for c in cands:
        if c in data or c.lower() in low:
            hit = True
print("YES" if hit else "no")
PY
}

# Sets users.settings memory.enabled for one login in the test DB (values via psql vars).
# Existing users only (no row is created); one transaction; needs migration 00007.
user_mem_set() { psql -1 -qtAX -d "$DB" -v ON_ERROR_STOP=1 -v login="$1" -v val="$2" <<'SQL'
UPDATE users SET settings = settings || jsonb_build_object('memory',
    (CASE WHEN jsonb_typeof(settings->'memory') = 'object' THEN settings->'memory' ELSE '{}'::jsonb END)
    || jsonb_build_object('enabled', (:'val')::boolean))
  WHERE login = :'login' RETURNING settings->'memory'->>'enabled';
SQL
}

case $MODE in
  readonly|readonly-restore)
    if [ "$MODE" = readonly ]; then
      en=$($PB -c 'Print :EnvironmentVariables:GW_MEMORY_ENABLED' "$PL" 2>/dev/null)
      v=$(curl -sf -m 2 http://127.0.0.1:8002/health) || { echo "readonly: test gateway not running; run '$0 on' first" >&2; exit 3; }
      [ "$en" = true ] || { echo "readonly: gateway is not in 'on' (GW_MEMORY_ENABLED=$en); run '$0 on' first" >&2; exit 3; }
    fi
    files=$(ls "$MEM"/*.json 2>/dev/null)
    [ -n "$files" ] || { echo "$MODE: no user memory file in $MEM yet (create a memory in 'on' first)" >&2; exit 3; }
    perm=600; [ "$MODE" = readonly ] && perm=644
    chmod $perm $files
    for f in $files; do echo "$(basename "$f") -> $(stat -f %Lp "$f")"; done
    if [ "$MODE" = readonly ]; then
      echo "mode=readonly (no restart) health=$v"
      echo "expect: next memory write -> 503 memory_read_only; reads still OK. Undo: $0 readonly-restore"
    else
      echo "mode=readonly-restore (no restart): writes should succeed again"
    fi
    exit 0;;
  user-on|user-off)
    : "${QA_USER:?set QA_USER to the test user login}"
    val=false; [ "$MODE" = user-on ] && val=true
    # target check: database name AND server host/port as reported by the server itself
    want_port=$PGPORT
    tgt=$(psql -qtAX -d "$DB" -c "SELECT current_database()||' '||coalesce(host(inet_server_addr()),'local')||' '||current_setting('port')") ||
      { echo "$MODE: cannot connect" >&2; exit 3; }
    set -- $tgt
    [ "$1" = llmchat_test ] || { echo "refusing: connected db is not llmchat_test" >&2; exit 2; }
    case $2 in local|127.0.0.1|::1) ;; *) echo "refusing: DB server is not local" >&2; exit 2;; esac
    [ "$3" = "$want_port" ] || { echo "refusing: DB port $3 != expected $want_port (set QA_PGPORT)" >&2; exit 2; }
    psql -qtAX -d "$DB" -c "SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='users' AND column_name='settings'" | grep -q 1 ||
      { echo "$MODE: users.settings missing (test DB not migrated to 00007); nothing changed" >&2; exit 3; }
    got=$(user_mem_set "$QA_USER" "$val") || { echo "$MODE: update failed" >&2; exit 3; }
    [ -n "$got" ] || { echo "$MODE: no such user in $DB (log in once via the test gateway first)" >&2; exit 3; }
    echo "mode=$MODE db=$DB memory.enabled=$got"
    [ "$got" = "$val" ]; exit;;
  off) EN=false; KEY=key-ok; EXPECT=start;;
  on|auditfail|key-ok) EN=true; KEY=key-ok; EXPECT=start;;
  key-ok-nl) EN=true; KEY=key-ok-nl; EXPECT=start;;
  key-bad-*) EN=true; KEY=$MODE; EXPECT=refuse;;
  *) echo "unknown mode: $MODE" >&2; exit 2;;
esac
[ "$MODE" = auditfail ] && EXPECT=refuse
[ -f "$KEYS/$KEY" ] || { echo "missing fixture $KEYS/$KEY" >&2; exit 2; }

psql -qd $DB -c "GRANT INSERT ON tool_audit TO $APPROLE" >/dev/null
[ "$MODE" = auditfail ] && psql -qd $DB -c "REVOKE INSERT ON tool_audit FROM $APPROLE" >/dev/null

# throwaway config: base config + memory block
{ grep -v '^memory:' "$BASE/config.yaml"; printf '\nmemory:\n  enabled: %s\n  data_dir: %s\n  audit_key_file: %s\n' "$EN" "$MEM" "$KEYS/$KEY"; } > "$CFG"
$PB -c "Delete :EnvironmentVariables:GW_CONFIG" "$PL" 2>/dev/null
$PB -c "Add :EnvironmentVariables:GW_CONFIG string $CFG" "$PL"
$PB -c "Delete :EnvironmentVariables:GW_MEMORY_ENABLED" "$PL" 2>/dev/null
$PB -c "Add :EnvironmentVariables:GW_MEMORY_ENABLED string $EN" "$PL"
plutil -lint -s "$PL" || exit 3

MARK=$(wc -c <"$LLOG" 2>/dev/null || echo 0); MARK2=$(wc -c <"$LOG" 2>/dev/null || echo 0)
launchctl bootout gui/$(id -u)/$LABEL 2>/dev/null; sleep 1
launchctl bootstrap gui/$(id -u) "$PL"
ok=0; v=; for i in $(seq 1 10); do sleep 1; v=$(curl -sf -m 2 http://127.0.0.1:8002/health) && { ok=1; break; }; done
NEW=$(mktemp); trap 'rm -f "$NEW"' EXIT
{ tail -c +$((MARK+1)) "$LLOG"; tail -c +$((MARK2+1)) "$LOG"; } > "$NEW" 2>/dev/null
GOT=refuse; [ $ok = 1 ] && GOT=start
leak=$(leak_check "$LOG" "$LLOG")
reason=$(grep -aoE 'memory_disabled[^}]*|memory_enabled[^,]*|refusing to start[^"]*' "$NEW" | tail -1 | cut -c1-160)
echo "mode=$MODE expect=$EXPECT got=$GOT key_leak=$leak health=${v:-none}"
[ -n "$reason" ] && echo "log: $reason"
[ "$GOT" = "$EXPECT" ] && [ "$leak" = no ]
