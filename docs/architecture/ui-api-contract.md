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
| Q8 | 설정 응답에 읽기 전용 `system` 블록: `proactive_available`(=`GW_PROACTIVE_ENABLED` && !`proactive_off`), `tools_available`(킬스위치 `all_off`가 아님), `memory_read_only`(Q17). PATCH로 `system`을 보내면 400. | MINOR |
| Q9 | 만료된 제안의 "다시 요청" = `POST /v1/proposals/{id}/revise`. `item_ids`가 있는 제안(메모리 삭제)은 **원래와 같은 `item_ids`**, `item_ids`가 없는 제안(메모리 add, 툴, `proactive.propose_followup`)은 **빈 본문 = 같은 payload 재발급**. 새 제안·새 `payload_hash`·새 토큰, 원 제안은 `voided`. 새 모델 턴 없음. 규칙은 §3.1. | MINOR |
| Q10 | `token_expires_at`은 토큰과 함께 **세션 POST 응답**(reissue·revise)에만 포함(`expires_at`과 별개, TP 두 만료 규칙). SSE confirmation에는 토큰 관련 필드 없음(§3.4). | MINOR |
| Q11 | `memory.propose` `payload_view`: `{op:"add"\|"delete", items:[{id?, text, kind, updated_at?}], ambiguous:bool}`. add는 `id` 없음·정규화 후 텍스트, delete(후보)는 저장된 `id`·`text`·`kind`·`updated_at`(최근 수정일 표시용). | MINOR |
| Q12 | follow-up 생성 툴 이름 `proactive.propose_followup`(TP `<domain>.<verb>` 규칙, 스코프 `proactive.write`, 확인 게이트 필수). `payload_view: {when:{at:"RFC3339"}, thread_id, display:"10/28 (화) 09:00", template_id}`. `display`는 서버가 Asia/Seoul로 만든 표시 문자열. 보안 규칙은 §3.2. | MINOR |
| Q13 | SSE confirmation에 툴의 선언 `scopes`(예: `["memory.write"]`)를 그대로 포함. **위험 스타일은 스코프 verb `send`와 삭제(`payload_view.op="delete"` 또는 `delete` 스코프)에만**, `write`는 기본(primary) 스타일. | MINOR |
| Q19 | 확인 카드 `payload_view.verb`: 고정 enum `add`\|`update`\|`send`\|`delete`\|`run`. **서버가 툴 매니페스트의 정적 `verb` 필드(배포 시 정의, TP §1.1)로만 채운다.** 모델 출력·툴 인자·사용자 입력으로는 바꿀 수 없고, 자유 텍스트 label은 없다. UI가 verb → 한국어 버튼 문구를 매핑하고, **모르는 값은 `run`으로 처리**(전방 호환). **위험 스타일은 verb가 아니라 Q13의 scopes 규칙** 그대로. enum 값 추가는 MINOR. 테스트: (1) 모든 매니페스트 툴이 유효한 `verb`를 선언(누락·무효 시 CI 실패), (2) 모델 출력이 `verb`를 바꾸는 경로 없음(모델 args에 `verb`/`payload_view`를 넣어도 카드 verb = 매니페스트 값). | MINOR |
| Q20 | `template_kind`는 **Message 최상위 필드**(`origin`·`job_id`·`kind`와 같은 레벨)다. `provenance` 안에 넣지 않는다(`provenance`는 `{actor, job_id, job_name}`만). 예: `{"id":"…","origin":"proactive","job_id":"…","kind":null,"template_kind":"briefing","read_at":null,"provenance":{"actor":"scheduler","job_id":"…","job_name":"아침 브리핑"}}` | — |
| Q14 | 서버 거부는 **카드 error 상태**: SSE confirmation이 `status:"denied"`와 고정 enum `error_code`만 보낸다: `secret_detected` \| `cap_exceeded` \| `tainted` \| `scope_denied` \| `expired` \| `not_found` \| `item_gone` \| `conflict` \| `payload_mismatch` \| `kill_switch` \| `invalid`(매핑 §3.3). **자유 텍스트 요약 없음**(본문·매칭 내용 노출 방지). UI가 코드별 고정 문구를 쓴다. 값 추가는 MINOR, 모르는 코드는 `invalid` 문구. 모델 답변은 별개로 이어짐. | MINOR |
| Q15 | `selected_ids` 없음. 부분 해제 = `POST /v1/proposals/{id}/revise {item_ids}`, `item_ids ⊆ 원 제안`(아니면 400 `invalid`). 새 제안·새 `payload_hash`·새 토큰, 원 제안 `voided`. approve 본문은 그대로(TP). 규칙은 §3.1. | MINOR |
| Q16 | 매칭 없음 = **카드 없음**, 모델이 답한다. 별도 신호 필드 없음(P1). UI의 "메모리 관리 열기" 링크는 붙이지 않거나 고정 도움말로. (청사진 §3) | — |
| Q17 | 메모리 읽기 전용(MEM S5-R6)은 `GET /v1/settings`의 `system.memory_read_only`. `GET /v1/memory`에는 추가 필드 없음. | MINOR |
| Q18 | 스레드별 "메모리 없이"는 **P1 범위 밖**. (청사진 §3) | — |

## 2. 설정 (`users.settings`)

```json
GET /v1/settings
{
  "proactive": { "enabled": false,
                 "quiet_hours": {"start": "22:00", "end": "08:00"},
                 "memory_read": false },
  "memory":    { "enabled": true },
  "system":    { "proactive_available": false, "tools_available": true, "memory_read_only": false }
}
```

| 키 | 타입·기본 | 규칙 |
|---|---|---|
| `proactive.enabled` | bool, false | 사용자 선톡 on/off. `system.proactive_available=false`면 UI 토글 비활성. |
| `proactive.quiet_hours` | `{"start":"22:00","end":"08:00"}` | 24시간 `HH:MM`. 서버 시간대 **Asia/Seoul** 고정. `end < start`면 자정을 넘김(22:00–08:00). `start == end`는 400. `tz` 필드는 예약(나중에 추가, MINOR). |
| `proactive.memory_read` | bool, false | 선톡이 메모리를 읽을지(MEM §6, 기본 off). UI는 `memory.enabled=true`일 때만 표시. |
| `memory.enabled` | bool, true | 메모리 주입 전체 on/off(MEM §3). |
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

## 4. 확장 메모 (청사진)
- **job 스레드 표시(Q5)**: 목록에서 job이 만든 스레드를 구분해야 하면 Thread에 `job_id`를 노출(MINOR). P1은 메시지 `provenance`·`template_kind`로 충분.
- **매칭 없음 신호(Q16)**: 필요하면 SSE `tool_result`에 `status:"no_match"`를 노출(MINOR).
- **스레드별 메모리 끄기(Q18)**: `threads.settings` JSONB(`{"memory":false}`) 또는 `PATCH /v1/threads/{id} {"memory":false}`. 프롬프트 동작 변경이라 리뷰 대상.
- **`kind` 값 추가(Q4)**: `digest` 외 값은 MINOR, UI 기본 처리 규칙으로 하위 호환.
- **daily_cap 노출**: 예약 키, 1–3 낮추기만.
- **시간대**: `quiet_hours.tz` 예약.
