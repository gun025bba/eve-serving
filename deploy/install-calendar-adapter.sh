#!/bin/bash
# shellcheck disable=SC2015  # "A && B || die" is intended: die on any failure
# 캘린더 연동·아침 브리핑(M1) install: 캘린더 어댑터 (the eve-pim helper, #68 / PR #75) + socket plumbing for the gateway
# client (#69 / #76) and the briefing (#71 / #77).
# Run right AFTER deploy-5.0.0.sh (or use deploy-5.0.0-with-calendar.sh):   sudo bash ~/eve-5.0.0/install-calendar-adapter.sh
#   sudo DRY_RUN=1 bash install-calendar-adapter.sh      read-only checks, changes are echoed
# Installs the adapter and (default, PM-approved) the gateway env keys. Flags stay OFF (GW_PIM_ENABLED / GW_BRIEFING_ENABLED = false);
# writes stay OFF: the LaunchAgent never passes --writes-enabled and GW_PIM_WRITES_ENABLED is only ever set to false by this script (never true).
# Re-runnable: every step checks first. Never prints calendar/reminder content, secrets, hostnames, logins or the owner user id.
set -Eeuo pipefail
umask 022

# ===================== pinned inputs (edit only here, then re-run sha256) =====================
PIM_SHA=6ce19939245e021f7dff5830d3a4273a85c9c949   # 5.1.0 adapter commit: last change to pim/eve-pim and the launchd template on release/5.1.0 (#97 review Lows; includes #75-#78). Update it only to a commit security approved. Must be the full 40-hex SHA.
KEEP_REF=keep/pim-adapter-6ce1993  # branch kept on EVE_REPO at PIM_SHA so the pin stays reachable after release/5.1.0 is squashed into main and deleted. Never delete it; move it (fast-forward only) together with PIM_SHA.
EVE_REPO=https://github.com/gun025bba/eve-serving
# Pin of the built eve-pim binary (64 hex). Empty = the hash is only printed (a WARN is logged): after security has reviewed the first build on the
# target Mac, record the printed value here (or pass EXP_PIM_SHA256=<hash> in the sudo environment) so every later run must reproduce it.
EXP_PIM_SHA256="${EXP_PIM_SHA256:-}"
PIM_GID_WANT=452                 # first-choice gid for _evepim; next free gid in 452..499 is used if taken
SOCK_BASE=/opt/eve-pim           # root:wheel 0755; the socket dir below it is <login>:_evepim 0750
RUN_TESTS="${RUN_TESTS:-1}"      # swift test before installing
NO_SANDBOX="${NO_SANDBOX:-0}"    # 1 = LaunchAgent without sandbox-exec (use if EventKit/TCC fails under the profile)
WAIT_TCC_SECS="${WAIT_TCC_SECS:-180}"
# Gateway env block (§6) is the default (PM approved). OWNER_USER_ID may be passed to override the run-time lookup; it is never printed.
# ==============================================================================================

DRY_RUN="${DRY_RUN:-0}"
LABEL=com.eve.pim
GWLABEL=com.llmserving.gateway
DAEMON=/Library/LaunchDaemons/$GWLABEL.plist
SOCK_DIR=$SOCK_BASE/run
SOCK=$SOCK_DIR/pim.sock
# Private work area: random name, mode 0700 (owned by the login user below so the unprivileged build can write). The build tree and the
# logs live inside it, so no predictable /tmp path is created or truncated by root.
WORK=$(mktemp -d /tmp/eve-install-calendar-adapter.XXXXXXXXXX) || { echo "mktemp failed" >&2; exit 1; }
chmod 0700 "$WORK"
BUILD=$WORK/pim-build
TS_STAMP=$(date +%Y%m%d-%H%M%S)
LOG=$WORK/install-$TS_STAMP.log
STAGE=0        # 0 nothing changed | 2 agent loaded (undo = bootout + disable plist)
DONE=0

( umask 077; : > "$LOG" ); chmod 0600 "$LOG"
log()  { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG"; }
die()  { log "FAIL: $*"; exit 1; }
run()  { if [ "$DRY_RUN" = 1 ]; then echo "  DRY: $*"; else "$@"; fi; }

cd /
if [ "$DRY_RUN" = 1 ]; then SUDO_USER="${SUDO_USER:-$(id -un)}"
else
  [ "$(id -u)" = 0 ] || { echo "run with: sudo bash $0" >&2; exit 1; }
  [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] || { echo "SUDO_USER missing" >&2; exit 1; }
fi
chown "$SUDO_USER" "$WORK" "$LOG" 2>/dev/null || true
U_UID=$(id -u "$SUDO_USER")
U_HOME=$(dscl . -read "/Users/$SUDO_USER" NFSHomeDirectory | awk '{print $2}')
PIMHOME="$U_HOME/eve-pim"
UPLIST="$U_HOME/Library/LaunchAgents/$LABEL.plist"
as_user() { if [ "$(id -u)" = "$U_UID" ]; then "$@"; else sudo -u "$SUDO_USER" "$@"; fi; }
run_user() { if [ "$DRY_RUN" = 1 ]; then echo "  DRY(user): $*"; else as_user "$@"; fi; }
dscl_val() { # $1 record $2 key -> value(s) or nothing; "No such key" is exit 0, so read the output
  local o; o=$(dscl . -read "$1" "$2" 2>&1 || true)
  case "$o" in *"No such key"*|*"eDSRecordNotFound"*|*"DS Error"*|"") return 0;; esac
  printf '%s' "$o" | sed -e "1s/^$2:[[:space:]]*//" | tr -s ' \n' ' ' | sed 's/^ *//;s/ *$//'; }
sha_of() { shasum -a 256 "$1" | awk '{print $1}'; }
agent_state() { launchctl print "gui/$U_UID/$LABEL" 2>&1 || true; }
agent_loaded() { local o; o=$(agent_state); case "$o" in *"state = "*) return 0;; esac; return 1; }
health_gw() { [ "$(curl -s -m 3 http://127.0.0.1:8000/health 2>/dev/null || true)" = '{"status":"ok"}' ]; }

undo() {
  set +e; trap - ERR
  log "UNDO start (stage=$STAGE): helper only; gateway and DB untouched"
  [ "$DRY_RUN" = 1 ] && return
  if [ "$STAGE" -ge 2 ]; then
    launchctl bootout "gui/$U_UID/$LABEL" 2>/dev/null
    [ -f "$UPLIST" ] && mv -f "$UPLIST" "$UPLIST.disabled-$TS_STAMP"
  fi
  if health_gw; then log "UNDO gateway health OK"; else log "UNDO gateway health NOT OK (only §6 touches it, and restores the previous plist on failure)"; fi
  log "UNDO done. Kept: _evepim group, $SOCK_BASE, ~/eve-pim (nothing deleted)."
}
on_err() { log "error at line $1 (stage=$STAGE)"; }
on_exit() { local rc=$?
  if [ "$rc" != 0 ] && [ "$DONE" != 1 ] && [ "$STAGE" -ge 2 ]; then undo; fi
  [ "$rc" = 0 ] || log "EXIT rc=$rc, log: $LOG"; }
trap 'on_err $LINENO' ERR
trap on_exit EXIT

log "install-calendar-adapter start (dry_run=$DRY_RUN, pim sha=$PIM_SHA, sandbox=$([ "$NO_SANDBOX" = 1 ] && echo off || echo on))"

# =============================== PRECHECKS + BUILD (user, no root changes) ===============================
is_hex_len() { case "$1" in ""|*[!0-9a-f]*) return 1;; esac; [ "${#1}" -eq "$2" ]; }   # lowercase hex of exactly $2 chars
is_hex_len "$PIM_SHA" 40 || die "PIM_SHA must be the full 40-character lowercase hex commit SHA"
[ -z "$EXP_PIM_SHA256" ] || is_hex_len "$EXP_PIM_SHA256" 64 || die "EXP_PIM_SHA256 must be 64 lowercase hex characters (or empty)"
[ -n "$EXP_PIM_SHA256" ] || log "WARN: EXP_PIM_SHA256 is not pinned: the built eve-pim hash is printed but not checked. Record it after security reviewed this build."
[ -f /Library/LaunchDaemons/$GWLABEL.plist ] || die "gateway LaunchDaemon missing: run deploy-5.0.0.sh first (or deploy-5.0.0-with-calendar.sh)"
health_gw || die "gateway not healthy (deploy 5.0.0 first)"
[ "$(dscl_val /Users/_evegw UniqueID)" = 451 ] || die "_evegw missing / uid != 451"
[ "$(id -u "$SUDO_USER")" -ge 500 ] || die "login user uid unexpected"
case "$(launchctl print "gui/$U_UID" 2>&1 | head -1 || true)" in *"Could not find"*|"") die "no GUI session for $SUDO_USER: log in on the Mac screen first (TCC prompts need it)";; esac
SWIFT=$(as_user xcrun --find swift 2>/dev/null || true)
[ -n "$SWIFT" ] && [ -x "$SWIFT" ] || die "swift not found for $SUDO_USER (xcode-select --install, or install Xcode)"
log "PRE swift: $(as_user swift --version 2>&1 | head -1)"
run_user rm -rf "$BUILD"
run_user git clone -q "$EVE_REPO" "$BUILD"
# The pinned commit must stay reachable: a plain clone has it through $KEEP_REF; fetch it by SHA if a branch no longer reaches it.
if [ "$DRY_RUN" != 1 ] && ! as_user git -C "$BUILD" cat-file -e "$PIM_SHA^{commit}" 2>/dev/null; then
  as_user git -C "$BUILD" fetch -q origin "$PIM_SHA" || die "pinned commit $PIM_SHA is not reachable on $EVE_REPO (restore branch $KEEP_REF at that commit)"
fi
run_user git -C "$BUILD" checkout -q --detach "$PIM_SHA"
if [ "$DRY_RUN" != 1 ]; then
  FULL=$(as_user git -C "$BUILD" rev-parse HEAD); [ "$FULL" = "$PIM_SHA" ] || die "HEAD $FULL != $PIM_SHA"
  P="$BUILD/pim/eve-pim"
  [ -f "$P/Package.swift" ] && [ -f "$P/eve-pim.sb" ] && [ -f "$BUILD/deploy/launchd/com.eve.pim.plist.template" ] || die "pim sources missing at $PIM_SHA"
  if grep -rnE 'import (Network|CFNetwork)|URLSession|NWConnection|NWListener|AF_INET|getaddrinfo' "$P/Sources" >/dev/null; then die "network code found in eve-pim sources"; fi
  # TCC needs usage strings; a bare CLI has no Info.plist, so embed one in the binary (build-time only, no repo change)
  INFO="$BUILD/eve-pim-Info.plist"
  as_user tee "$INFO" >/dev/null <<'PL'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.eve.pim</string>
<key>CFBundleName</key><string>eve-pim</string>
<key>CFBundleVersion</key><string>1</string>
<key>NSCalendarsUsageDescription</key><string>Eve morning briefing reads your calendar (read-only).</string>
<key>NSCalendarsFullAccessUsageDescription</key><string>Eve morning briefing reads your calendar (read-only).</string>
<key>NSRemindersUsageDescription</key><string>Eve morning briefing reads your reminders (read-only).</string>
<key>NSRemindersFullAccessUsageDescription</key><string>Eve morning briefing reads your reminders (read-only).</string>
</dict></plist>
PL
  plutil -lint "$INFO" >/dev/null || die "Info.plist lint"
  if [ "$RUN_TESTS" = 1 ]; then as_user sh -c "cd '$P' && swift test" >"$LOG.swifttest" 2>&1 || { tail -20 "$LOG.swifttest"; die "swift test failed (full output: $LOG.swifttest)"; }; log "PRE swift test OK"; fi
  as_user sh -c "cd '$P' && swift build -c release -Xlinker -sectcreate -Xlinker __TEXT -Xlinker __info_plist -Xlinker '$INFO'" >"$LOG.swiftbuild" 2>&1 \
    || { tail -20 "$LOG.swiftbuild"; die "swift build failed (full output: $LOG.swiftbuild)"; }
  BIN="$P/.build/release/eve-pim"; [ -x "$BIN" ] || die "built binary missing"
  as_user codesign --force --sign - --identifier com.eve.pim "$BIN" >/dev/null 2>&1 || log "PRE WARN: ad-hoc codesign failed (TCC identity may be unstable)"
  PIM_SHA256=$(sha_of "$BIN")
  [ -z "$EXP_PIM_SHA256" ] || [ "$PIM_SHA256" = "$EXP_PIM_SHA256" ] || die "eve-pim sha256 != pinned"
  log "PRE built eve-pim from $FULL  sha256=$PIM_SHA256 (Info.plist embedded, ad-hoc signed)"
else echo "  DRY(user): swift test; swift build -c release (+ embedded Info.plist, ad-hoc codesign)"; BIN=/dev/null; P="$BUILD/pim/eve-pim"; fi

# =============================== §1 _evepim group, _evegw member (root) ===============================
EXISTING_GID=$(dscl_val /Groups/_evepim PrimaryGroupID)
if [ -z "$EXISTING_GID" ]; then
  USED=$(dscl . -list /Groups PrimaryGroupID 2>/dev/null | awk '{print $2}')
  GID_NEW=""; g=$PIM_GID_WANT
  while [ "$g" -le 499 ]; do printf '%s\n' "$USED" | grep -qx "$g" || { GID_NEW=$g; break; }; g=$((g+1)); done
  [ -n "$GID_NEW" ] || die "no free gid in $PIM_GID_WANT..499"
  run dscl . -create /Groups/_evepim
  run dscl . -create /Groups/_evepim PrimaryGroupID "$GID_NEW"
  run dscl . -create /Groups/_evepim RealName "Eve pim socket"
  run dscl . -create /Groups/_evepim Password '*'
fi
[ "$DRY_RUN" = 1 ] || { [ -n "$(dscl_val /Groups/_evepim PrimaryGroupID)" ] || die "_evepim group not created"; }
GM=$(dscl_val /Groups/_evepim GroupMembership)
case " $GM " in *" _evegw "*) ;; *) run dseditgroup -o edit -a _evegw -t user _evepim ;; esac
if [ "$DRY_RUN" != 1 ]; then
  GM=$(dscl_val /Groups/_evepim GroupMembership)
  [ "$GM" = _evegw ] || die "_evepim members are '$GM' (must be exactly _evegw)"
  case " $(sudo -u _evegw id -Gn 2>/dev/null || true) " in *" _evepim "*) ;; *) log "§1 WARN: 'id -Gn _evegw' does not list _evepim yet (directory cache); retry in a minute if the smoke test cannot connect";; esac
fi
log "§1 OK (_evepim gid=$(dscl_val /Groups/_evepim PrimaryGroupID), members: ${GM:-_evegw}; the running gateway picks the group up at its next restart)"

# =============================== §2 socket directory ===============================
run install -d -o root -g wheel -m 0755 "$SOCK_BASE"
run install -d -o "$SUDO_USER" -g _evepim -m 0750 "$SOCK_DIR"
if [ "$DRY_RUN" != 1 ]; then
  [ "$(stat -f '%Su:%Sg:%Lp' "$SOCK_DIR")" = "$SUDO_USER:_evepim:750" ] || die "socket dir must be $SUDO_USER:_evepim 0750"
  [ "$(realpath "$SOCK_DIR")" = "$SOCK_DIR" ] || die "socket dir path has a symlink"
fi
log "§2 OK ($SOCK_DIR = <login>:_evepim 0750; $SOCK_BASE root:wheel 0755)"

# =============================== §3 install files + sandbox profile check ===============================
STOP_FOR_UPDATE=0
if agent_loaded; then STOP_FOR_UPDATE=1; fi
if [ "$STOP_FOR_UPDATE" = 1 ] && [ "$DRY_RUN" != 1 ]; then
  if [ -x "$PIMHOME/eve-pim" ] && [ "$(sha_of "$PIMHOME/eve-pim")" = "$PIM_SHA256" ]; then log "§3 same binary already installed and agent loaded: file install skipped"; STOP_FOR_UPDATE=2
  else launchctl bootout "gui/$U_UID/$LABEL" 2>/dev/null || true; sleep 1; fi
fi
if [ "$STOP_FOR_UPDATE" != 2 ]; then
  run_user mkdir -p "$PIMHOME" "$U_HOME/Library/LaunchAgents" "$U_HOME/Library/Logs"
  if [ "$DRY_RUN" = 1 ]; then echo "  DRY(user): install eve-pim + eve-pim.sb into $PIMHOME"
  else
    as_user install -m 0755 "$BIN" "$PIMHOME/eve-pim"
    as_user install -m 0644 "$P/eve-pim.sb" "$PIMHOME/eve-pim.sb"
    [ "$(sha_of "$PIMHOME/eve-pim")" = "$PIM_SHA256" ] || die "installed binary hash"
    as_user codesign --verify "$PIMHOME/eve-pim" >/dev/null 2>&1 || log "§3 WARN: codesign --verify failed on installed binary"
  fi
  # a stale pim.sock is never deleted by the helper; remove it only if it is a socket and no helper is running
  if [ "$DRY_RUN" != 1 ] && [ -e "$SOCK" ] && ! pgrep -x eve-pim >/dev/null 2>&1; then
    if [ -S "$SOCK" ]; then rm -f "$SOCK"; log "§3 removed stale socket (no helper running)"; else die "$SOCK exists and is not a socket: inspect by hand"; fi
  fi
fi
if [ "$DRY_RUN" != 1 ]; then
  sandbox-exec -f "$PIMHOME/eve-pim.sb" /usr/bin/true || die "sandbox profile does not load"
  if as_user /usr/bin/nc -z -w 2 127.0.0.1 8000 >/dev/null 2>&1; then
    if as_user sandbox-exec -f "$PIMHOME/eve-pim.sb" /usr/bin/nc -z -w 2 127.0.0.1 8000 >/dev/null 2>&1; then die "sandbox profile does NOT block IP connect"; fi
    log "§3 sandbox profile OK (loads; IP connect works outside, is blocked inside)"
  else log "§3 sandbox profile loads (IP-block test skipped: baseline connect failed)"; fi
fi
# render the LaunchAgent from the repo template (no --writes-enabled; verified above)
RENDER=$(mktemp)
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: render $BUILD/deploy/launchd/com.eve.pim.plist.template -> $UPLIST (sandbox=$([ "$NO_SANDBOX" = 1 ] && echo off || echo on))"
else
  python3 - "$BUILD/deploy/launchd/com.eve.pim.plist.template" "$RENDER" "$U_HOME" "$SOCK_DIR" "$NO_SANDBOX" <<'PY'
import plistlib, sys
src, dst, home, sock, nosb = sys.argv[1:6]
import re
t = open(src).read().replace("__HOME__", home).replace("__SOCKET_DIR__", sock)
t = re.sub(r"<!--.*?-->", "", t, flags=re.S)   # the template's comment contains "--", which is invalid XML
pl = plistlib.loads(t.encode())
a = pl["ProgramArguments"]
assert not any("writes" in x for x in a), "writes flag in plist"
if nosb == "1": a[:] = a[3:]       # drop sandbox-exec -f <profile>
pl["ProgramArguments"] = a
with open(dst, "wb") as f: plistlib.dump(pl, f)
PY
  plutil -lint "$RENDER" >/dev/null || die "rendered plist lint"
  PA=$(plutil -extract ProgramArguments json -o - "$RENDER" 2>/dev/null || true)
  case "$PA" in *writes*|"") die "rendered plist ProgramArguments empty or has a writes flag";; esac
  install -o "$SUDO_USER" -g staff -m 0644 "$RENDER" "$UPLIST"; rm -f "$RENDER"
fi
log "§3 OK (binary+profile in ~/eve-pim, LaunchAgent plist rendered, writes flag absent)"

# =============================== §4 load the LaunchAgent + TCC instructions ===============================
if ! agent_loaded; then
  STAGE=2
  run launchctl bootstrap "gui/$U_UID" "$UPLIST"
fi
cat <<'TXT' | tee -a "$LOG"

================ 건영님이 직접 할 일: 캘린더/미리알림 권한 (TCC) ================
 캘린더 어댑터(eve-pim)가 권한을 요청합니다. 지금 Mac 화면(또는 화면 공유)에 팝업이 두 번 뜹니다. 이 스크립트는 팝업을 눌러 줄 수 없습니다.
  1) "eve-pim이 캘린더에 접근하려고 합니다" -> [전체 접근 허용 / Allow Full Access]
  2) "eve-pim이 미리 알림에 접근하려고 합니다" -> [전체 접근 허용 / Allow Full Access]
 팝업이 안 보이면 (시스템 설정 > 개인정보 보호 및 보안):
  - 캘린더 > 목록에서 eve-pim 켜기, 미리 알림 > eve-pim 켜기 ("전체 접근" 표시 확인)
  - 목록에 없으면: 헬퍼 재시작 후 다시 팝업 확인
        launchctl kickstart -k gui/$(id -u)/com.eve.pim
  - 그래도 없으면 권한 초기화 후 재시도 (eve-pim 항목만 초기화됨)
        tccutil reset Calendar com.eve.pim ; tccutil reset Reminders com.eve.pim
    (샌드박스가 원인이면 `sudo NO_SANDBOX=1 bash install-calendar-adapter.sh` 로 샌드박스 없이 다시 설치)
 권한은 eve-pim 바이너리의 서명(cdhash)에 묶입니다. 바이너리를 다시 빌드하면 팝업이 다시 뜹니다.
 읽기 전용입니다: 쓰기(--writes-enabled)는 켜지지 않았고, 게이트웨이 플래그도 모두 꺼져 있습니다.
==================================================================================
TXT

# =============================== §5 wait for the socket (helper binds only after the prompts are answered) ===============================
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: wait up to ${WAIT_TCC_SECS}s for $SOCK; smoke tests"
else
  waited=0; while [ ! -S "$SOCK" ] && [ "$waited" -lt "$WAIT_TCC_SECS" ]; do sleep 3; waited=$((waited+3)); done
  if [ ! -S "$SOCK" ]; then
    log "PENDING: no socket yet after ${WAIT_TCC_SECS}s. The helper is loaded and waiting for the TCC prompts (up to 5 min per prompt)."
    log "         Answer the prompts, then re-run: sudo bash $0   (idempotent; it only runs the smoke test)"
    DONE=1; exit 0
  fi
  # ---- smoke test ----
  [ "$(stat -f '%Su:%Sg:%Lp' "$SOCK_DIR")" = "$SUDO_USER:_evepim:750" ] || die "smoke: socket dir perms changed"
  [ "$(stat -f '%Sg:%Lp' "$SOCK")" = _evepim:660 ] || die "smoke: socket must be group _evepim, mode 660 (is $(stat -f '%Sg:%Lp' "$SOCK"))"
  REQ=$(python3 - <<'PY'
import datetime, json
n = datetime.datetime.now().astimezone().replace(microsecond=0)
print(json.dumps({"v": 1, "id": "smoke", "method": "list_events",
                  "params": {"from": n.isoformat(), "to": (n + datetime.timedelta(days=1)).isoformat()}}))
PY
)
  # (a) as _evegw: must answer (ok:true, or permission_denied while a prompt is still unanswered)
  RESP=$(printf '%s\n' "$REQ" | sudo -u _evegw /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)
  SMOKE=$(printf '%s' "$RESP" | python3 -c 'import json,sys
try:
    d = json.loads(sys.stdin.read())
except Exception:
    print("noresp"); sys.exit()
if d.get("ok"): print("ok count=%d" % len((d.get("result") or {}).get("events", [])))
else: print("err " + str(d.get("error")))' 2>/dev/null || echo noresp)
  case "$SMOKE" in
    ok*) log "SMOKE (a) _evegw list_events: $SMOKE (Calendar permission works)";;
    "err permission_denied") log "SMOKE (a) _evegw reached the helper; Calendar permission not granted yet -> answer/enable the prompt, then re-run";;
    *) die "smoke (a): _evegw got '$SMOKE' (group membership cache? try again in a minute, or check ~/Library/Logs/eve-pim.log)";;
  esac
  # (b) non-_evegw peers (the login user, root) must be disconnected without any response
  [ -z "$(printf '%s\n' "$REQ" | as_user /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)" ] || die "smoke (b): login user got a response"
  [ -z "$(printf '%s\n' "$REQ" | /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)" ] || die "smoke (b): root got a response"
  # (c) writes are off: add_reminder must answer writes_disabled, store untouched
  WR=$(printf '%s\n' '{"v":1,"id":"smoke-w","method":"add_reminder","params":{"title":"x"}}' | sudo -u _evegw /usr/bin/nc -U -w 3 "$SOCK" 2>/dev/null || true)
  printf '%s' "$WR" | grep -q '"writes_disabled"' || die "smoke (c): add_reminder did not answer writes_disabled"
  # (d) a stale pim.sock makes a second helper refuse to start; the file must survive (own temp dir)
  T="$SOCK_BASE/selftest.$$"; install -d -o "$SUDO_USER" -g _evepim -m 0750 "$T"; as_user touch "$T/pim.sock"
  as_user "$PIMHOME/eve-pim" --socket-dir "$T" --peer-user _evegw --group _evepim >/dev/null 2>&1 &
  SP=$!; sleep 4
  if kill -0 "$SP" 2>/dev/null; then kill "$SP" 2>/dev/null || true; log "SMOKE (d) SKIPPED: second helper still waiting (TCC?)"
  else wait "$SP" 2>/dev/null && die "smoke (d): helper started on a stale socket path"; [ -e "$T/pim.sock" ] || die "smoke (d): stale file was deleted"; log "SMOKE (d) stale socket: refused to start, file kept"; fi
  rm -f "$T/pim.sock"; rmdir "$T"
  log "SMOKE OK (a-d). Socket: $SOCK"
fi

# =============================== §6 gateway env block (DEFAULT; flags false, write flags never set) ===============================
# Keys: GW_PIM_SOCKET_PATH, GW_PIM_OWNER_USER_ID, GW_PIM_ENABLED=false, GW_BRIEFING_ENABLED=false (the two flags are only
# added when absent: a value someone set later is kept). The 5.0.0 gateway ignores these keys until a calendar gateway build is deployed.
# GW_PIM_WRITES_ENABLED is only added as false when absent, never changed or set to true. The owner id is read at run time and never printed.
CFG=/opt/eve-gw/etc/config.yaml
OWNER_ID="${OWNER_USER_ID:-}"
if [ -z "$OWNER_ID" ] && [ -r "$CFG" ]; then
  # exactly one allowed login -> its users.id (SQL goes via stdin so the login never shows up in a process list or log)
  OWNER_ID=$(python3 - "$CFG" <<'PY' | as_user psql -d llmchat -At 2>/dev/null | head -1 || true
import json, re, sys
for l in open(sys.argv[1]).read().splitlines():
    m = re.match(r'^allowed_users:\s*(\[.*?\])', l)
    if m:
        try: u = json.loads(m.group(1))
        except Exception: sys.exit()
        if len(u) == 1 and isinstance(u[0], str):
            print("select id from users where login = '" + u[0].replace("'", "''") + "';")
        break
PY
)
fi
case "$OWNER_ID" in ""|*[!0-9]*|0) OWNER_ID="";; esac
if [ -z "$OWNER_ID" ]; then
  log "§6 PENDING: the calendar owner's user id could not be read yet (not signed in once, or more than one allowed user). Sign in on the device, then re-run this script (idempotent), or pass OWNER_USER_ID=<users.id>."
  DONE=1; exit 0
fi
log "§6 owner user id resolved (value not logged)"
RB=/var/root/eve-rollback-pim
if [ "$DRY_RUN" = 1 ]; then echo "  DRY: set GW_PIM_SOCKET_PATH, GW_PIM_OWNER_USER_ID (+ GW_PIM_ENABLED/GW_PIM_WRITES_ENABLED/GW_BRIEFING_ENABLED=false if absent) in $DAEMON, bootout+bootstrap"
else
  CHG=$(EVE_OWNER="$OWNER_ID" python3 - "$DAEMON" "$SOCK" <<'PY'
import os, plistlib, sys
p, sock = sys.argv[1:3]
e = plistlib.load(open(p, "rb")).get("EnvironmentVariables", {})
want = {"GW_PIM_SOCKET_PATH": sock, "GW_PIM_OWNER_USER_ID": os.environ["EVE_OWNER"]}
print("diff" if any(e.get(k) != v for k, v in want.items()) or "GW_PIM_ENABLED" not in e or "GW_PIM_WRITES_ENABLED" not in e or "GW_BRIEFING_ENABLED" not in e else "same")
PY
)
  if [ "$CHG" = same ]; then log "§6 gateway env keys already in place: nothing changed, gateway not restarted"
  else
    install -d -o root -g wheel -m 0700 "$RB"
    install -o root -g wheel -m 0600 "$DAEMON" "$RB/$GWLABEL.plist.$TS_STAMP"
    EVE_OWNER="$OWNER_ID" python3 - "$DAEMON" "$SOCK" <<'PY'
import os, plistlib, sys
p, sock = sys.argv[1:3]
pl = plistlib.load(open(p, "rb"))
e = pl.setdefault("EnvironmentVariables", {})
e["GW_PIM_SOCKET_PATH"] = sock
e["GW_PIM_OWNER_USER_ID"] = os.environ["EVE_OWNER"]
e.setdefault("GW_PIM_ENABLED", "false")
e.setdefault("GW_PIM_WRITES_ENABLED", "false")
e.setdefault("GW_BRIEFING_ENABLED", "false")
tmp = p + ".new"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
with os.fdopen(fd, "wb") as f: plistlib.dump(pl, f)
os.replace(tmp, p)
PY
    plutil -lint "$DAEMON" >/dev/null || { install -o root -g wheel -m 0644 "$RB/$GWLABEL.plist.$TS_STAMP" "$DAEMON"; die "plist lint after edit (restored)"; }
    launchctl bootout "system/$GWLABEL" 2>/dev/null || true
    gw_boot() { local i; for i in $(seq 1 15); do launchctl print "system/$GWLABEL" >/dev/null 2>&1 && return 0; launchctl bootstrap system "$DAEMON" 2>/dev/null && return 0; sleep 1; done; return 1; }
    gw_boot || log "§6 bootstrap kept failing; restoring previous plist"
    for _ in $(seq 1 30); do health_gw && break; sleep 1; done
    if ! health_gw; then
      log "gateway unhealthy after env change: restoring previous plist"
      launchctl bootout "system/$GWLABEL" 2>/dev/null || true
      install -o root -g wheel -m 0644 "$RB/$GWLABEL.plist.$TS_STAMP" "$DAEMON"; gw_boot || true
      die "gateway did not come back with the calendar env keys"
    fi
    log "§6 OK gateway restarted with the calendar env keys (rollback copy: $RB/$GWLABEL.plist.$TS_STAMP)"
  fi
  # report only the flag values (not secrets), and warn if a write flag exists
  for k in GW_PIM_ENABLED GW_BRIEFING_ENABLED GW_PIM_WRITES_ENABLED; do
    v=$(/usr/libexec/PlistBuddy -c "Print :EnvironmentVariables:$k" "$DAEMON" 2>/dev/null || true); log "§6 $k=${v:-unset}"
  done
fi
DONE=1
log "INSTALL-CALENDAR-ADAPTER DONE. Log: $LOG"
