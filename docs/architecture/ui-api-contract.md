# UI ↔ API 계약 — 선톡·메모리 (P1)

상태: 설계(미구현) · 근거: [proactive-messaging.md](proactive-messaging.md)(PM), [memory.md](memory.md)(MEM), `docs/tool-platform.md` §3.2·§10.3(TP), `docs/threads-api.md`
대상: eve-chat-ui-design `PROACTIVE-MEMORY-SPEC.md`의 열린 질문 Q1–Q18.

## 0. 원칙
- **가장 단순하게**: 새 엔드포인트는 `GET/PATCH /v1/settings`, `POST /v1/threads/{id}/read`, `POST /v1/proposals/{id}/revise`, `GET /v1/actions`, push 구독 3개(§3.5)뿐이고, 나머지는 기존 응답에 필드를 더한다.
- **기존 필드 재사용**: PM §3 컬럼(`origin`, `job_id`, `read_at`), `users.settings` JSONB, TP `pending_actions`·`payload_view`·`provenance`를 그대로 노출한다.
- **확장 메모**: 지금 안 하는 것은 §3에 청사진으로 남긴다.
- **SemVer**: 이 문서의 API 추가는 모두 **필드·엔드포인트 추가(하위 호환) = MINOR**. 다만 기능 자체(선톡·메모리)의 출시는 PM·MEM에 적힌 대로 MAJOR이며, 이 계약은 그 MAJOR 릴리스 안에 들어간다.

## 1. 결정 목록

| Q | 결정 | SemVer |
|---|---|---|
| Q1 | `GET /v1/threads/{id}/messages`의 Message에 `origin`(`user`\|`assistant`\|`proactive`), `job_id`(proactive만, 아니면 null), `read_at`(proactive만 의미, 아니면 null) 포함. DB `origin='user_turn'`은 기존 `role`로 `user`/`assistant`를 나눠 내보낸다. | MINOR |
| Q2 | Thread에 서버 계산 `unread_count`(읽지 않은 proactive 메시지 수, `threads.user_id` 조인 범위). 선톡 도착 시 `threads.updated_at`을 올린다(기존 `updated_at` 정렬 그대로). | MINOR |
| Q3 | proactive Message에 `provenance: {actor:"scheduler", job_id, job_name}`. `job_name`은 템플릿 `templates/<id>.yaml`의 `display_name`(서버 고정 문자열, 사용자 입력 아님). SSE confirmation `provenance`에도 같은 `job_name`. | MINOR |
| Q4 | 플래그 대신 Message `kind` 필드 하나: `null`(일반) \| `digest`(quiet hours 종료 요약). 그리고 proactive Message에 `template_kind`(`briefing`\|`watch`\|`followup`, 템플릿 yaml의 선언값)를 둬 UI가 출처 아이콘을 고른다. 새 값 추가는 MINOR, UI는 모르는 값을 기본 아이콘·`null`처럼 처리. | MINOR |
| Q5 | 별도 "job 전용 스레드" 종류는 없다. `scheduled_jobs.thread_id`는 nullable: follow-up은 원 스레드, 원 스레드가 없는 job(브리핑·watch)은 **첫 실행 때 서버가 스레드를 하나 만들고**(제목 = `job_name`) 그 id를 `thread_id`에 저장, 이후 실행은 재사용. 보통 Thread라 새 필드 없음(PM §3 갱신, 컬럼 추가만 = MINOR). | MINOR |
| Q6 | `POST /v1/threads/{id}/read` → 해당 스레드의 미읽음 proactive 메시지를 모두 `read_at=now()`, 응답 `{unread_count:0}`. 다른 사용자 스레드는 **404**. 기존 `POST /v1/messages/{id}/read`도 유지. CSRF·Origin 동일. | MINOR |
| Q7 | `GET /v1/settings`, `PATCH /v1/settings`(부분 갱신, merge). 키는 §2. 알 수 없는 키·타입·범위 오류는 400(PM S4-4). | MINOR |
| Q8 | 설정 응답에 읽기 전용 `system` 블록: `proactive_available`(=`GW_PROACTIVE_ENABLED` && !`proactive_off`), `tools_available`(킬스위치 `all_off`가 아님), `memory_available`(서버 킬스위치 `GW_MEMORY_ENABLED`). PATCH로 `system`을 보내면 400. | MINOR |
| Q9 | 만료된 제안의 "다시 요청" = `POST /v1/proposals/{id}/revise`. `item_ids`가 있는 제안(메모리 삭제)은 **원래와 같은 `item_ids`**, `item_ids`가 없는 제안(메모리 add, 툴, `proactive.propose_followup`)은 **빈 본문 = 같은 payload 재발급**. 새 제안·새 `payload_hash`·새 토큰, 원 제안은 `voided`. 새 모델 턴 없음. 규칙은 §3.1. | MINOR |
| Q10 | `token_expires_at`은 토큰과 함께 **세션 POST 응답**(reissue·revise)에만 포함(`expires_at`과 별개, TP 두 만료 규칙). SSE confirmation에는 토큰 관련 필드 없음(§3.4). | MINOR |
| Q11 | `memory.propose` `payload_view`: `{op:"add"\|"delete", items:[{id?, text, kind, updated_at?}], ambiguous:bool}`. add는 `id` 없음·정규화 후 텍스트, delete(후보)는 저장된 `id`·`text`·`kind`·`updated_at`(최근 수정일 표시용). | MINOR |
| Q12 | follow-up 생성 툴 이름 `proactive.propose_followup`(TP `<domain>.<verb>` 규칙, 스코프 `proactive.write`, 확인 게이트 필수). `payload_view: {when:{at:"RFC3339"}, thread_id, display:"10/28 (화) 09:00", template_id}`. `display`는 서버가 Asia/Seoul로 만든 표시 문자열. 보안 규칙은 §3.2. | MINOR |
| Q13 | SSE confirmation에 툴의 선언 `scopes`(예: `["memory.write"]`)를 그대로 포함. **위험 스타일은 스코프 verb `send`와 삭제(`payload_view.op="delete"` 또는 `delete` 스코프)에만**, `write`는 기본(primary) 스타일. | MINOR |
| Q19 | 확인 카드 `payload_view.verb`: 고정 enum `add`\|`update`\|`send`\|`delete`\|`run`. **서버가 툴 매니페스트의 정적 `verb` 필드(배포 시 정의, TP §1.1)로만 채운다.** 모델 출력·툴 인자·사용자 입력으로는 바꿀 수 없고, 자유 텍스트 label은 없다. UI가 verb → 한국어 버튼 문구를 매핑하고, **모르는 값은 `run`으로 처리**(전방 호환). **위험 스타일은 verb가 아니라 Q13의 scopes 규칙** 그대로. enum 값 추가는 MINOR. 테스트: (1) 모든 매니페스트 툴이 유효한 `verb`를 선언(누락·무효 시 CI 실패), (2) 모델 출력이 `verb`를 바꾸는 경로 없음(모델 args에 `verb`/`payload_view`를 넣어도 카드 verb = 매니페스트 값). | MINOR |
| Q20 | `template_kind`는 **Message 최상위 필드**(`origin`·`job_id`·`kind`와 같은 레벨)다. `provenance` 안에 넣지 않는다(`provenance`는 `{actor, job_id, job_name}`만). 예: `{"id":"…","origin":"proactive","job_id":"…","kind":null,"template_kind":"briefing","read_at":null,"provenance":{"actor":"scheduler","job_id":"…","job_name":"아침 브리핑"}}` | — |
| Q14 | 서버 거부는 **카드 error 상태**: SSE confirmation이 `status:"denied"`와 고정 enum `error_code`만 보낸다: `secret_detected` \| `sensitive_category` \| `cap_exceeded` \| `tainted` \| `scope_denied` \| `expired` \| `not_found` \| `item_gone` \| `conflict` \| `payload_mismatch` \| `kill_switch` \| `invalid`(매핑 §3.3). **자유 텍스트 요약 없음**(본문·매칭 내용 노출 방지). UI가 코드별 고정 문구를 쓴다. 값 추가는 MINOR, 모르는 코드는 `invalid` 문구. 모델 답변은 별개로 이어짐. | MINOR |
| Q15 | `selected_ids` 없음. 부분 해제 = `POST /v1/proposals/{id}/revise {item_ids}`, `item_ids ⊆ 원 제안`(아니면 400 `invalid`). 새 제안·새 `payload_hash`·새 토큰, 원 제안 `voided`. approve 본문은 그대로(TP). 규칙은 §3.1. | MINOR |
| Q16 | 매칭 없음 = **카드 없음**, 모델이 답한다. 별도 신호 필드 없음(P1). UI의 "메모리 관리 열기" 링크는 붙이지 않거나 고정 도움말로. (청사진 §3) | — |
| Q17 | 메모리 읽기 전용(MEM S5-R6)은 설정 필드로 노출하지 않는다. 쓰기 시 503 `memory_read_only`로만 드러난다(아키텍트 결정). `GET /v1/memory`에는 추가 필드 없음. | MINOR |
| Q18 | 스레드별 "메모리 없이"는 **P1 범위 밖**. (청사진 §3) | — |

## 2. 설정 (`users.settings`)

```json
GET /v1/settings
{
  "proactive": { "enabled": false,
                 "quiet_hours": {"start": "22:00", "end": "08:00"},
                 "memory_read": false },
  "memory":    { "enabled": false },
  "system":    { "proactive_available": false, "tools_available": true, "memory_available": false }
}
```

| 키 | 타입·기본 | 규칙 |
|---|---|---|
| `proactive.enabled` | bool, false | 사용자 선톡 on/off. `system.proactive_available=false`면 UI 토글 비활성. |
| `proactive.quiet_hours` | `{"start":"22:00","end":"08:00"}` | 24시간 `HH:MM`. 서버 시간대 **Asia/Seoul** 고정. `end < start`면 자정을 넘김(22:00–08:00). `start == end`는 400. `tz` 필드는 예약(나중에 추가, MINOR). |
| `proactive.memory_read` | bool, false | 선톡이 메모리를 읽을지(MEM §6, 기본 off). UI는 `memory.enabled=true`일 때만 표시. |
| `memory.enabled` | bool, **true**(5.0.0부터, 이전 false) | 메모리 주입·제안·승인 on/off(MEM §3). 키가 없으면 true(opt-out). 실제 적용 = `system.memory_available` AND `memory.enabled`. |
| `proactive.daily_cap` | **P1에 없음** | 서버 고정 하루 3건. 키는 예약: 나중에 1–3 범위, **낮추기만** 허용(MINOR). P1에 보내면 400(알 수 없는 키). |

- PM §3의 `quiet_start`/`quiet_end`/`daily_cap` 예시는 이 계약(`quiet_hours` 객체, `daily_cap` 미노출)으로 대체한다. PM의 서버 상한(코드 상수)은 그대로.
- PATCH는 **고정 구조체 strict decode**(Go struct + `DisallowUnknownFields`)로 받고, 지정된 키만 덮어쓴다(일반 map deep-merge 없음). 알 수 없는 키, `null`, 중복 키, 허용 깊이(2단계) 초과, 타입·범위 오류, `system` 키 → 400 `invalid`. CSRF·Origin 필수.
- 감사: `proactive.enabled`·`proactive.memory_read`·`memory.enabled` 변경은 `tool_audit` **security 이벤트**로 before/after 값을 기록(불리언만). quiet hours는 ops 이벤트.
- **킬스위치가 항상 우선**: 사용자 설정이 on이어도 `proactive_off`·`all_off`면 실행하지 않고, `system.*_available=false`로 보인다.

## 3. 보안 규칙 (보안 리뷰 반영)

### 3.1 `POST /v1/proposals/{id}/revise` [Sec-M1]
- 다른 사용자의 제안이면 **404**. 없는 제안과 **응답 본문·헤더가 동일**(`{"error_code":"not_found"}`)해 존재 여부를 노출하지 않는다. CSRF·Origin 필수.
- 요청 본문은 `item_ids`만 허용(strict decode). 다른 필드(`root_id`, `payload`, `when`, `scopes` 등)가 있으면 400 `invalid`.
- `item_ids`는 원 제안의 부분집합이고, **지금도 현재 사용자 소유로 존재하는지 다시 확인**(그사이 삭제된 id면 409 `item_gone`).
- 원 제안 무효화는 조건부 UPDATE: `UPDATE pending_actions SET status='voided' WHERE id=$1 AND user_id=$2 AND status='pending'` → **정확히 1행**이어야 하며, 같은 트랜잭션에서 새 제안 생성 + 원 토큰 무효화. 0행이면(이미 approve·void) 409 `conflict`. approve와 경쟁하면 둘 중 하나만 성공한다(approve도 같은 `status='pending'` 조건부 UPDATE).
- 제안에는 `root_id`(첫 제안 id)를 둔다. **서버만 설정**하며 API로 받지도 SSE·응답에 내보내지도 않는다. **토큰 발급 3회 상한은 revise 체인 전체(`root_id`)로 센다**.
- 제안 만료는 **`root.created_at + 24h`를 넘지 않는다**. 그 뒤 revise는 **410 `expired`**.
- 새 제안은 원 제안의 `actor`, `job_id`, taint 표시, `scopes`를 그대로 물려받는다(revise로 권한·출처를 바꿀 수 없음).
- revise는 **생성 시 검증을 전부 다시 실행**한다: 시크릿 필터·정규화(add), 스코프, 킬스위치, follow-up이면 `when.at`(미래·30일 이내), 활성 10개 상한, 템플릿 허용 목록. 하나라도 실패하면 해당 `error_code`로 거부하고 원 제안은 그대로 둔다.
- 테스트: `TestProposalRevise_OtherUser404`, `_ItemIDsMustStillBeOwned`, `_ConditionalVoidSingleRow`, `_RaceWithApproveOnlyOneWins`, `_IssueCapAcrossChain`, `_ExpiryCappedAtRoot24h410`, `_InheritsActorJobTaintScope`, `_RerunsAllCreationValidations`, `_EmptyBodyReissuesSamePayload`, `_BodyOnlyItemIDsElse400`, `_RootIDServerOnly`, `_NotFoundBodyIdenticalForMissingAndOtherUser`, `_DeletedItemReturnsItemGone`.

### 3.2 `proactive.propose_followup` [Sec-M2]
- **scheduler 실행과 tainted 스레드에서 `proactive.write`를 하드 거부**(선톡이 선톡을 만드는 자기 루프 차단). `scope_denied` / `tainted`.
- `thread_id`는 모델 인자가 아니라 **서버가 현재 스레드로 고정**한다.
- `when.at`은 미래이고 **현재로부터 30일 이내**(아니면 `invalid`).
- 사용자별 활성 follow-up(`enabled AND deleted_at IS NULL`) **최대 10개**(코드 상수, 초과 시 `cap_exceeded`). 개수는 `SELECT … FROM users WHERE id=$1 FOR UPDATE`로 사용자 행을 잠근 트랜잭션 안에서 세고, **approve 때 같은 방식으로 다시 확인**한다(동시 제안·승인으로 10개 초과 불가). 청사진: 필요하면 서버 설정값으로 옮기되 낮추기만 MINOR, 올리기는 리뷰.
- `template_id`는 follow-up 허용 목록에 있는 것만. 허용 여부(`allow_followup: true`)는 **배포 시 읽기 전용 템플릿 yaml**에만 있고 API·모델·설정으로 바꿀 수 없다.
- 테스트: `TestProposeFollowup_DeniedInSchedulerRun`, `_DeniedInTaintedThread`, `_ThreadIDPinnedToCurrent`, `_WhenFutureWithin30Days`, `_ActiveCap10`, `_CapConcurrentProposeApprove`, `_CapRecheckedAtApprove`, `_TemplateAllowlistNotMutable`.

### 3.2.1 job 스레드 자동 생성 (Q5)
- 스레드는 **job 소유자의 `user_id`로**, 실행과 **같은 트랜잭션**에서 만들고 `scheduled_jobs.thread_id`에 저장한다(실패 시 함께 롤백).
- 새 스레드는 job 실행의 taint를 물려받는다(tainted 실행이 만든 스레드는 tainted).
- 테스트: `TestJobThread_CreatedWithOwnerInSameTx`, `TestJobThread_InheritsTaint`, `TestJobThread_ReusedOnLaterRuns`.

### 3.3 상태 코드 ↔ `error_code`

| HTTP | `error_code` | 상황 |
|---|---|---|
| 400 | `invalid` | 본문·형식·범위 오류, 허용 외 필드 |
| 403 | `scope_denied` / `tainted` / `kill_switch` | 스코프·taint·킬스위치 거부 |
| 404 | `not_found` | 없음, 다른 사용자 소유(본문 동일), push 비활성 시 push 엔드포인트 |
| 409 | `item_gone` | revise 대상 항목이 그사이 삭제됨 |
| 409 | `conflict` | 제안이 이미 approve·void됨 (approve·revise 공통) |
| 410 | `expired` | root + 24h 경과 |
| 422 | `secret_detected` | 시크릿 필터 |
| 422 | `cap_exceeded` | 토큰 재발급 3회 상한, follow-up 활성 10개(제안·approve 때 모두) |
| 422 | `payload_mismatch` | approve의 `payload_hash`가 제안과 다름 (MINOR, 새 코드) |
| 500 | `internal_error` | 메모리 쓰기 중 감사 기록 실패 → 쓰기 거부(fail-closed, 파일 변경 없음) |
| 503 | `memory_read_only` | 메모리 파일 손상·복구 중으로 읽기 전용(memory.md §7) (MINOR, 새 코드) |

SSE 거부 카드는 같은 `error_code`만 쓴다(HTTP 코드 없음). TP §10.3 approve 응답 코드도 이 이름을 쓴다(구 `action_already_decided`·`action_expired`·`not_owner`는 쓰지 않음). 테스트 `TestErrorCodes_StatusMapping`, `TestApprove_PayloadMismatch422`, `TestApprove_NonOwner404SameBody`.

### 3.4 기타 [Sec-L]
- **SSE에는 토큰·토큰 해시를 절대 넣지 않는다**(토큰은 사용자 세션 POST 응답으로만). SSE는 해당 세션 사용자에게만 전송. 테스트 `TestSSEConfirmation_NoTokenOrHash`, `TestSSEConfirmation_OnlyToSessionUser`.
- `POST /v1/threads/{id}/read`의 다른 사용자 스레드 → 404. 테스트 `TestThreadRead_OtherUser404`.
- 설정: `TestSettingsPatch_StrictDecodeRejectsNullDupDeep`, `TestSettingsPatch_NoMapMerge`, `TestSettingsAudit_SecurityKeysBeforeAfter`, `TestSettings_KillSwitchWins`.
- 거부 카드: `TestConfirmationDenied_FixedErrorCodeNoText`.

### 3.5 추가 엔드포인트 (모두 MINOR)

#### 3.5.1 `GET /v1/actions` — 대기 중 제안 목록
- 응답 `{data:[…]}`: 각 항목은 confirmation 객체에서 **토큰 관련 필드 전부를 뺀 것**(`confirm_token`, `token_sha256`, `token_expires_at` 없음; `payload_hash`는 포함) + `thread_id` + `message_id`(nullable, scheduler 제안처럼 메시지가 없으면 `null`). 세션 사용자의 `status='pending'` 제안만.
- 토큰이 필요하면 `POST /v1/actions/{id}/reissue`로 받는다(§3.5.4). 만료된 제안을 다시 요청할 때만 `revise`(§3.1).
- 테스트: `TestActionsList_NoTokenRelatedKeys`(응답 JSON 전체에서 `token`이 들어간 키가 하나도 없음을 검사), `TestActionsList_IncludesPayloadHash`, `TestThreadsUnread_OnlyOwnThreads`, `TestActionsList_OnlySessionUser`.

#### 3.5.2 미읽음 — `/v1/notifications` 없음
- 별도 알림 리소스를 만들지 않고 `GET /v1/threads?unread=true`(기존 목록 + 필터, 각 Thread의 `unread_count` > 0만).
- Web Push payload는 그대로 `{v, type, nid}` ≤256B이며 **`nid` = `message_id`**. 클라이언트는 받은 뒤 API로 본문을 가져온다(push에 본문 없음). `nid`로 메시지를 조회할 때도 **소유자 확인**(아니면 404). ID(ULID)는 `crypto/rand`로 만들고 단조 증가(monotonic) 모드를 쓰지 않는다(추측 방지).
- 테스트: `TestThreadsList_UnreadFilter`, `TestMessageByNid_OwnerCheck404`, `TestULID_CryptoRandNonMonotonic`, `TestPushPayload_NidIsMessageIDUnder256B`.

#### 3.5.3 Push 구독
- `GET /v1/push/vapid-public-key` → `{key, key_id}`.
- `POST /v1/push/subscriptions` `{endpoint, keys:{p256dh, auth}, vapid_key_id}` → 201. `vapid_key_id`가 현재 키와 다르면 400 `invalid`.
- `DELETE /v1/push/subscriptions` `{endpoint}` → **항상 204**(세션 사용자 구독만 삭제, 없거나 다른 사용자 것이어도 204).
- 구독 검증(서버): `endpoint`는 `https`, 포트 443, 호스트가 정확히 `web.push.apple.com`(하위 도메인·다른 호스트 400). **userinfo·쿼리·프래그먼트가 있으면 400**, 길이 상한 **1024바이트**(코드 상수, 청사진: 서버 설정으로 옮기되 낮추기만 MINOR). `p256dh` 디코딩 65바이트이고 **P-256 곡선 위의 점**(비압축 `0x04`, 곡선 검증 실패 400), `auth` 16바이트.
- 결정 (1) 사용자별 구독 **최대 5개**(코드 상수, 초과 422 `cap_exceeded`). 상한 검사는 **같은 트랜잭션 안에서, 다른 사용자 endpoint인지 판정하기 전에** 한다. 청사진: 필요하면 서버 설정으로 옮기되 낮추기만 MINOR.
- 결정 (2) 다른 사용자가 이미 등록한 `endpoint`를 등록해도 **소유권은 옮겨지지 않고** 존재 여부도 드러나지 않는다(같은 **201**, 기존 행 유지, 내 구독 행은 만들지 않음). 감사에는 **endpoint 해시만** 남기고(원문 endpoint 없음) **ops 알림**을 보낸다.
  - 알려진 한계: 여러 계정이 한 브라우저를 같이 쓰면 두 번째 계정은 201을 받지만 push를 받지 못해 오해가 생길 수 있다. 청사진: 필요하면 구독 상태 조회를 MINOR로 추가.
- 설정을 읽지 못하면 push 비활성으로 보고 **404(fail-closed)**.
- push가 꺼져 있으면(설정·킬스위치) **세 엔드포인트 모두 404** `not_found`. CSRF·Origin 동일.
- 테스트: `TestPushVapidKey_Returns`, `TestPushSubscribe_201`, `TestPushSubscribe_EndpointHostPortScheme`, `TestPushSubscribe_EndpointRejectsUserinfoQueryFragment`, `TestPushSubscribe_EndpointLengthCap`, `TestPushSubscribe_KeyLengths`, `TestPushSubscribe_P256dhOnCurve`, `TestPushSubscribe_Cap5`, `TestPushSubscribe_CapCheckedBeforeOwnershipSameTx`, `TestPushSubscribe_OtherUserEndpointNoTransferNoLeak`, `TestPushSubscribe_OtherUserEndpointAuditHashOnlyOpsAlert`, `TestPushUnsubscribe_Always204`, `TestPush_DisabledAll404`, `TestPush_SettingsReadFailure404`.

#### 3.5.4 토큰 발급 — reissue·revise 응답
- 토큰은 세션 POST 응답에만 담기고 **SSE에는 절대 싣지 않는다**(§3.4).
- 결정 (3) **첫 토큰도 `POST /v1/actions/{id}/reissue`로 받으며, 이 첫 발급이 3회 상한에 포함**된다(TP §10.3).
  - reissue 검사: CSRF, `Origin` 정확 일치, 소유자 확인(아니면 404, §3.7), `status='pending'` 확인.
  - 검사 순서: CSRF·`Origin` → **소유자 확인을 먼저** → `status` 확인. 제안이 없거나 남의 것이면 상태와 무관하게 **항상 404**(§3.7 동일 응답)이고 **409는 절대 반환하지 않는다**(존재 여부 노출 방지).
  - **409 `conflict`는 소유자 본인의 non-pending 제안에만** 반환한다.
  - push endpoint 길이 상한은 **1024바이트로 확정**(§3.5.3).
  - 발급 카운터 증가와 이전 토큰 무효화는 **한 트랜잭션에서 원자적으로** 처리한다(조건부 1행 UPDATE).
  - UI 권장: 카드를 그릴 때가 아니라 **승인 버튼을 누를 때 reissue → approve를 이어서** 호출한다. 카드 표시 시점에 받으면 TTL(120~300초)이 지나고, 여러 탭에서 열면 3회 상한을 금방 다 쓴다.
- `POST /v1/proposals/{id}/revise` 응답 = **새 confirmation 객체 전체(토큰 포함)**(§3.1).
- 테스트: `TestActionReissue_FirstIssueCountsTowardCap3`, `TestActionReissue_CSRFAndExactOrigin`, `TestActionReissue_OtherUser404`, `TestActionReissue_OtherUserNonPending404`, `TestActionReissue_NonPending409`(소유자 본인만), `TestActionReissue_CounterAndVoidAtomic`, `TestProposalRevise_ResponseHasTokenSSEHasNone`.

### 3.6 CORS·캐시·로그 [Sec-Med CORS, Sec-L]
- **선행 조건**: main에 아직 없는 **CSRF 보호(보안 Med)가 먼저 들어가야** 토큰 발급·`/v1/actions`·push 엔드포인트를 구현·배포할 수 있다.
- `cors_origins`에 `*`가 있으면 **게이트웨이가 기동을 거부**한다. 테스트 `TestConfig_CORSWildcardRefusesStartup`.
- 토큰 발급 엔드포인트(`revise`, `reissue`, approve 경로의 토큰 발급)와 `GET /v1/actions`는 **허용 origin이어도 CORS 허용 헤더를 절대 내지 않는다**(`Access-Control-Allow-*` 없음, preflight 포함). 테스트 `TestCORS_TokenEndpointsAndActionsNoAllowHeaders`.
- `revise`·`reissue`·`/v1/actions` 응답은 `Cache-Control: no-store`. 테스트 `TestTokenEndpoints_CacheNoStore`.
- 이 엔드포인트들의 요청·응답 본문은 로그에 남기지 않는다. `tool_audit`에는 토큰·토큰 해시가 절대 없다(`action_id`·`payload_hash`만). 테스트 `TestTokenEndpoints_BodiesNotLogged`, `TestAudit_NoTokenOrTokenHash`.
- 422 `payload_mismatch` 본문은 `{"error_code":"payload_mismatch"}`뿐(기대 해시 없음). 테스트 `TestApprove_PayloadMismatchBodyCodeOnly`.

### 3.7 404 동일성 [Sec-L5]
- 없는 리소스와 다른 사용자 리소스는 **같은 쿼리 경로**(`WHERE id=$1 AND user_id=$session`, 0행이면 404)로 처리해 분기·시간 차이를 만들지 않는다.
- 404 응답은 **본문과 헤더가 동일**하다(상태, `Content-Type`, `Content-Length`, `Cache-Control` 등). 요청마다 다른 `request_id`만 비교에서 뺀다.
- 테스트: `TestNotFound_BodyBytesIdenticalMissingVsOtherUser`, `TestNotFound_HeadersIdenticalMissingVsOtherUser`, `TestNotFound_SameQueryPath`.

| ID | 지적 | 반영 |
|---|---|---|
| Sec-M1 | revise 규칙 | §3.1 (404, 소유 재확인, 조건부 1행 void, 체인 상한, root+24h, 상속, 필터 재실행) |
| Sec-M2 | follow-up 툴 | §3.2 (scheduler·taint 거부, thread 고정, 30일, 활성 10개, 템플릿 허용 목록) |
| Sec-L | settings strict decode·감사·킬스위치, SSE 토큰 금지, read 404 | §2, §3.4 |
| Sec-Med CORS | `*` 기동 거부, 토큰 발급·actions CORS 헤더 없음, CSRF 선행 | §3.6 |
| Sec-L2 | no-store, 본문 무로그, 감사 토큰 없음, 422 본문 code만, 404 바이트 비교 | §3.6 |
| Sec-L3 | 토큰 전달 경로 = 세션 POST 응답만(SSE·TP에서 제거, UI PR #1 수정 필요), actions에 토큰 키 없음 | §3.5.1, §3.5.4, TP §10.3 |
| Sec-L4 | push 구독 검증(호스트·키 길이·상한 5·소유권·DELETE 204·fail-closed), nid 소유 확인, ULID crypto/rand, unread 본인 스레드 | §3.5.2, §3.5.3 |
| Sec-L5 | reissue 경로 정정(§3.5.1), 결정 (1)~(3) 조건, push endpoint userinfo·쿼리·프래그먼트 거부·길이 상한·p256dh 곡선 검증, 404 헤더 동일·같은 쿼리 경로 | §3.5.1, §3.5.3, §3.5.4, §3.7 |
| 디자이너 | 위험 스타일 send·delete만, job 스레드 첫 실행 생성, 후보 `updated_at`, 고정 error_code, 빈 본문 revise, `template_kind` | §1 Q4·Q5·Q9·Q11·Q13·Q14 |

### 3.8 분리 생성·재연결 (detached generation, MINOR, gateway 3.1.0)
iPhone PWA가 백그라운드로 가면 SSE가 끊긴다. 저장되는 채팅(`thread_id` 있음)은 이제 **클라이언트 연결과 무관하게 끝까지 생성·저장**한다.

- **생성 수명**: 요청 컨텍스트와 분리된 컨텍스트(`context.WithoutCancel` + 자체 타임아웃 15분)로 실행. 큐·동시성 한도(`max_concurrency`/`max_queue`)는 그대로. 끊긴 클라이언트는 구독만 끝난다. `thread_id` 없는 채팅은 기존대로 연결이 끊기면 취소된다.
- **행 선생성**: 생성을 시작할 때 assistant 메시지 행을 `status:"generating"`으로 만든다(재생성은 같은 id를 `generating`으로 표시하고 끝날 때 덮어씀). 끝나면 `complete` 또는 `failed`.
- **status 값 (API)**: `"generating" | "complete" | "failed"`. `failed`면 `error_code`(문자열)가 붙는다: `upstream_error`, `upstream_timeout`, `timeout`(15분 초과), `queue_full`/`queue_timeout` 등 게이트웨이 에러 코드, `gateway_restart`(재시작 시 남은 `generating` 행을 기동 시 일괄 `failed` 처리), `internal_error`. 기존 메시지는 `complete`; 레거시 `incomplete` 행은 `failed` + `error_code`=기존 `metadata.reason`으로 보인다.
- **스트림 첫 이벤트**: `POST /v1/chat/completions`(stream, thread 저장) 응답의 첫 SSE 이벤트는
  ```
  id: 1
  event: eve.meta
  data: {"thread_id":"01J…","message_id":"01J…"}
  ```
  헤더 `X-Message-Id`도 같은 값. 이후 **모든 이벤트에 `id: <seq>`**(1부터 단조 증가)가 붙는다. 그 뒤 순서는 기존과 같다(persist 청크 → 콘텐츠 청크/툴 이벤트 → sources → `chat.completion.persisted` → `[DONE]`). `persisted.status`도 `complete|failed`.
- **어드미션 전 에러 = HTTP 상태 (3.0.0과 동일)**: 요청 검증(400/404/503), 큐 포화 `429 queue_full`, 큐 대기 초과 `queue_timeout` 등 **큐 슬롯을 얻기 전** 실패는 기존과 같은 HTTP 상태·본문으로 오고 **assistant 행을 만들지 않는다**(`generating` 행은 어드미션 후에만 생성). 웹검색 경로도 동일(3.0.0에서는 스트림 안 에러였음 → 이제 HTTP 429).
  추가 HTTP 에러: `429 too_many_generations`(사용자당 동시 생성 2개 초과), `429 server_busy`(허브 전체 한도), `409 generation_in_progress`(이미 `generating`인 메시지를 동시에 재생성 — 조건부 UPDATE로 한 요청만 성공). 모든 429에는 `Retry-After` 헤더.
- **어드미션 후 에러 = 스트림 안**: 업스트림 연결 실패·중간 에러·타임아웃은 HTTP 200 SSE 안에서 에러 이벤트(`{"error":{"code","message",…,"retry_after"?}}`) → persisted(`failed`) → `[DONE]`. 원래 상태·코드는 서버 로그와 `eve_gen_stream_errors_total{code,status}`에 남는다.
- **취소**: `POST /v1/threads/{thread_id}/messages/{message_id}/cancel` (CSRF 헤더 필요, 본문 없음) → 항상 **204**(멱등: 이미 끝난 메시지도 204, 상태 변화 없음). 진행 중이면 생성을 멈추고 `failed` + `error_code:"cancelled"`. 없는/남의 메시지 → 같은 404.
- **자동 취소**: 구독자(원 스트림 + 재연결)가 하나도 없는 상태가 5분(`generation.idle_cancel_s`, env `GW_GEN_IDLE_CANCEL_S`) 이어지면 `failed` + `no_subscriber`. 백그라운드 PWA는 5분 안에 재연결해야 이어서 받는다.
- **한도**: 런당 이벤트 로그 2MB/10k 이벤트 — 넘으면 오래된 이벤트부터 버리고, 버린 구간 이후를 요청한 재연결은 `eve.snapshot`(현재까지의 content, `status`, `last_event_id`)로 시작한 뒤 이어서 실시간 이벤트. 부분 응답(content+reasoning) 64KB 초과 → `failed` + `too_large`. 허브 전체 런 64개/128MB, 넘으면 끝난 런부터 퇴출(퇴출된 런 재연결 = DB 스냅숏); 진행 중 런만으로 바이트 한도에 차면 새 생성은 `429 server_busy`. 기동 시 `hub_max_bytes >= hub_max_runs × run_max_bytes`를 검사해 어긋나면 시작하지 않는다. 메시지당 구독자 4개, 5번째 → `429 too_many_subscribers`. 구독자 쓰기 데드라인 30초(느린 클라이언트는 끊김 → 재연결).
- **새 `error_code`**: `cancelled`, `no_subscriber`, `too_large`, `timeout`, `gateway_restart`, `internal_error` + 기존 게이트웨이 코드.
- **캐시**: 스트림·재연결 응답 모두 `Cache-Control: no-store`.
- **기동 스윕은 단일 인스턴스 전용**: 기동 시 남은 `generating` 행을 모두 `failed`(`gateway_restart`)로 바꾼다. 같은 DB를 쓰는 게이트웨이를 2개 이상 띄우면 서로의 진행 중 생성을 실패 처리하므로 지원하지 않는다.
- **메트릭**: `eve_gen_runs`, `eve_gen_bytes`, `eve_gen_evictions_total`, `eve_gen_overflows_total{kind}`, `eve_gen_rejected_total{reason}`, `eve_gen_cancelled_total{code}`, `eve_gen_stream_errors_total{code,status}`.
- **목록**: `GET /v1/threads/{id}/messages`는 `status`(+`error_code`)를 주고, `generating`인 메시지는 **지금까지의 부분 content/reasoning**을 준다(스트림으로 보낸 것과 같은 delta를 같은 방식으로 누적한 값 = 저장될 값과 동일).
- **재연결**: `GET /v1/threads/{thread_id}/messages/{message_id}/stream` + `Last-Event-ID: <seq>` 헤더(또는 `?after=<seq>`, 0 이상 정수, 아니면 400). seq 이후 이벤트를 재생하고 진행 중이면 실시간으로 이어서 보낸 뒤 `[DONE]`으로 끝난다. 생성 완료 후 10분간은 메모리에서 같은 방식으로 재생된다. 그 이후(또는 게이트웨이 재시작 후)는 `event: eve.snapshot` 한 개(data = 목록과 같은 Message 객체) + `data: [DONE]`. 15초마다 `: keepalive`.
- **권한**: 없는 메시지·남의 메시지·다른 스레드의 메시지·assistant가 아닌 메시지 → 모두 같은 404(§3.7, user 범위 조회). GET이라 CSRF 헤더 불필요, CORS 허용 없음(같은 출처만).
- **클라이언트 권장 흐름**: `eve.meta`에서 `message_id`와 마지막 `id`를 저장 → `visibilitychange`로 돌아오거나 스트림이 끊기면 `.../stream`에 `Last-Event-ID`로 재연결(또는 목록을 다시 불러 `status`가 `generating`이면 재연결, `complete`면 그대로 표시).
- **마이그레이션**: `00006_message_generating.sql` — `messages.status` CHECK에 `generating`,`failed` 추가(확장만). Down은 두 값을 `incomplete`로 되돌린다.

### 3.9 메모리 API 응답 스키마 (memory.md §4, MINOR)

구현 기준은 gateway 4.1.0(PR #25)이며, 후속 PR에서 approve·설정이 더해진다. 모든 경로는 **최종 활성화 = `GW_MEMORY_ENABLED` AND 사용자 `memory.enabled`**일 때만 동작한다.
- 서버 킬스위치가 꺼져 있으면 `/v1/memory*`와 메모리 approve는 라우트가 없고, 기존과 바이트 단위로 같은 404를 낸다(§3.7). 기존 응답은 바뀌지 않는다.
- 서버는 켜져 있고 사용자 `memory.enabled=false`이면: 조회·편집·삭제·export는 그대로 동작한다(사용자가 지울 수 있어야 함). 주입과 `memory.propose`는 하지 않고, 메모리 제안 approve는 403 `kill_switch`.
- 모든 응답에 `Cache-Control: no-store`가 붙고 CORS 허용 헤더는 없다(§3.6). 본문은 로그에 남기지 않는다.
- 쓰기는 CSRF + 정확한 Origin 검사가 필요하다. 사용자는 인증 컨텍스트로만 정한다(경로·본문에 user 필드 없음).

#### 3.9.1 공통 객체

```json
// memory item
{ "id": "01JB...", "kind": "preference", "text": "아침 브리핑은 짧게", "pinned": false,
  "confidence": "user", "source": {"thread_id": "01J...", "message_id": "01J..."},
  "created_at": "2026-10-27T05:00:00Z", "updated_at": "2026-10-27T05:00:00Z", "expires_at": null }
```

| 필드 | 타입 | 규칙 |
|---|---|---|
| `id` | string (ULID) | 서버 생성 |
| `kind` | `profile` \| `preference` \| `fact` \| `task` | 그 밖의 값은 400 `invalid` |
| `text` | string | 저장된 정규화 텍스트(NFKC, 제어·zero-width 제거), 최대 300자 |
| `pinned` | bool | `profile`은 기본 true |
| `confidence` | `user` \| `confirmed` | UI 직접 입력 = `user`, 카드 승인 = `confirmed` |
| `source` | object, 생략 가능 | 카드 승인으로 만든 항목에만 있다 |
| `expires_at` | RFC 3339 \| null | `task`는 기본 30일 |

tombstone은 API로 노출하지 않는다.

#### 3.9.2 조회 — `GET /v1/memory`, `GET /v1/memory/export`

```json
200
{ "object": "memory", "updated_at": "2026-10-27T05:00:00Z", "items": [ <memory item>, ... ] }
```
- 파일이 없으면 `updated_at: null`, `items: []`.
- export는 본문이 같고 `Content-Disposition: attachment; filename="memory.json"`이 붙는다. `tool_audit`에 `memory_export`로 기록한다(본문 없음).
- 만료된 항목도 목록에 보인다(UI가 만료 표시). 주입에서만 빠진다.

#### 3.9.3 직접 편집 — UI 전용

| 요청 | 본문 (strict, 모르는 필드 400) | 성공 |
|---|---|---|
| `POST /v1/memory/items` | `{kind, text, pinned?, expires_at?}` | 201 + memory item |
| `PATCH /v1/memory/items/{id}` | `{text?, pinned?, expires_at?\|null, if_updated_at?}` | 200 + memory item |
| `DELETE /v1/memory/items/{id}` | 없음 | 204, 본문 없음 |
| `DELETE /v1/memory` | 없음 | 204, 본문 없음 (파일 삭제) |

- `if_updated_at`이 현재 값과 다르면 409 `conflict`.
- 감사에는 item id와 텍스트의 HMAC만 남는다(`memory_add`·`memory_edit`·`memory_delete`·`memory_delete_all`, `via=ui`).

#### 3.9.4 제안 카드 — `memory.propose` (SSE confirmation)

모델의 `memory.propose`는 바로 쓰지 않는다. 확인 카드(§3.5, TP §10.3)를 만든다.
- 모델에게는 `{"status":"pending_confirmation","action_id":"..."}`만 돌려준다.
- **taint**: 스레드에 웹·툴 출력이 들어온 뒤의 제안은 카드를 만들지 않는다. SSE 거부 카드는 `{"status":"denied","error_code":"tainted"}`이다. scheduler 제안은 `scope_denied`이다.

```json
{ "object": "chat.completion.confirmation", "action_id": "01JB...", "thread_id": "01J...", "message_id": "01J...",
  "tool": "memory.propose", "tool_version": "1", "scopes": ["memory.write"],
  "payload_hash": "sha256:...", "provenance": {"actor": "user", "job_id": null, "job_name": null},
  "tainted": false, "status": "pending",
  "expires_at": "2026-10-28T05:00:00Z", "created_at": "2026-10-27T05:00:00Z",
  "summary": "메모리에 추가",
  "payload_view": { "op": "add", "items": [{"text": "아침 브리핑은 짧게", "kind": "preference"}], "ambiguous": false } }
```

- `payload_view.op`는 `add` 또는 `delete`이다(Q11).
  - add 항목에는 `id`가 없고, 정규화 후 실제로 저장될 텍스트를 보여준다.
  - delete 항목에는 `id`, `text`, `kind`, `updated_at`이 있다.
- `"잊어줘"` = delete 카드. 매칭이 여럿이거나 모호하면 `ambiguous: true`로 후보를 체크박스로 보여준다. 일부만 지우려면 revise(§3.9.6)를 쓴다.
  - 매칭이 없으면 카드를 만들지 않고, 모델이 답한다(Q16).
  - **카드 없이 지우는 경로는 없다.**
- 스타일: delete는 위험(danger) 스타일, add는 기본 스타일이다(Q13).
- 시크릿 필터에 걸리면 SSE 거부 카드 `{"status":"denied","error_code":"secret_detected"}`, 민감 범주(건강·성적 지향·종교·정치·범죄·노조·인종·생체/유전·금융·신분증)에 걸리면 `{"status":"denied","error_code":"sensitive_category"}`만 보낸다. 매칭 내용·세부 범주는 보내지 않는다. 직접 편집 API에서는 각각 422 `secret_detected` / 422 `sensitive_category`이다(MINOR, 새 코드).
- 민감 범주 정책: 사용자 본인에 대한 민감 진술은 **거부**한다(P1). 제3자(가족·지인·동료 등)에 대한 민감 진술은 **항상 거부**하며 앞으로도 허용 경로를 두지 않는다.

#### 3.9.5 승인 — `POST /v1/actions/{id}/approve`

토큰은 `POST /v1/actions/{id}/reissue`로 받는다(§3.5.4).

```json
요청 { "confirm_token": "<reissue 응답의 토큰>", "payload_hash": "sha256:..." }
200  { "status": "executed", "action_id": "01JB...", "result": { "op": "add", "item_ids": ["01JC..."] } }
```

- 검사 순서: 본문 400 `invalid`(`confirm_token` 누락·빈 값 포함) → 404 `not_found`(다른 사용자도 본문 동일) → 409 `conflict`(이미 approve·void) → 410 `expired`(제안 또는 토큰 만료. 토큰이 틀리거나 재발급으로 대체된 경우도 같은 410 — 토큰 존재 여부를 구분해 노출하지 않는다) → 422 `payload_mismatch` → 403 `kill_switch`(사용자 `memory.enabled=false`) / `tainted` / `scope_denied` → 실행.
- 실행 시점에 다시 검사한다.
  - add 텍스트는 정규화와 시크릿 필터를 다시 거친다. 걸리면 422 `secret_detected`, 개수·크기 상한은 422 `cap_exceeded`이다.
  - delete 대상이 그사이 사라졌으면 409 `item_gone`이다.
- `pending→approved`는 조건부 UPDATE 1행이다. 같은 approve를 다시 보내면 409 `conflict`이다.
- 감사 기록이 실패하면 500 `internal_error`(파일 변경 없음), 읽기 전용이면 503 `memory_read_only`이다.

#### 3.9.6 부분 선택 — `POST /v1/proposals/{id}/revise`

§3.1·§3.5.4와 같다. `{ "item_ids": ["01JB...", ...] }` → 새 confirmation 객체 전체(토큰 포함).
- 메모리 제안은 successor 페이로드에 생성 검사를 다시 적용한다.
  - 대상 항목이 없어졌으면 409 `item_gone`이다.
  - taint는 `tainted`, scheduler는 `scope_denied`, 시크릿은 `secret_detected`로 거부한다.

#### 3.9.7 사용자 설정 — `memory.enabled`

§2의 `GET /v1/settings` / `PATCH /v1/settings`를 쓴다. 메모리 전용 설정 엔드포인트는 따로 만들지 않는다.

```json
GET   → { ..., "memory": { "enabled": true }, "system": { ..., "memory_available": true } }
PATCH { "memory": { "enabled": false } } → 200 + 전체 설정 객체
```

- **5.0.0부터** 사용자 `memory.enabled`의 기본값은 **on(true)**이다(4.x는 off). 저장 위치는 `users.settings` JSONB(마이그레이션 00007, 기본 `'{}'`)이고, 키가 없으면 **on으로 읽는다(opt-out)**. 사용자는 설정에서 언제든 끌 수 있고, 끈 뒤의 동작은 아래와 같으며 바뀌지 않는다.
- **저장 경로는 두 가지다.** (1) 모델 제안(`memory.propose`)은 항목마다 승인 카드를 사용자가 승인해야 저장된다(자동 승인 없음). (2) UI 직접 추가(`POST /v1/memory/items`)는 브라우저 세션에서 사용자 본인이 명시적으로 하는 행동이라 카드가 없다. 그 밖의 저장 경로는 없다.
- **API 키(Bearer) 접근**: API 키 가상 사용자는 `memory.enabled`가 off로 고정되며 바꿀 수 없다. `/v1/memory*` 라우트는 모두 브라우저 세션 전용이고 Bearer 요청은 403 `scope_denied`이다.
- 5.0.0 go-live 전제 조건(memory.md §14.1): 민감 필터 QA 홀드아웃 통과와 보안 최종 승인(#53, 미충족), #33·#35 머지, #42 fail-closed, #23 cutover 완료(신원 위조 HIGH 해결), 첫 사용 안내 시트 구현(`ui.memory_intro_seen=false`면 `memory.propose` 미노출·주입 없음), 서버 설정 allowlist에 `ui.memory_intro_seen` 포함, 감사 HMAC 키 준비(기본 on에서 키가 없거나 틀리면 게이트웨이 전체 기동 거부).
- 기존 사용자: 업그레이드 전에 키가 없던 사용자(명시적으로 끄지 않은 사용자 전원)는 5.0.0 업그레이드 직후 메모리가 켜진다. 명시적으로 `false`를 저장한 사용자는 그대로 off다.
- 첫 사용 안내(UI): 메모리가 켜진 사용자에게 **한 번만** 안내 시트를 보여 준다. 안내에는 메모리가 켜졌다는 사실, 모델 제안은 승인 카드로만 저장된다는 점, 설정에서 끄는 방법을 담는다. 표시 여부는 `users.settings`의 `ui.memory_intro_seen`(bool, 기본 false, PATCH 가능)에 기록해 여러 기기에서 한 번만 보이게 한다. 서버가 이 키를 지원하기 전까지 UI는 localStorage만 쓰고 400은 무시한다. 키 추가는 MINOR이며 5.0.0에 함께 넣는다.
- `system.memory_available`(bool, 읽기 전용)은 서버 킬스위치다. 서버 `GW_MEMORY_ENABLED=false`이면(5.0.0부터 서버 기본값은 **true**, 4.x는 false) `false`이고, 이때 `memory` 블록은 응답에 나오지 않으며 `memory` 키를 PATCH하면 400 `invalid`이다.
- 실제 적용 = `system.memory_available` AND `memory.enabled`.
- 사용자 `memory.enabled=false`(서버는 on)일 때:
  - 허용: `GET /v1/memory`, `GET /v1/memory/export`, `PATCH`·`DELETE /v1/memory/items/{id}`, `DELETE /v1/memory`(읽기·편집·삭제·내보내기).
  - 거부(HTTP 403 `kill_switch`): `POST /v1/memory/items`, `POST /v1/actions/{id}/approve`, 메모리 카드의 `POST /v1/proposals/{id}/revise`.
  - 주입 없음. `memory.propose` 도구 정의를 모델에 보내지 않는다. 그래도 모델이 `memory.propose`를 호출하면 도구 결과로 `{"status":"denied","error_code":"kill_switch"}`를 돌려주고 아무것도 저장하지 않으며 카드도 만들지 않는다. 메모리 거부 사유는 도구 결과·SSE 카드·HTTP 모두 `error_code` 키 하나만 쓴다(별칭 없음, 코드 변경은 #38 머지 후 후속 PR).
- 서버 `GW_MEMORY_ENABLED=false`이면 위 라우트가 모두 없고(404 `{"detail":"Not Found"}`), 도구 정의·주입도 없으며 API 응답은 메모리 도입 전과 같다.
- 키는 네임스페이스로 둔다(`memory.*`, `proactive.*`). PATCH는 지정한 키만 해당 네임스페이스에 병합하고, 모르는 기존 키는 보존한다.
- 청사진: 앞으로의 사용자 설정은 이 컬럼에 네임스페이스 키로 추가한다. 키가 많아지거나 키별 이력·인덱스가 필요하면 별도 테이블로 나눈다.
- 변경은 `tool_audit` security 이벤트로 before/after 불리언만 남긴다.
- `system.*`은 읽기 전용이다. PATCH로 보내면 400 `invalid`이다. (P1 설정 응답에는 `system.memory_read_only`가 없다. 읽기 전용 상태는 쓰기 시 503 `memory_read_only`로만 드러난다. 노출이 필요해지면 MINOR로 추가한다.)
- SemVer: `system.memory_available`·`memory.enabled` 추가는 MINOR(4.x). **기본값을 on으로 바꾸는 것은 MAJOR(5.0.0)**다. 이유: 사용자가 opt-in하지 않아도 개인 데이터를 저장할 수 있게 되어 보안 경계가 바뀌고, 고정했던 기본 off 계약을 뒤집는다. 롤백·전제 조건은 memory.md §14.

#### 3.9.7a 보안 요건 (security Med, 고정)

- approve·revise·reissue는 **브라우저 세션 전용**이다(세션 provider 허용 목록 + CSRF·Origin 검사). Bearer(API 키)는 403 `scope_denied`이다. **자동 승인 경로는 없다**(모델·scheduler·API 키 어느 것도 승인할 수 없음).
- `confirm_token`:
  - (user, action_id, payload_hash)에 바인딩된다.
  - 1회용이다(승인 시 조건부 UPDATE로 소비).
  - TTL 120초. 만료·대체·불일치는 모두 410 `expired`.
  - 서버에는 해시만 저장한다(평문 저장·로그 없음). 비교는 상수 시간.
  - revise·reissue 시 이전 토큰은 무효가 된다.
- taint는 서버 상태로 결정하며 스레드에 고정(sticky)된다. 감사에는 출처 목록의 개수와 해시를 남긴다.
- 실행 실패 시 카드는 pending으로 되돌아간다(소비만 되고 실행되지 않는 상태 없음).
- 백업 제외(#42, fail-closed): 메모리가 켜진 상태로 시작할 때 메모리 디렉터리가 Time Machine 제외 목록(고정 경로 `tmutil`, 정확한 줄 일치)에 없거나 동기화 폴더(iCloud·Dropbox 등) 안이면 **시작을 거부**한다. 확인할 수 없는 환경에서만 `GW_MEMORY_ALLOW_UNVERIFIED_BACKUP=1`로 명시적으로 우회하며, 이때 시작 경고를 남긴다. 절차는 memory.md 런북 참고.

#### 3.9.8 메모리 오류 코드 요약

| HTTP | 본문 | 메모리 상황 |
|---|---|---|
| 400 | `{"error_code":"invalid"}` | strict decode 실패, kind·길이·형식 오류 |
| 403 | `{"error_code":"kill_switch"}` / `tainted` / `scope_denied` | 사용자 off에서 항목 추가·approve·revise, taint, scheduler·Bearer |
| 404 | `{"error_code":"not_found"}` | 항목·제안 없음, 다른 사용자 소유(본문 동일) |
| 404 | `{"detail":"Not Found"}` | 서버 메모리 off(`GW_MEMORY_ENABLED=false`): `/v1/memory*`·approve 라우트가 등록되지 않아 게이트웨이 기본 404 |
| 409 | `conflict` / `item_gone` | `if_updated_at` 불일치·이미 결정됨 / 삭제 대상 사라짐 |
| 410 | `expired` | 제안·토큰 만료, 틀리거나 대체된 토큰 |
| 422 | `secret_detected` / `sensitive_category` / `cap_exceeded` / `payload_mismatch` | 시크릿 / 민감 범주 / 200개·64KB·300자 / 해시 불일치 |
| 500 | `{"error":{"code":"internal_error",...}}` | 감사 쓰기 실패 등(fail-closed) |
| 503 | `{"error":{"code":"memory_read_only",...}}` | 파일 손상·권한 이상, 복구 전까지 쓰기 거부 |

- 404 본문 구분: `{"detail":"Not Found"}`는 **기능 꺼짐**(라우트 없음), `{"error_code":"not_found"}`는 **기능은 켜져 있고 대상이 없음**이다. UI는 전자를 "메모리 사용 불가", 후자를 "항목 없음"으로 보여준다.
- `PATCH /v1/memory/items/{id}` 오류 전체 목록:
  | HTTP | 본문 | 원인 |
  |---|---|---|
  | 400 | `invalid` | JSON 아님·객체 아님, 모르는 필드, 타입 오류(`text`·`pinned`·`expires_at`·`if_updated_at`), 빈 텍스트, 형식 오류, 메모리 블록 마커(`<user_memory>` 변형) 포함 |
  | 404 | `not_found` | 항목 없음(다른 사용자 항목 포함) |
  | 404 | `{"detail":"Not Found"}` | 서버 메모리 off |
  | 409 | `conflict` | `if_updated_at`이 현재 값과 다름 |
  | 422 | `secret_detected` | 새 텍스트가 시크릿 필터에 걸림 |
  | 422 | `sensitive_category` | 새 텍스트가 민감 범주에 걸림 |
  | 422 | `cap_exceeded` | 300자 초과, 파일 64KB 초과 |
  | 403 | CSRF 오류(기존 형식) | Origin/CSRF 불일치 |
  | 500 | `internal_error` | 감사 쓰기 실패(fail-closed, 파일 변경 없음) 등 |
  | 503 | `memory_read_only` | 파일 손상·권한 이상 |
- 500과 503은 기존 게이트웨이 오류 형식(`error` 객체)을 쓰고, `error_code` 단일 필드 형식이 아니다(현재 구현 그대로).
- UI는 `error.code`를 읽어 고정 문구로 보여준다.


## 4. 확장 메모 (청사진)
- **민감 범주 자기 진술 opt-in(청사진)**: 향후 사용자가 명시적으로 켜면 본인에 대한 민감 진술을 **경고 확인 카드**(위험 스타일, 범주 안내 문구)로 저장할 수 있게 하는 설정(`memory.allow_self_sensitive`, 기본 off)을 검토한다. 제3자 민감 진술은 이 설정과 무관하게 항상 거부한다. 도입 시 보안 리뷰 대상, MINOR.
- **job 스레드 표시(Q5)**: 목록에서 job이 만든 스레드를 구분해야 하면 Thread에 `job_id`를 노출(MINOR). P1은 메시지 `provenance`·`template_kind`로 충분.
- **매칭 없음 신호(Q16)**: 필요하면 SSE `tool_result`에 `status:"no_match"`를 노출(MINOR).
- **스레드별 메모리 끄기(Q18)**: `threads.settings` JSONB(`{"memory":false}`) 또는 `PATCH /v1/threads/{id} {"memory":false}`. 프롬프트 동작 변경이라 리뷰 대상.
- **`kind` 값 추가(Q4)**: `digest` 외 값은 MINOR, UI 기본 처리 규칙으로 하위 호환.
- **daily_cap 노출**: 예약 키, 1–3 낮추기만.
- **시간대**: `quiet_hours.tz` 예약.
