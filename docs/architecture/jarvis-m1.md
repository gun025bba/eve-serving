# Jarvis M1 — 캘린더·리마인더 연동 + 아침 브리핑 — **제안 · 미구현**

비전: Eve는 사용자를 알고, 직접 행동하고, 먼저 말하고, 어디서든 닿는 개인 비서다. M1은 그중 **"내 일정을 알고 아침에 먼저 말해 준다"**만 한다.
원칙: 새 상자를 최소로 만든다. 기존 툴 플랫폼(`docs/tool-platform.md`, #21), 선톡(`proactive-messaging.md`), 메모리 5.0.0(#54), 신원 전달(#23 `identity-transport.md`)을 재사용한다.

## 0. 요약
- 새 컴포넌트는 **1개**: host의 EventKit 헬퍼(`eve-pim`). 나머지는 기존 툴 레지스트리, `pending_actions`, `scheduled_jobs`, in-app 선톡 전달을 그대로 쓴다.
- 새 테이블은 **0개**. 브리핑은 `scheduled_jobs` 템플릿 job 하나, 설정은 `users.settings`에 둔다.
- 기본값은 **off**(§5).

## 1. 툴 플랫폼 배치 (#21)
| 툴 | scope | verb(payload_view) | 확인 카드 |
|---|---|---|---|
| `calendar.list_events(from,to)` | `calendar.read` | – | 없음 |
| `reminders.list(due_before, include_done=false)` | `reminders.read` | – | 없음 |
| `reminders.add(title, due?, list?)` | `reminders.write` | `add` | **필수** |
| `reminders.complete(id)` | `reminders.write` | `update` | **필수** |
| `calendar.add_event(...)` | `calendar.write` | `add` | **필수** — M1에서는 **노출하지 않음**(청사진) |

- 쓰기는 모두 기존 `pending_actions` 확인 게이트를 탄다. 모델은 `{"status":"pending_confirmation","action_id":...}`만 받고, 승인은 브라우저 세션(페어링 쿠키)에서만 가능하다. Bearer는 403 `scope_denied`, 자동 승인은 없다. 메모리 approve 흐름과 같은 규칙이다.
- 결과 형식(기존 `tool_result` 그대로):
  `{"status":"ok","data":{"events":[{"id":"<opaque>","title":"...","start":"2026-10-28T09:00:00+09:00","end":"...","all_day":false,"calendar":"<name>"}]}}`
  리마인더: `{"id","title","due","list","done"}`. 메모·참석자·위치·URL은 M1에서 **반환하지 않는다**(최소 데이터, 프롬프트 인젝션 면적 축소). title은 untrusted로 래핑한다(web_search와 같은 방식).
- 에러는 `error_code`만 쓴다: `pim_unavailable`, `permission_denied`(TCC 미허용), `kill_switch`, `timeout`.

## 2. 데이터 소스
| 안 | 방식 | 데이터 경로 | 평가 |
|---|---|---|---|
| **A. EventKit 헬퍼 (추천)** | host 사용자 세션의 작은 Swift 데몬 `eve-pim`(LaunchAgent)이 EventKit을 읽고 쓴다. gateway는 로컬 unix 소켓으로 호출한다 | host 안에서 끝난다(gateway↔헬퍼). 캘린더 계정 동기화는 macOS가 이미 하고 있는 것 | 단순하다. macOS 캘린더/미리알림에 연결된 모든 계정(iCloud·Google·CalDAV)을 한 번에 커버한다 |
| B. CalDAV 직접 | gateway가 CalDAV 서버에 접속 | **iCloud/Google CalDAV면 외부 API** [건영 승인 필요]. tailnet 안 자체 서버(예: Radicale)면 내부 | 앱 비밀번호 보관이 필요하고 리마인더(VTODO) 지원이 제각각 |
| C. Google Calendar API | OAuth + REST | **외부 API, 토큰 외부 발급** [건영 승인 필요] | 리마인더는 별도 API(Tasks). 상자가 가장 많다 |

**추천: A.** 주의:
- EventKit은 TCC 권한이 필요하다 → 헬퍼는 서비스 계정 `_evegw`가 아니라 **건영 로그인 세션의 LaunchAgent**로 돌고, 처음 한 번 권한 팝업을 승인한다(sudo 불필요, 건영 직접).
- 소켓: `<user-run-dir>/pim.sock` 0660, 그룹은 `_evegw`만 들어 있는 전용 그룹 `_evepim`(사람·에이전트는 넣지 않음). Tailscale serve와 무관한 host 내부 IPC라 #23의 샌드박스 문제는 없다. 소켓 디렉터리 소유·권한 검사(fail-closed)는 #23 런북 §0 규칙을 재사용한다.
- 헬퍼 API는 툴 표와 1:1인 최소 JSON-RPC 4개(`list_events`, `list_reminders`, `add_reminder`, `complete_reminder`)만 둔다. 임의 쿼리·삭제·캘린더 목록 변경은 없다.
- [건영 승인 필요] 쓰기(`reminders.add/complete`)는 로컬 저장소에 쓰지만 **macOS가 연결된 계정(iCloud 등)으로 동기화해 외부로 나간다**. 쓰기 verb 활성화는 건영 승인 후에 한다(읽기만 먼저 배포 가능).
- 다중 사용자: EventKit은 host 로그인 사용자 한 명의 데이터다 → M1은 **`pim.owner_user_id` 한 명**(users.id)에게만 툴을 노출한다. 다른 사용자는 툴 자체가 숨겨진다. 청사진: 사용자별 CalDAV(안 B)나 사용자별 헬퍼.

## 3. 아침 브리핑 스케줄러
- **실행 위치**: gateway 안의 기존 선톡 스케줄러. job = `scheduled_jobs(when_kind='rrule', when_value='FREQ=DAILY;BYHOUR=<h>;BYMINUTE=<m>', tz, template_id='morning_briefing')`. 사용자가 설정에서 켜면 서버가 job을 만들거나 갱신한다(모델은 job을 만들 수 없음, 기존 H3 규칙).
- **구성**(템플릿이 정한 고정 수집, 모델 자유 툴 호출 없음):
  1. `calendar.list_events(오늘 00:00~24:00)`
  2. `reminders.list(due_before=오늘 끝, include_done=false)` + 지난 미완료
  3. 승인된 메모리 항목(§4 조건 충족 시)
  4. 모델이 위 데이터만으로 짧은 요약을 만든다. 툴은 노출하지 않는다(읽기 결과만 컨텍스트로, 쓰기 제안 없음).
- **비어 있으면** 짧은 한 줄을 보내거나 생략한다(`briefing.skip_if_empty`, 기본 true → `skipped_empty`).
- **전달**: in-app 선톡만(`messages.origin='proactive'`, job 스레드 "아침 브리핑"). 이것은 tailnet 안에서 끝난다.
  - iOS 셸 푸시는 무료 계정이라 불가(ios-shell.md §6).
  - Web Push / APNs / 이메일 / 메신저 전달은 모두 **외부 발송** → [건영 승인 필요]. M1 범위 밖이고 `Deliverer` 인터페이스 청사진으로만 둔다.
- 끄기 우선: `GW_PROACTIVE_ENABLED`, `users.settings.proactive.enabled`, `briefing.enabled`, job.enabled 중 하나라도 off면 실행하지 않는다. 헬퍼가 응답하지 않으면 그 섹션을 "일정 확인 불가"로 표시하고 나머지만 보낸다(실패 이유 코드만 기록).

## 4. 메모리 5.0.0 연동
- **읽기 전용**: 승인된(`approved`) 항목만 users.id 단위로 불러와 개인화에 쓴다(예: "아침엔 짧게", 가족 일정 우선 표시).
- 게이트: `memory.enabled=false`(사용자 kill_switch)나 시스템 메모리 off/503, `ui.memory_intro_seen=false`면 **메모리를 넣지 않는다**(브리핑 자체는 보낸다).
- 브리핑 생성 경로에는 `memory.propose`를 노출하지 않는다 → 자동 저장 경로가 없다. 일반 대화에서 저장할 때는 기존 승인 카드를 쓴다.
- 민감 카테고리 규칙(#54)을 그대로 따른다. 캘린더 데이터는 메모리로 자동 복사하지 않는다.

## 5. API · 설정 · 스키마 스케치
- 설정(`/v1/settings`, allowlist 추가):
  - `briefing.enabled` bool, **기본 false**
  - `briefing.time` "HH:MM", 기본 "07:30"
  - `briefing.tz`, 기본 사용자 tz → 없으면 Asia/Seoul
  - `briefing.skip_if_empty` bool, 기본 true
  - `pim.enabled` bool, 기본 false(툴 노출 여부)
- 서버 설정: `pim.socket_path`, `pim.owner_user_id`(하드코딩 대신 설정, 청사진 = 사용자별 매핑 테이블)
- API: 새 엔드포인트 없음. 설정 저장 시 서버가 `scheduled_jobs`를 upsert하고, 스레드·메시지는 기존 API로 본다. 테스트용 `POST /v1/briefing/preview`(세션 전용, 저장·발송 없이 미리보기)는 선택.
- DB: 새 테이블 없음. `scheduled_jobs.template_id='morning_briefing'`. `last_status`에 `skipped_empty` 값 추가(CHECK가 있으면 마이그레이션 1개).
- **기본 off 이유**: 캘린더는 새 민감 데이터 범주이고, macOS TCC를 건영이 직접 승인해야 하며, 선톡은 사용자가 스스로 켜야 스팸이 되지 않는다. 켜는 것은 설정 토글 하나다.

## 6. 보안 고려
- 새 신뢰 경계는 gateway↔`eve-pim` 소켓이다. 헬퍼는 소켓 peer uid가 `_evegw`인지 확인하고(`getpeereid`), 다르면 거부한다.
- 일정 제목·메모는 외부인이 넣을 수 있는 **untrusted 입력**이다(초대 메일 등). untrusted로 래핑하고, 브리핑 생성 중에는 쓰기 툴을 노출하지 않는다 → 인젝션이 쓰기로 이어지지 않는다.
- 로그와 `tool_audit`에는 이벤트 id와 개수만 남긴다. 제목·시간 원문은 남기지 않는다.
- 쓰기는 확인 카드 + 1회 승인 = 1회 실행 + 멱등 키(EventKit에는 멱등 키가 없으므로 **재시도하지 않는다**).
- 플래그 요약: [건영 승인 필요] = 리마인더/캘린더 쓰기(계정 동기화로 외부 전파), CalDAV(iCloud/Google)·Google API, 모든 외부 푸시·메일·메신저 전달.

## 7. 확장 청사진
- `calendar.add_event`/`update`: 같은 확인 게이트로 verb만 추가(MINOR).
- 사용자별 데이터 소스: `pim_sources(user_id, kind, ref)` 테이블 + 어댑터 인터페이스(`EventKit|CalDAV`).
- 전달: `Deliverer`에 WebPush/APNs(TestFlight 이후)를 추가한다. 각각 외부 발송 승인을 받는다.
- 다른 선제 트리거(일정 30분 전 알림 등): 같은 job/템플릿 구조로 추가한다.

## 8. SemVer 초기 판단
- **MINOR**(기존 계약에 추가만 하고 기본 off): 새 툴, 설정 키, 템플릿, `last_status` 값 추가.
- 단, 다음 중 하나라도 들어가면 **MAJOR**로 다시 판단한다: 쓰기 verb가 외부 계정으로 동기화되는 것을 기본 on, 외부 전달 채널, 새 host 프로세스의 보안 경계를 보안 담당이 "경계 변경"으로 판정하는 경우. 확실하지 않으면 MAJOR 원칙에 따라, **헬퍼 소켓 경계에 대한 보안 판정 전까지는 잠정 MINOR**로 둔다.
- 선행 조건: 선톡 스케줄러(proactive-messaging) 구현, #21 확인 게이트, #23 4.0.0 컷오버.

## 9. 보안 질문
1. gateway↔`eve-pim`을 사용자 LaunchAgent의 unix 소켓(전용 그룹 `_evepim` + `getpeereid` uid 검사)으로 연결하는 경계를 승인하는가? 아니면 loopback TCP + 공유 비밀을 원하는가?
2. 툴 결과에서 일정 메모·참석자·위치를 빼고 제목만 untrusted로 넣는 최소화와, 브리핑 생성 중 쓰기 툴을 노출하지 않는 것으로 인젝션 대응이 충분한가?
3. 브리핑 스레드에 일정 원문이 `messages`에 남는다. 보존 기간을 따로 둘까(예: 30일 후 자동 삭제), 아니면 일반 스레드 정책을 따를까? 자동 삭제는 데이터 삭제라 건영 승인 대상이다.
