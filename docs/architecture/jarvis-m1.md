# Jarvis M1 — 캘린더·리마인더 연동 + 아침 브리핑 — **제안 · 미구현**

상태: **보안 조건부 승인, 머지 전 최종 head 리뷰**.

> **경계 원칙(인증 독립)**: 기능은 인증 방식에 의존하지 않는다. 기능·라우트는 인증 미들웨어가 요청 컨텍스트에 붙인 principal `{user_id, scopes}`(내부 `users.id`)만 보고 필요한 scope만 검사하며, 쿠키·세션·페어링·Bearer 구분은 인증 계층 안에만 둔다.

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
| `calendar.add_event(...)` | `calendar.write` | `add` | **필수** — M1 범위(§10.1 승인됨). 인자·카드는 §11.4 |

- 쓰기는 모두 기존 `pending_actions` 확인 게이트를 탄다. 모델은 `{"status":"pending_confirmation","action_id":...}`만 받고, 승인은 `interactive` scope가 필요하다(없으면 403 `scope_denied`, 현재 API 키가 해당). 자동 승인은 없다. 메모리 approve 흐름과 같은 규칙이다.
- **확인 카드 내용(보안 조건)**: 카드 `payload_view`에 실제 쓸 값 그대로 — 제목(title), 기한(due), 대상 목록(list) — 를 표시한다. 같은 턴에 툴 결과(일정·리마인더 내용)를 읽은 뒤 나온 쓰기 제안도 허용하되, 카드에 정확한 값이 보여야 하고 사용자가 승인해야만 실행된다.
- **읽기 상한(보안 조건)**: `calendar.list_events` 기간은 최대 31일, 결과 최대 50건. `reminders.list`는 최대 100건. 초과하면 잘라서 `truncated:true`로 표시한다.
- **라우트 권한 테스트**: Jarvis의 `interactive` 전용 라우트(approve, `/v1/settings`의 briefing·pim 키 변경, 선택 `POST /v1/briefing/preview`)를 `TestRouteAuthMatrix`에 반드시 추가한다(`interactive` 없으면 403).
- 결과 형식(기존 `tool_result` 그대로):
  `{"status":"ok","data":{"events":[{"id":"<opaque>","title":"...","start":"2026-10-28T09:00:00+09:00","end":"...","all_day":false,"calendar":"<name>"}]}}`
  리마인더: `{"id","title","due","list","done"}`. 메모·참석자·위치·URL은 M1에서 **반환하지 않는다**(최소 데이터, 프롬프트 인젝션 면적 축소). title은 untrusted로 래핑한다(web_search와 같은 방식).
- 에러는 `error_code`만 쓴다: `pim_unavailable`, `permission_denied`(TCC 미허용), `kill_switch`, `timeout`.
- 툴 이름은 레지스트리 규칙상 wire에서 `calendar_list_events`, `reminders_list`, `reminders_add`, `reminders_complete`, `calendar_add_event`다(위 표의 점 표기는 설계 별칭). UI에 내려가는 이벤트 형식과 전체 오류 코드는 **§11 UI 포맷 계약**이 정한다.

## 2. 데이터 소스
| 안 | 방식 | 데이터 경로 | 평가 |
|---|---|---|---|
| **A. EventKit 헬퍼 (추천)** | host 사용자 세션의 작은 Swift 데몬 `eve-pim`(LaunchAgent)이 EventKit을 읽고 쓴다. gateway는 로컬 unix 소켓으로 호출한다 | host 안에서 끝난다(gateway↔헬퍼). 캘린더 계정 동기화는 macOS가 이미 하고 있는 것 | 단순하다. macOS 캘린더/미리알림에 연결된 모든 계정(iCloud·Google·CalDAV)을 한 번에 커버한다 |
| B. CalDAV 직접 | gateway가 CalDAV 서버에 접속 | **iCloud/Google CalDAV면 외부 API** [건영 승인 필요]. tailnet 안 자체 서버(예: Radicale)면 내부 | 앱 비밀번호 보관이 필요하고 리마인더(VTODO) 지원이 제각각 |
| C. Google Calendar API | OAuth + REST | **외부 API, 토큰 외부 발급** [건영 승인 필요] | 리마인더는 별도 API(Tasks). 상자가 가장 많다 |

**추천: A.** 주의:
- EventKit은 TCC 권한이 필요하다 → 헬퍼는 서비스 계정 `_evegw`가 아니라 **건영 로그인 세션의 LaunchAgent**로 돌고, 처음 한 번 권한 팝업을 승인한다(sudo 불필요, 건영 직접).
- 소켓: `<user-run-dir>/pim.sock` 0660, 그룹은 `_evegw`만 들어 있는 전용 그룹 `_evepim`(사람·에이전트는 넣지 않음). Tailscale serve와 무관한 host 내부 IPC라 #23의 샌드박스 문제는 없다. 소켓 디렉터리 소유·권한 검사(fail-closed)는 #23 런북 §0 규칙을 재사용한다.
- **헬퍼 보안 조건(보안 합의)**:
  - 소켓 디렉터리는 **0750, 고정 소유자**(건영 계정:`_evepim`)다.
  - 기동 시 소켓 경로에 기존 파일이나 심볼릭 링크가 있으면(`lstat`) **기동을 거부**한다(자동 삭제 없음).
  - peer uid(`getpeereid`)가 allowlist(`_evegw`)와 **정확히 일치**해야 한다.
  - 요청 스키마는 고정이고 크기 상한(예: 8 KiB)이 있다. 모르는 필드나 메서드는 거부한다.
  - 헬퍼에는 **네트워크 접근이 없다**(소켓 외 리스너 없음, 외부 연결 코드 없음. 가능하면 sandbox 프로필로 network deny).
  - 로그에 **일정·리마인더 내용(제목·시간·메모)을 남기지 않는다**.
- 헬퍼 API는 툴 표와 1:1인 최소 JSON-RPC만 둔다: `list_events`, `list_reminders`, `add_reminder`, `complete_reminder`, 그리고 §10.1·§11.4로 더해진 `add_event`와 읽기 전용 `list_calendars`(id·이름·쓰기 가능 여부만, 일정 내용 없음) — 합계 6개. 같은 고정 스키마·크기 상한·peer uid 조건을 적용한다. 임의 쿼리·삭제·캘린더 목록 변경은 없다.
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
- API: 새 엔드포인트 없음. 설정 저장 시 서버가 `scheduled_jobs`를 upsert하고, 스레드·메시지는 기존 API로 본다. 테스트용 `POST /v1/briefing/preview`(`interactive` scope, 저장·발송 없이 미리보기)는 선택.
- DB: 새 테이블 없음. `scheduled_jobs.template_id='morning_briefing'`. `last_status`에 `skipped_empty` 값 추가(CHECK가 있으면 마이그레이션 1개).
- **기본 off 이유**: 캘린더는 새 민감 데이터 범주이고, macOS TCC를 건영이 직접 승인해야 하며, 선톡은 사용자가 스스로 켜야 스팸이 되지 않는다. 켜는 것은 설정 토글 하나다.

## 6. 보안 고려
- 새 신뢰 경계는 gateway↔`eve-pim` 소켓이다. 헬퍼는 소켓 peer uid가 `_evegw`인지 확인하고(`getpeereid`), 다르면 거부한다.
- 일정 제목·메모는 외부인이 넣을 수 있는 **untrusted 입력**이다(초대 메일 등). untrusted로 래핑하고, 브리핑 생성 중에는 쓰기 툴을 노출하지 않는다 → 인젝션이 쓰기로 이어지지 않는다.
- 로그와 `tool_audit`에는 이벤트 id와 개수만 남긴다. 제목·시간 원문은 남기지 않는다.
- 쓰기는 확인 카드 + 1회 승인 = 1회 실행 + 멱등 키(EventKit에는 멱등 키가 없으므로 **재시도하지 않는다**).
- 플래그 요약(§10의 세 항목은 승인됨 — 건영 직접, 2026-10-28 00:41 KST. 그 밖의 항목은 여전히 [건영 승인 필요]): [건영 승인 필요] = 리마인더/캘린더 쓰기(계정 동기화로 외부 전파), CalDAV(iCloud/Google)·Google API, 모든 외부 푸시·메일·메신저 전달.

## 7. 확장 청사진
- `calendar.add_event`는 §10.1로 M1에 들어왔다(§11.4). `update`/삭제 같은 verb는 같은 확인 게이트로 나중에 추가한다(MINOR).
- 사용자별 데이터 소스: `pim_sources(user_id, kind, ref)` 테이블 + 어댑터 인터페이스(`EventKit|CalDAV`).
- 전달: `Deliverer`에 WebPush/APNs(TestFlight 이후)를 추가한다. 각각 외부 발송 승인을 받는다.
- 다른 선제 트리거(일정 30분 전 알림 등): 같은 job/템플릿 구조로 추가한다.

## 8. SemVer 판단: **MAJOR**
- UI 포맷 계약(§11)의 SemVer 영향은 §11.6 한 줄 참조.
- 새 로컬 신뢰 경계(gateway↔`eve-pim` 소켓)와 새 TCC 개인 데이터(캘린더·미리알림) 접근이 생기므로 MAJOR다(보안 경계 변경). 아래 이전 초안의 MINOR 판단을 대체한다.

### (이전 초안)
- **MINOR**(기존 계약에 추가만 하고 기본 off): 새 툴, 설정 키, 템플릿, `last_status` 값 추가.
- 단, 다음 중 하나라도 들어가면 **MAJOR**로 다시 판단한다: 쓰기 verb가 외부 계정으로 동기화되는 것을 기본 on, 외부 전달 채널, 새 host 프로세스의 보안 경계를 보안 담당이 "경계 변경"으로 판정하는 경우. 확실하지 않으면 MAJOR 원칙에 따라, **헬퍼 소켓 경계에 대한 보안 판정 전까지는 잠정 MINOR**로 둔다.
- 선행 조건: 선톡 스케줄러(proactive-messaging) 구현, #21 확인 게이트, #23 4.0.0 컷오버.

## 9. 보안 질문
1. gateway↔`eve-pim`을 사용자 LaunchAgent의 unix 소켓(전용 그룹 `_evepim` + `getpeereid` uid 검사)으로 연결하는 경계를 승인하는가? 아니면 loopback TCP + 공유 비밀을 원하는가?
2. 툴 결과에서 일정 메모·참석자·위치를 빼고 제목만 untrusted로 넣는 최소화와, 브리핑 생성 중 쓰기 툴을 노출하지 않는 것으로 인젝션 대응이 충분한가?
3. 브리핑 스레드에 일정 원문이 `messages`에 남는다. 보존 기간을 따로 둘까(예: 30일 후 자동 삭제), 아니면 일반 스레드 정책을 따를까? 자동 삭제는 데이터 삭제라 건영 승인 대상이다.

## 10. 승인된 범위 확장 — **승인됨 (건영 직접, 2026-10-28 00:41 KST)**
아래 셋은 M1 범위다. 위 보안 조건(§1 확인 카드·`interactive` scope·읽기 상한, §2 헬퍼 조건, §6)은 모두 그대로 적용한다.

### 10.1 리마인더·캘린더 쓰기 (iCloud 동기화) — 승인됨 (건영 직접, 2026-10-28 00:41 KST)
- verb: `reminders.add`, `reminders.complete`, `calendar.add_event`(추가). 모두 확인 카드에 실제 값(제목·기한/시각·대상 목록/캘린더)을 표시하고, `interactive` scope로 승인해야 하며 재시도하지 않는다.
- 쓰기는 EventKit 로컬 저장소에 하지만, macOS가 연결된 계정(iCloud 등)으로 동기화해 **외부로 나간다**. 운영 플래그 `pim.writes_enabled`로 끌 수 있다(끄기 우선).
- 삭제 verb는 없다.

### 10.2 외부 전달 (Web Push / 이메일, 사용자 본인에게만) — 승인됨 (건영 직접, 2026-10-28 00:41 KST)
- `Deliverer` 인터페이스에 `webpush`, `email`을 추가한다. 수신자는 **해당 users.id 본인으로 고정**한다(본인이 설정에 등록·검증한 endpoint/주소 1개, 제3자 발송 불가, 모델이 수신자를 정할 수 없음).
- payload에는 "아침 브리핑이 준비됐어요" 수준만 담는다(일정 원문 없음). 본문은 앱에서 본다(proactive-messaging §2.4 원칙).
- 설정: `briefing.delivery`(`inapp`(기본) | `webpush` | `email`), 운영 플래그 `GW_OUTBOUND_ENABLED`(기본 false). 끄기 우선.
- iOS 셸 푸시(APNs)는 무료 계정이라 불가하고, TestFlight 이후 별도로 다룬다.

### 10.3 브리핑 자동 삭제 (기본 30일 보존) — 승인됨 (건영 직접, 2026-10-28 00:41 KST)
- 브리핑 스레드의 `origin='proactive'`이고 `template_id='morning_briefing'`인 메시지를 생성 후 30일이 지나면 삭제한다(일 1회 job). 다른 스레드나 메모리는 건드리지 않는다.
- 설정: `briefing.retention_days`(기본 30, 0 = 자동 삭제 안 함). 삭제 건수만 `tool_audit`에 남기고 내용은 남기지 않는다.

## 11. UI 포맷 계약 — 캘린더 연동·아침 브리핑(M1)

대상: 프론트(eve-web)와 백엔드. 기존 표기 재사용이 원칙이고 **새 엔드포인트는 없다**(단순함 우선). 근거: `docs/web-search.md` §2.7, `docs/tool-platform.md` §10.3, [ui-api-contract.md](ui-api-contract.md) Q14·Q19·Q20·§3.3, 게이트웨이 `internal/handlers/web.go`의 SSE 출력(`emitCall`), eve-web 1.9.0의 `app.js`·`proactive.js`·`proactive-api.js`.

### 11.0 기존 형식 요약 (바뀌지 않음)
- SSE는 모두 `data: {json}` 한 줄이고 `object`로 구분한다. 서버가 `created`(초)를 붙인다. 순서는 `tool_call` → `tool_result` → 본문 delta → `sources` → `persisted` → `[DONE]`.
- 웹 툴: `tool_call {id, tool, args, round}`, `tool_result {id, tool, status, sources, blocked_count?}`. `status`는 `ok|error|blocked|timeout`.
- 확인 카드: `chat.completion.confirmation`(Q14·Q19). 서버 거부는 `status:"denied"` + 고정 `error_code`. UI는 모르는 코드를 `invalid` 문구로 처리한다. `payload_view.verb`는 서버가 매니페스트 허용 목록에서 채운다. 확인 카드는 토큰을 싣지 않는다.
- 현재 eve-web은 **툴 이름과 상관없이** `tool_call`을 "웹 검색 중…"으로, 소스 없는 비-ok `tool_result`를 "검색 결과 없이 답변 작성 중…"으로 표시한다 → 프론트는 `tool`로 분기해야 한다(§11.1 문구 표).

### 11.1 tool_call / tool_result (읽기 2종 + 쓰기 3종)
기존 두 이벤트를 그대로 쓰고, PIM 툴에서만 아래 필드를 더한다(웹 툴 이벤트는 그대로, 필드 추가만 = 하위 호환).

| 필드 | 대상 | 규칙 |
|---|---|---|
| `tool_call.args` | 읽기 | 모델이 보낸 인자 그대로(날짜·불리언뿐, 일정·리마인더 내용 없음). |
| `tool_call.args` | 쓰기 | **항상 `{}`**. 값은 확인 카드에만 보인다(모델 원본 인자가 카드보다 먼저 사실처럼 보이지 않게). |
| `tool_result.status` | 공통 | `ok` \| `error` \| `timeout` \| `denied` \| `pending_confirmation`(쓰기 성공 경로). `blocked`·`sources`는 PIM 툴에 없다. |
| `tool_result.count` | 읽기 ok | 돌려준 항목 수(정수). |
| `tool_result.truncated` | 읽기 ok | 상한(§1: 일정 50, 리마인더 100) 때문에 **잘렸으면 true**. 기간 31일 초과로 줄인 경우도 true. |
| `tool_result.error_code` | 실패 | §11.2 이유 코드 하나. `message`·상세 필드는 없다. |
| `tool_result.card` | 읽기, 쓰기 실패 | UI 카드 오브젝트(§11.2). 쓰기 성공(`pending_confirmation`)에는 없다(카드는 `confirmation`이 그린다). |
| `tool_result.action_id` | 쓰기 pending | 확인 카드의 id. 같은 턴에 `confirmation` 이벤트가 **바로 뒤에** 온다. |

읽기 성공 — 일정 (`calendar_list_events`):
```json
{"object":"chat.completion.tool_call","id":"call_1_0","tool":"calendar_list_events","args":{"from":"2026-10-28T00:00:00+09:00","to":"2026-10-29T00:00:00+09:00"},"round":1,"created":1792000000}
{"object":"chat.completion.tool_result","id":"call_1_0","tool":"calendar_list_events","status":"ok","count":2,"truncated":false,"created":1792000001,
 "card":{"kind":"calendar_events","v":1,"range":{"from":"2026-10-28T00:00:00+09:00","to":"2026-10-29T00:00:00+09:00"},
  "items":[{"id":"evt_a1","title":"팀 스탠드업","start":"2026-10-28T09:30:00+09:00","end":"2026-10-28T10:00:00+09:00","all_day":false,"calendar":"회사","calendar_id":"cal_1"},
           {"id":"evt_a2","title":"기념일","start":"2026-10-28T00:00:00+09:00","end":"2026-10-29T00:00:00+09:00","all_day":true,"calendar":"개인","calendar_id":"cal_2"}]}}
```
읽기 성공 — 리마인더 (`reminders_list`), 잘린 경우:
```json
{"object":"chat.completion.tool_call","id":"call_1_1","tool":"reminders_list","args":{"due_before":"2026-10-29T00:00:00+09:00","include_done":false},"round":1,"created":1792000002}
{"object":"chat.completion.tool_result","id":"call_1_1","tool":"reminders_list","status":"ok","count":100,"truncated":true,"created":1792000003,
 "card":{"kind":"reminders","v":1,"due_before":"2026-10-29T00:00:00+09:00","items":[{"id":"rem_b1","title":"우유 사기","due":"2026-10-28T18:00:00+09:00","list":"장보기","done":false}]}}
```
(`card.items`는 위 예시에서 한 개만 적었다. 실제로는 `count`개다.)

쓰기 — 카드 제안 (`reminders_add`, `reminders_complete`, `calendar_add_event` 동일):
```json
{"object":"chat.completion.tool_call","id":"call_1_2","tool":"reminders_add","args":{},"round":1,"created":1792000004}
{"object":"chat.completion.tool_result","id":"call_1_2","tool":"reminders_add","status":"pending_confirmation","action_id":"01JBX0000000000000000000A1","created":1792000005}
```
이어서 `chat.completion.confirmation`이 온다(카드 필드는 기존 그대로, `payload_view`는 §11.4).
- 쓰기 툴의 검증·거부 실패는 `confirmation` 이벤트 **없이** `tool_result`가 `status:"denied"`/`error` + `error_code` + 오류 카드(`kind:"error"`, §11.2)를 낸다(읽기 오류와 같은 경로, 현재 구현 `proposePimWrite`가 `confirmation`을 내지 않는 것과 일치). 한 호출에 카드는 `confirmation` 또는 오류 카드 중 하나뿐이다.
- 필드 요약 — 일정 항목: `id`(불투명), `title`(최대 200자, 넘으면 `…`로 자름), `start`, `end`(RFC3339, **`+09:00`로 정규화**), `all_day`, `calendar`(이름), `calendar_id`(불투명, `calendar_add_event`에 쓰는 값). 종일 일정은 `start`가 KST 00:00, `end`는 **다음 날 00:00(배타적)**. 리마인더 항목: `id`, `title`, `due`(RFC3339 `+09:00` 또는 `null`), `list`(이름), `done`. 메모·참석자·위치·URL은 반환하지 않는다(§1).
- 일정·리마인더 제목은 **untrusted 텍스트**다. UI는 `textContent`로만 그린다(HTML·마크다운 해석 없음).
- **저장 안 함**: `card`는 스트림 전용이다. DB 메시지에는 저장하지 않고, 재연결 재생(§3.8의 메모리 이벤트 로그, 최대 10분)에만 남는다. 스레드를 다시 열면 카드는 없고 답변 본문만 있다.
- 상태 문구(프론트 고정, 권장): `calendar_list_events` "일정 확인 중…", `reminders_list` "리마인더 확인 중…", `reminders_add`/`reminders_complete`/`calendar_add_event` "확인 카드를 준비하는 중…". 실패한 읽기 뒤에는 "확인하지 못한 채로 답변 작성 중…". 웹 검색 문구를 재사용하지 않는다.

### 11.2 읽기 결과·오류 = `tool_result.card` (새 엔드포인트 없음)
`card`는 `{kind, v, ...}` 오브젝트이고 `v`는 카드 스키마 버전(정수, 지금 1)이다. UI는 **모르는 `kind`나 더 큰 `v`는 카드를 그리지 않고** 상태 문구만 둔다(전방 호환, 새 kind 추가는 MINOR).

| `kind` | 언제 | 본문 |
|---|---|---|
| `calendar_events` | `calendar_list_events` ok | `range{from,to}`, `items[]`(위 예시). 비어 있으면 `items:[]`, `count:0` |
| `reminders` | `reminders_list` ok | `due_before`(없으면 `null`), `items[]` |
| `error` | 읽기·쓰기 실패 | `{"kind":"error","v":1,"error_code":"permission_denied"}` — **이유 코드 하나만** |

오류 카드 예:
```json
{"object":"chat.completion.tool_result","id":"call_1_0","tool":"calendar_list_events","status":"denied","error_code":"permission_denied","created":1792000001,"card":{"kind":"error","v":1,"error_code":"permission_denied"}}
```
- `error_code`는 `tool_result` 최상위와 `card.error_code`에 **같은 값**을 둔다(불일치 시 테스트 실패).
- **이유 코드(고정 enum, 내부 정보 없음)**: `pim_unavailable`(헬퍼 연결 불가·응답 이상) · `permission_denied`(macOS TCC 미허용) · `scope_denied`(scope 없음) · `kill_switch`(툴 꺼짐: `pim.enabled`/킬스위치/`pim.writes_enabled`) · `timeout` · `invalid_arguments`(인자 오류) · `not_found`(없는 리마인더·캘린더 id) · `writes_disabled`(헬퍼 쓰기 꺼짐). 쓰기 실패에는 Q14 코드(`secret_detected`, `cap_exceeded` …)도 같은 방식으로 쓴다. 새 코드 추가는 MINOR, UI는 모르는 코드를 `invalid` 문구로 처리한다.
- 코드→status: `permission_denied`·`scope_denied`·`kill_switch`·`tainted` 등 거부류는 `denied`, `timeout`은 `timeout`, 나머지(`pim_unavailable`, `invalid_arguments`, `not_found`, `writes_disabled`)는 `error`.
- **노출 금지**: 소켓 경로, 헬퍼 오류 문자열, macOS 오류 번호, 스택, 호스트·사용자 이름, 일정·리마인더 내용, TCC 설정 위치. 로그에는 툴·status·`error_code`·count만 남긴다(§6).
- UI 고정 문구(제안): `pim_unavailable` "캘린더·리마인더에 연결할 수 없어요" · `permission_denied` "캘린더·리마인더 접근 권한이 없어요. 서버에서 권한을 허용해야 해요" · `scope_denied` "이 작업은 할 수 있는 권한이 없어요" · `kill_switch` "지금은 이 기능을 쓸 수 없어요" · `timeout` "응답이 늦어 확인하지 못했어요" · `invalid_arguments` "요청 내용이 올바르지 않아요" · `not_found` "대상을 찾을 수 없어요" · `writes_disabled` "지금은 쓰기가 꺼져 있어요".
- 모델에게는 기존 형식 그대로(`{"status":"ok","data":…}` / `{"status":"error","error_code":…}`, untrusted 래핑)를 준다. `card`는 UI 전용이다.

### 11.3 저장되는 브리핑 메시지 형식
브리핑 메시지는 기존 선톡 Message(`origin:"proactive"`, `template_kind:"briefing"`, Q20)이고, `content`는 요약 텍스트다. 구조화 정보는 **Message 최상위 `briefing` 오브젝트**(Q20의 `template_kind`와 같은 레벨)로 내려준다. 저장은 `messages.metadata`(JSONB, 새 컬럼·테이블 없음)이고, 서버가 검증한 필드만 API에 내보낸다. 브리핑이 아닌 메시지는 `briefing: null`.

```json
{"id":"01JBM0000000000000000000B1","origin":"proactive","job_id":"01JBJ0000000000000000000J1","kind":null,"template_kind":"briefing","read_at":null,
 "provenance":{"actor":"scheduler","job_id":"01JBJ0000000000000000000J1","job_name":"아침 브리핑"},
 "content":"오늘은 일정 2건, 마감 리마인더 1건이에요. …",
 "briefing":{"schema":1,"status":"partial","date":"2026-10-28","tz":"Asia/Seoul","generated_at":"2026-10-28T07:30:02+09:00","text_source":"model",
  "sections":[
   {"kind":"events","status":"ok","count":2,"truncated":false,"items":[{"title":"팀 스탠드업","start":"2026-10-28T09:30:00+09:00","end":"2026-10-28T10:00:00+09:00","all_day":false,"calendar":"회사"}]},
   {"kind":"reminders","status":"error","error_code":"timeout","count":0,"truncated":false,"items":[]}]}}
```
- `schema`: 정수, 지금 1. UI는 더 큰 값이면 `content`만 보여 준다. 필드 추가는 schema를 올리지 않고(MINOR), 의미가 바뀌는 변경만 올린다(MAJOR).
- `date`는 브리핑 대상 날짜(`tz` 기준 로컬 날짜), `generated_at`은 RFC3339 `+09:00`. `tz`는 `briefing.tz`(§5)다.
- `text_source`: `model`(모델 요약) \| `template`(모델 출력이 검증에 실패해 서버 고정 렌더링으로 대체, 백엔드 `Fallback`) \| `fixed`(모델 호출 없이 고정 문구, 백엔드 `Fixed`).
- **sections**: 순서 고정(`events`, `reminders`). `kind`는 UI가 모르면 건너뛴다(새 kind = MINOR; 메모리는 입력으로만 쓰이므로 section이 아니다).
  - `status`: `ok` \| `error`(+`error_code`, §11.2 코드).
  - `count`: 수집된 전체 항목 수(§1 상한 안: 일정 ≤50, 리마인더 ≤100).
  - `items`: 앞에서 **최대 10개**. 일정 `{title,start,end,all_day,calendar}`, 리마인더 `{title,due,list,overdue}`(`overdue`: 오늘 이전 기한). **id는 저장하지 않는다**(불투명 id를 DB에 남기지 않음).
  - `truncated`: **보여 주는 `items`가 전체보다 적으면 true**(10개 제한 또는 읽기 상한). 건수 표시는 `count`, "외 N건"은 `count - items.length`.
- 메시지 `briefing.status`(메시지가 저장된 경우만):

| 값 | 의미 |
|---|---|
| `ok` | 모든 section이 `ok`(비어 있어도 ok. `skip_if_empty=false`일 때 "오늘은 일정이 없어요") |
| `partial` | 한 section 이상 `ok`이고 한 section 이상 `error`. 실패한 section은 UI에 "확인 불가"로 표시 |
| `unavailable` | section이 모두 `error`이거나 PIM이 꺼져 `sections:[]`. `content`는 고정 문구, `text_source:"fixed"` |

- **메시지를 만들지 않는 경우**는 job의 `scheduled_jobs.last_status`에만 남는다(백엔드 구현 이름 그대로): `skipped_empty`(비어 있고 `skip_if_empty=true`), `skipped_off`(끄기 우선 게이트 중 하나가 off), `error`(수집·생성·저장 실패, 메시지 없음). 메시지가 만들어진 실행의 `last_status`는 메시지 `briefing.status`와 같은 값(`ok`/`partial`/`unavailable`)이다. 위 값들은 모두 `last_status`가 자유 텍스트라 마이그레이션이 없다(§5의 CHECK 가정은 해당 없음). "failed"/"skipped"라는 이름은 쓰지 않는다.
- 보존: `briefing` 오브젝트는 메시지와 함께 §10.3의 30일 자동 삭제 대상이다. 감사·로그에는 status, section 코드, 건수만 남기고 `items`는 남기지 않는다.

### 11.4 `calendar_add_event` 인자와 확인 카드
**인자**(strict decode, 모르는 필드·`null`·중복 키·타입 오류 → `invalid_arguments`):

| 인자 | 타입 | 필수 | 규칙·기본값 |
|---|---|---|---|
| `title` | string | 예 | 1–200자. 앞뒤 공백 제거, NFKC, 제어문자·zero-width 있으면 거부, 시크릿 필터 걸리면 거부 |
| `start` | string | 예 | RFC3339 **타임존 포함 필수**(`Z` 또는 `±hh:mm`, 없으면 거부). **초·소수초는 0**이어야 한다(분 단위). 범위: 지금−1일 ~ 지금+365일 |
| `end` | string | 아니오 | RFC3339 타임존 포함, 초 0. `end > start`. 생략 시 일정은 `start+60분`, 종일은 `start+1일` |
| `all_day` | bool | 아니오 | 기본 `false`. `true`면 `start`·`end`는 **KST 00:00**이고 `end`는 배타적 다음 날 00:00 |
| `location` | string | 아니오 | 기본 없음. 최대 200자, 위 문자·시크릿 규칙 |
| `notes` | string | 아니오 | 기본 없음. 최대 500자(카드에 전부 보여 주기 위한 상한), 위 규칙 |
| `calendar_id` | string | 아니오 | 기본 없음 = **기본 캘린더**. 있으면 `calendar_list_events` 결과의 `calendar_id`이고, 헬퍼 `list_calendars`에서 쓰기 가능한 캘린더여야 한다(아니면 `not_found`) |

- 길이: 일정 최대 7일, 종일 최대 31일(넘으면 `invalid_arguments`). 반복·참석자·알림·URL·첨부는 **없다**(청사진).
- **정규화**: 서버는 `start`·`end`를 같은 시각의 `+09:00`(Asia/Seoul)로 바꾼 값으로 `payload`와 `payload_hash`를 만들고 그 값으로 실행한다. 한국은 서머타임이 없어 변환은 정확하다. 카드·실행·해시가 같은 정규화 값을 쓴다.
- 실행은 승인 때 저장된 `payload`만 쓴다(모델 인자 재사용 없음). `calendar_id`가 사라졌으면 쓰지 않고 `not_found`. 재시도하지 않는다(§6).

**확인 카드** (`chat.completion.confirmation`):
```json
{"object":"chat.completion.confirmation","action_id":"01JBX0000000000000000000C1","thread_id":"01J...","message_id":"01J...",
 "tool":"calendar_add_event","tool_version":"1","scopes":["calendar.write"],"payload_hash":"sha256:...",
 "provenance":{"actor":"user","job_id":null,"job_name":null},"tainted":false,"status":"pending",
 "expires_at":"2026-10-28T05:00:00Z","created_at":"2026-10-28T04:55:00Z","summary":"일정 추가",
 "payload_view":{"verb":"add","title":"치과","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00","all_day":false,
  "display":"10/28 (수) 15:00–16:00","location":"역삼동 치과","notes":null,"calendar_id":"cal_1","calendar":"개인"}}
```
- `payload_view`의 `title`, `start`, `end`, `all_day`, `location`, `notes`, `calendar_id`는 **실행될 인자와 값이 같다**(위 정규화 값. 없는 값은 `null`). `display`와 `calendar`는 서버가 만든 **표시 전용** 문자열이다. 둘 다 같은 `payload`에서 한 함수로 계산한다.
- **KST 표시 규칙** (`display`, 서버 생성, UI는 그대로 표시):
  - 항상 Asia/Seoul, 24시간 `HH:MM`, 요일은 한글 한 글자. 시간대 접미사는 붙이지 않는다(모든 표시가 KST).
  - 같은 날: `10/28 (수) 15:00–16:00`. 날짜가 다르면: `10/28 (수) 23:00 – 10/29 (목) 01:00`.
  - 종일: 하루 `10/28 (수) 종일`, 여러 날 `10/28 (수) – 10/30 (금) 종일`(끝 날짜는 포함, `end`보다 하루 앞).
  - 표시 연도는 현재(KST) 연도와 다를 때만 앞에 붙인다(`2027/1/5 (화) 09:00–10:00`).
- `calendar`: `calendar_id`가 `null`이면 `"기본 캘린더"`, 있으면 헬퍼 `list_calendars`로 찾은 이름. `calendar_id`를 `null`로 쓰는 경우 `calendar:"기본 캘린더"`가 실행 결과(헬퍼 기본 캘린더)와 같다.
- 시크릿 필터·표시용 redactor가 값을 가리게 되면 **거부**한다(`invalid_arguments`). 카드가 실제와 다른 값을 보여 주는 상황을 만들지 않는다(현재 구현의 `proposePimWrite` 규칙과 같다).
- 같은 턴에 읽은 일정 내용이 있어도 카드는 만들 수 있지만 `tainted:true` 경고를 붙인다(§1). `summary`는 서버 고정 문구다.
- **같은 규칙의 나머지 두 카드**(UI는 공용 "PIM" 카드 하나로 그린다. 행 이름은 UI 고정: 제목·시간·목록/캘린더·장소·메모):
  - `reminders_add`: `{"verb":"add","title":"우유 사기","due":"2026-10-28T18:00:00+09:00","display":"10/28 (수) 18:00","list":null}`. 인자는 `title`(1–200자), `due`(RFC3339 타임존 포함, 초 0, 선택, 정규화 `+09:00`), `list`(이름, 최대 100자, 선택). `list:null`은 UI에서 "기본 목록". 기한 없으면 `due:null`, `display:null`.
  - `reminders_complete`: `{"verb":"update","id":"rem_b1","title":"우유 사기","display":"10/28 (수) 18:00","list":"장보기"}`. 인자는 `id`뿐이고, 서버가 헬퍼로 조회한 제목·목록·기한을 카드에 넣는다(`id`는 표시하지 않음). 없거나 이미 완료면 `not_found`.
  - 현재 구현의 `payload_view.method`(헬퍼 메서드 이름)는 **UI에 필요 없으므로 제거**한다(내부 `payload`에만 남김).
- 모델 인자·사용자 입력으로 `verb`, `display`, `calendar`, `payload_view`를 바꿀 수 없다(Q19와 같다). 인자에 이 키가 있으면 `invalid_arguments`.

### 11.5 테스트 요건
`TestToolEvent_PimReadShape`(필드·`truncated`), `TestToolEvent_PimWriteArgsEmptyCardOnlyOnError`, `TestToolResult_ErrorCodeMirroredInCard`, `TestToolResult_ErrorNoInternalDetail`(소켓 경로·헬퍼 문자열 없음), `TestToolResult_CardNotPersisted`, `TestBriefing_StatusTable`(ok/partial/unavailable과 `last_status` 대응), `TestBriefing_ItemsCap10TruncatedMeaning`, `TestBriefing_NoIdsStored`, `TestAddEvent_ArgsStrict`(타임존 없음·초≠0·end≤start·상한·기본값), `TestAddEvent_NormalizedToKST`, `TestAddEvent_CardMatchesExecutedPayload`(`payload_view`의 인자 필드 == 헬퍼 요청, 골든), `TestAddEvent_DisplayFormat`(같은 날·다른 날·종일·다른 해), `TestAddEvent_ModelCannotSetView`, `TestAddEvent_RedactorMismatchRefuses`, `TestPimCard_NoMethodKey`.

### 11.6 SemVer
이 계약은 기존 이벤트·Message에 **필드·`kind`·코드 추가만** 하므로 단독으로는 MINOR이고, 기능 자체가 이미 M1 MAJOR(§8)이므로 **같은 MAJOR 릴리스에 포함되며 추가 MAJOR 없음**. 단 프론트가 `tool`로 분기하는 UI(eve-web 1.10.0)가 M1 기능 활성화(`pim.enabled=true`)보다 먼저 배포돼야 한다(1.9.0은 PIM 툴을 웹 검색 문구로 표시).
