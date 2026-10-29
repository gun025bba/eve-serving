#!/usr/bin/env bash
# music-probe.sh — Music.app 읽기 전용 실측 프로브 (이슈 #82)
#
# 보안 리뷰 완료 후 실행. 실행: bash scripts/poc/music-probe.sh   (macOS, 건영 로그인 세션의 터미널)
#
# 보장:
#  - 읽기 전용: 쓰기·삭제·duplicate·재생·UI 스크립팅·파일 접근·셸 실행 AppleScript 없음.
#    (실행 전에 모든 AppleScript 텍스트를 금지어 검사로 자체 점검하고, 걸리면 중단한다.)
#  - 네트워크 미사용(curl/nc 등 호출 없음).
#  - stdout에는 통계·소요 시간만 JSON으로 낸다. 곡 제목·아티스트·앨범·파일 경로는 읽지도 출력하지도 않는다.
#    플레이리스트 이름은 기본 제외, MUSIC_PROBE_NAMES=1 일 때만 출력(건영이 직접 켤 때).
#  - 부작용: Music.app이 꺼져 있으면 Apple Events 때문에 앱이 실행된다(재생은 하지 않음).
#    TCC 자동화 권한 팝업이 뜨면 건영이 직접 허용해야 한다.
#
# 환경 변수: PROBE_TIMEOUT_TCC(기본 90초), PROBE_TIMEOUT(기본 300초), MUSIC_PROBE_NAMES(기본 0)
# 쓰기 검증(duplicate/delete)은 이 스크립트가 하지 않는다 → scripts/poc/music-probe.md 의 수동 절차 참고.

set -u
PROBE_TIMEOUT_TCC="${PROBE_TIMEOUT_TCC:-90}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-300}"
MUSIC_PROBE_NAMES="${MUSIC_PROBE_NAMES:-0}"

ERRORS=()
add_error() { ERRORS+=("$1"); }

json_str() { # JSON 문자열 이스케이프(백슬래시, 따옴표, 제어문자 제거)
  local s=$1
  s=${s//\\/\\\\}; s=${s//\"/\\\"}
  s=$(printf '%s' "$s" | tr -d '\000-\037')
  printf '"%s"' "$s"
}
is_int() { [[ ${1:-} =~ ^-?[0-9]+$ ]]; }
int_or_null() { if is_int "${1:-}"; then printf '%s' "$1"; else printf 'null'; fi; }
now_ms() {
  if command -v perl >/dev/null 2>&1; then
    perl -MTime::HiRes=time -e 'printf "%d", time()*1000'
  else
    echo $(( $(date +%s) * 1000 ))
  fi
}
kv() { # kv "a=1;b=2" b → 2
  local IFS=';' pair
  for pair in $1; do
    if [[ ${pair%%=*} == "$2" ]]; then printf '%s' "${pair#*=}"; return 0; fi
  done
  return 1
}

# ---- 읽기 전용 자체 점검 ----
assert_readonly() {
  local text=$1
  if grep -Eq '(^|[^a-z])(duplicate|delete|make new|move |play|pause|stop|open location|do shell script|add |reveal|launch|keystroke|click)([^a-z]|$)' <<<"$text"; then
    echo '{"error":"readonly_guard_violation","detail":"forbidden verb in AppleScript"}'; exit 3
  fi
  if grep -Eq 'set (played count|played date|skipped count|skipped date|rating|loved|disliked|name|genre|artist|album|comment|year|database ID|persistent ID|date added|enabled|shuffle|sound volume|player position)( of|$)' <<<"$text"; then
    echo '{"error":"readonly_guard_violation","detail":"property write in AppleScript"}'; exit 3
  fi
  if grep -Eq 'System Events|Finder|http' <<<"$text"; then
    echo '{"error":"readonly_guard_violation","detail":"forbidden target in AppleScript"}'; exit 3
  fi
}

TMP=$(mktemp -d "${TMPDIR:-/tmp}/music-probe.XXXXXX") || exit 1
trap 'rm -rf "$TMP"' EXIT

OSA_OUT=""; OSA_ERR=""; OSA_RC=0; OSA_MS=0
run_osa() { # run_osa <timeout초> <AppleScript 텍스트>
  local tmo=$1 text=$2 t0 pid waited=0 limit
  assert_readonly "$text"
  t0=$(now_ms)
  printf '%s\n' "$text" | osascript - >"$TMP/out" 2>"$TMP/err" &
  pid=$!
  limit=$(( tmo * 5 ))
  while kill -0 "$pid" 2>/dev/null; do
    if (( waited >= limit )); then
      kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
      OSA_RC=124; OSA_OUT=""; OSA_ERR="timeout"; OSA_MS=$(( $(now_ms) - t0 )); return 124
    fi
    sleep 0.2; waited=$((waited + 1))
  done
  wait "$pid"; OSA_RC=$?
  OSA_MS=$(( $(now_ms) - t0 ))
  OSA_OUT=$(cat "$TMP/out"); OSA_ERR=$(cat "$TMP/err")
  return "$OSA_RC"
}

# ---- 환경 확인 ----
if [[ "$(uname -s)" != "Darwin" && "${PROBE_ALLOW_NONMAC:-0}" != "1" ]]; then
  echo '{"error":"unsupported_os","detail":"macOS 전용"}'; exit 2
fi
OS_VER=$(sw_vers -productVersion 2>/dev/null || echo unknown)
OS_BUILD=$(sw_vers -buildVersion 2>/dev/null || echo unknown)
if pgrep -x Music >/dev/null 2>&1; then WAS_RUNNING=true; else WAS_RUNNING=false; fi
GENERATED=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# ---- 1) TCC 자동화 권한 + Music 버전 (첫 호출) ----
TCC_STATUS=error; MUSIC_VER=unknown; FIRST_MS=0; READY=0
run_osa "$PROBE_TIMEOUT_TCC" 'tell application "Music"
  return (version as text)
end tell'
FIRST_MS=$OSA_MS
if (( OSA_RC == 0 )); then
  TCC_STATUS=granted; MUSIC_VER=$OSA_OUT; READY=1
elif (( OSA_RC == 124 )); then
  TCC_STATUS=timeout; add_error "tcc_prompt_timeout"
elif grep -q -- '-1743' <<<"$OSA_ERR"; then
  TCC_STATUS=denied; add_error "automation_denied(-1743)"
else
  TCC_STATUS=error; add_error "first_call_failed"
fi
PROMPT_SUSPECTED=false
if (( FIRST_MS >= 2000 )) && [[ $WAS_RUNNING == true ]]; then PROMPT_SUSPECTED=true; fi
if (( READY == 0 )) && [[ $TCC_STATUS != granted ]] && [[ $TCC_STATUS != error ]]; then PROMPT_SUSPECTED=true; fi

N=null; COUNT_MS=null; FETCH_MS=null
PP_PC=null; PP_PD=null; PP_SC=null; PP_DA=null; PP_RT=null
PL_TOTAL=null; PL_SMART=null; PL_EMPTY=null; PL_MS=null; PL_ITEMS="[]"
SAMPLES="[]"

if (( READY == 1 )); then
  # ---- 2) 라이브러리 트랙 수 ----
  if run_osa "$PROBE_TIMEOUT" 'tell application "Music"
  return (count of tracks of library playlist 1) as text
end tell' && is_int "$OSA_OUT"; then
    N=$OSA_OUT; COUNT_MS=$OSA_MS
  else
    add_error "track_count_failed"
  fi
fi

if (( READY == 1 )) && is_int "$N" && (( N > 0 )); then
  # ---- 3) 전체 트랙 메타 일괄 읽기(값은 버리고 개수만 반환) ----
  if run_osa "$PROBE_TIMEOUT" 'tell application "Music"
  set t to library playlist 1
  set a to played count of every track of t
  set b to played date of every track of t
  set c to skipped count of every track of t
  set d to date added of every track of t
  set e to rating of every track of t
  return ((count of a) as text) & "," & ((count of b) as text) & "," & ((count of c) as text) & "," & ((count of d) as text) & "," & ((count of e) as text)
end tell'; then
    FETCH_MS=$OSA_MS
  else
    add_error "bulk_fetch_failed"
  fi

  # ---- 4) 속성별 집계(소요 시간은 읽기+집계 포함) ----
  AS_HANDLERS='on pad(n)
  if n < 10 then return "0" & (n as text)
  return n as text
end pad
on iso(d)
  return ((year of d) as text) & "-" & my pad((month of d) as integer) & "-" & my pad(day of d)
end iso'

  if run_osa "$PROBE_TIMEOUT" "$AS_HANDLERS
tell application \"Music\"
  set xs to played count of every track of library playlist 1
end tell
set z to 0
set mx to 0
repeat with x in xs
  set v to contents of x
  if v is 0 then set z to z + 1
  if v > mx then set mx to v
end repeat
return \"zero=\" & z & \";max=\" & mx"; then
    PP_PC="{\"ms\":$OSA_MS,\"zero\":$(int_or_null "$(kv "$OSA_OUT" zero)"),\"max\":$(int_or_null "$(kv "$OSA_OUT" max)")}"
  else add_error "played_count_failed"; fi

  if run_osa "$PROBE_TIMEOUT" "$AS_HANDLERS
tell application \"Music\"
  set xs to played date of every track of library playlist 1
end tell
set miss to 0
set oldest to missing value
set newest to missing value
repeat with x in xs
  set v to contents of x
  if v is missing value then
    set miss to miss + 1
  else
    if oldest is missing value then
      set oldest to v
      set newest to v
    else
      if v < oldest then set oldest to v
      if v > newest then set newest to v
    end if
  end if
end repeat
set o to \"none\"
set nw to \"none\"
if oldest is not missing value then
  set o to my iso(oldest)
  set nw to my iso(newest)
end if
return \"missing=\" & miss & \";oldest=\" & o & \";newest=\" & nw"; then
    PD_O=$(kv "$OSA_OUT" oldest); PD_N=$(kv "$OSA_OUT" newest)
    PP_PD="{\"ms\":$OSA_MS,\"missing\":$(int_or_null "$(kv "$OSA_OUT" missing)"),\"oldest\":$(json_str "$PD_O"),\"newest\":$(json_str "$PD_N")}"
  else add_error "played_date_failed"; fi

  if run_osa "$PROBE_TIMEOUT" 'tell application "Music"
  set xs to skipped count of every track of library playlist 1
end tell
set z to 0
set mx to 0
repeat with x in xs
  set v to contents of x
  if v is 0 then set z to z + 1
  if v > mx then set mx to v
end repeat
return "zero=" & z & ";max=" & mx'; then
    PP_SC="{\"ms\":$OSA_MS,\"zero\":$(int_or_null "$(kv "$OSA_OUT" zero)"),\"max\":$(int_or_null "$(kv "$OSA_OUT" max)")}"
  else add_error "skipped_count_failed"; fi

  if run_osa "$PROBE_TIMEOUT" "$AS_HANDLERS
tell application \"Music\"
  set xs to date added of every track of library playlist 1
end tell
set miss to 0
set oldest to missing value
set newest to missing value
repeat with x in xs
  set v to contents of x
  if v is missing value then
    set miss to miss + 1
  else
    if oldest is missing value then
      set oldest to v
      set newest to v
    else
      if v < oldest then set oldest to v
      if v > newest then set newest to v
    end if
  end if
end repeat
set o to \"none\"
set nw to \"none\"
if oldest is not missing value then
  set o to my iso(oldest)
  set nw to my iso(newest)
end if
return \"missing=\" & miss & \";oldest=\" & o & \";newest=\" & nw"; then
    DA_O=$(kv "$OSA_OUT" oldest); DA_N=$(kv "$OSA_OUT" newest)
    PP_DA="{\"ms\":$OSA_MS,\"missing\":$(int_or_null "$(kv "$OSA_OUT" missing)"),\"oldest\":$(json_str "$DA_O"),\"newest\":$(json_str "$DA_N")}"
  else add_error "date_added_failed"; fi

  if run_osa "$PROBE_TIMEOUT" 'tell application "Music"
  set xs to rating of every track of library playlist 1
end tell
set rated to 0
repeat with x in xs
  set v to contents of x
  if v > 0 then set rated to rated + 1
end repeat
return "rated=" & rated & ";unrated=" & ((count of xs) - rated)'; then
    PP_RT="{\"ms\":$OSA_MS,\"rated\":$(int_or_null "$(kv "$OSA_OUT" rated)"),\"unrated\":$(int_or_null "$(kv "$OSA_OUT" unrated)")}"
  else add_error "rating_failed"; fi

  # ---- 5) 샘플 5개(균등 간격 인덱스; 제목 등 식별 문자열은 읽지 않음) ----
  SAMPLE_AS="$AS_HANDLERS
set n to $N
set cnt to 5
if n < 5 then set cnt to n
set dv to cnt - 1
if dv < 1 then set dv to 1
set out to \"\"
repeat with k from 0 to (cnt - 1)
  set i to 1 + ((k * (n - 1)) div dv)
  tell application \"Music\"
    set tr to track i of library playlist 1
    set pc to played count of tr
    set pd to played date of tr
    set sc to skipped count of tr
    set da to date added of tr
    set rt to rating of tr
  end tell
  set pds to \"null\"
  if pd is not missing value then set pds to my iso(pd)
  set das to \"null\"
  if da is not missing value then set das to my iso(da)
  set out to out & i & \"|\" & pc & \"|\" & pds & \"|\" & sc & \"|\" & das & \"|\" & rt & linefeed
end repeat
return out"
  if run_osa "$PROBE_TIMEOUT" "$SAMPLE_AS"; then
    SAMPLES="["; first=1
    while IFS='|' read -r si spc spd ssc sda srt; do
      [[ -z ${si:-} ]] && continue
      is_int "$si" || continue
      [[ $spd == null ]] && pdj=null || pdj=$(json_str "$spd")
      [[ $sda == null ]] && daj=null || daj=$(json_str "$sda")
      (( first )) || SAMPLES+=","; first=0
      SAMPLES+="{\"index\":$si,\"played_count\":$(int_or_null "$spc"),\"played_date\":$pdj,\"skipped_count\":$(int_or_null "$ssc"),\"date_added\":$daj,\"rating\":$(int_or_null "$srt")}"
    done <<<"$OSA_OUT"
    SAMPLES+="]"
  else add_error "samples_failed"; fi
fi

# ---- 6) 플레이리스트 목록(이름은 기본 제외) ----
if (( READY == 1 )); then
  if [[ $MUSIC_PROBE_NAMES == 1 ]]; then WANT_NAMES=true; else WANT_NAMES=false; fi
  if run_osa "$PROBE_TIMEOUT" "set wantNames to $WANT_NAMES
set out to \"\"
tell application \"Music\"
  repeat with p in (every user playlist)
    set sm to false
    try
      set sm to smart of p
    end try
    set c to -1
    try
      set c to count of tracks of p
    end try
    set nm to \"\"
    if wantNames then
      try
        set nm to name of p
      end try
    end if
    set out to out & sm & \"|\" & c & \"|\" & nm & linefeed
  end repeat
end tell
return out"; then
    PL_MS=$OSA_MS; PL_TOTAL=0; PL_SMART=0; PL_EMPTY=0; PL_ITEMS="["; first=1
    while IFS='|' read -r psm pc pnm; do
      [[ -z ${psm:-} ]] && continue
      PL_TOTAL=$((PL_TOTAL + 1))
      [[ $psm == true ]] && { PL_SMART=$((PL_SMART + 1)); smj=true; } || smj=false
      is_int "$pc" && (( pc == 0 )) && PL_EMPTY=$((PL_EMPTY + 1))
      (( first )) || PL_ITEMS+=","; first=0
      if [[ $WANT_NAMES == true ]]; then
        PL_ITEMS+="{\"smart\":$smj,\"track_count\":$(int_or_null "$pc"),\"name\":$(json_str "${pnm:-}")}"
      else
        PL_ITEMS+="{\"smart\":$smj,\"track_count\":$(int_or_null "$pc")}"
      fi
    done <<<"$OSA_OUT"
    PL_ITEMS+="]"
  else add_error "playlists_failed"; fi
fi

# ---- JSON 출력 ----
ERR_JSON="["; first=1
for e in "${ERRORS[@]+"${ERRORS[@]}"}"; do
  (( first )) || ERR_JSON+=","; first=0; ERR_JSON+=$(json_str "$e")
done
ERR_JSON+="]"

cat <<JSON
{
  "probe_version": 1,
  "read_only": true,
  "network_used": false,
  "generated_at": "$GENERATED",
  "macos": {"version": $(json_str "$OS_VER"), "build": $(json_str "$OS_BUILD")},
  "music_app": {"version": $(json_str "$MUSIC_VER"), "was_running": $WAS_RUNNING},
  "tcc_automation": {"status": "$TCC_STATUS", "first_call_ms": $FIRST_MS, "prompt_suspected": $PROMPT_SUSPECTED},
  "library": {"track_count": $N, "count_ms": $COUNT_MS},
  "bulk_read": {"properties": ["played_count","played_date","skipped_count","date_added","rating"], "fetch_only_ms": $FETCH_MS},
  "per_property": {"played_count": $PP_PC, "played_date": $PP_PD, "skipped_count": $PP_SC, "date_added": $PP_DA, "rating": $PP_RT},
  "playlists": {"total": $PL_TOTAL, "smart": $PL_SMART, "empty": $PL_EMPTY, "ms": $PL_MS, "names_included": $([[ $MUSIC_PROBE_NAMES == 1 ]] && echo true || echo false), "items": $PL_ITEMS},
  "samples": $SAMPLES,
  "errors": $ERR_JSON
}
JSON
if [[ $TCC_STATUS != granted ]]; then exit 2; fi
exit 0
