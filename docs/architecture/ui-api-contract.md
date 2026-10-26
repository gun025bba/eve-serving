# UI ↔ API 계약 — 선톡·메모리 (P1)

상태: 설계(미구현) · 근거: [proactive-messaging.md](proactive-messaging.md)(PM), [memory.md](memory.md)(MEM), `docs/tool-platform.md` §3.2·§10.3(TP), `docs/threads-api.md`
대상: eve-chat-ui-design `PROACTIVE-MEMORY-SPEC.md`의 열린 질문 Q1–Q18.

## 0. 원칙
- **가장 단순하게**: 새 엔드포인트는 4개(`GET/PATCH /v1/settings`, `POST /v1/threads/{id}/read`, `POST /v1/proposals/{id}/revise`)뿐이고, 나머지는 기존 응답에 필드를 더한다.
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
| Q10 | SSE confirmation 이벤트에 `token_expires_at` 포함(`expires_at`과 별개, TP 두 만료 규칙). | MINOR |
| Q11 | `memory.propose` `payload_view`: `{op:"add"\|"delete", items:[{id?, text, kind, updated_at?}], ambiguous:bool}`. add는 `id` 없음·정규화 후 텍스트, delete(후보)는 저장된 `id`·`text`·`kind`·`updated_at`(최근 수정일 표시용). | MINOR |
| Q12 | follow-up 생성 툴 이름 `proactive.propose_followup`(TP `<domain>.<verb>` 규칙, 스코프 `proactive.write`, 확인 게이트 필수). `payload_view: {when:{at:"RFC3339"}, thread_id, display:"10/28 (화) 09:00", template_id}`. `display`는 서버가 Asia/Seoul로 만든 표시 문자열. 보안 규칙은 §3.2. | MINOR |
| Q13 | SSE confirmation에 툴의 선언 `scopes`(예: `["memory.write"]`)를 그대로 포함. **위험 스타일은 verb `send`와 삭제(`payload_view.op="delete"` 또는 `delete` 스코프)에만**, `write`는 기본(primary) 스타일. | MINOR |
| Q14 | 서버 거부는 **카드 error 상태**: SSE confirmation이 `status:"denied"`와 고정 enum `error_code`만 보낸다: `secret_detected` \| `cap_exceeded` \| `tainted` \| `scope_denied` \| `expired` \| `not_found` \| `item_gone` \| `conflict` \| `kill_switch` \| `invalid`(매핑 §3.3). **자유 텍스트 요약 없음**(본문·매칭 내용 노출 방지). UI가 코드별 고정 문구를 쓴다. 값 추가는 MINOR, 모르는 코드는 `invalid` 문구. 모델 답변은 별개로 이어짐. | MINOR |
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
| 404 | `not_found` | 없음 또는 다른 사용자(본문 동일) |
| 409 | `item_gone` | revise 대상 항목이 그사이 삭제됨 |
| 409 | `conflict` | 원 제안이 이미 approve·void됨 |
| 410 | `expired` | root + 24h 경과 |
| 422 | `secret_detected` / `cap_exceeded` | 시크릿 필터·상한 |

SSE 거부 카드는 같은 `error_code`만 쓴다(HTTP 코드 없음). 테스트 `TestErrorCodes_StatusMapping`.

### 3.4 기타 [Sec-L]
- **SSE에는 토큰·토큰 해시를 절대 넣지 않는다**(토큰은 사용자 세션 POST 응답으로만). SSE는 해당 세션 사용자에게만 전송. 테스트 `TestSSEConfirmation_NoTokenOrHash`, `TestSSEConfirmation_OnlyToSessionUser`.
- `POST /v1/threads/{id}/read`의 다른 사용자 스레드 → 404. 테스트 `TestThreadRead_OtherUser404`.
- 설정: `TestSettingsPatch_StrictDecodeRejectsNullDupDeep`, `TestSettingsPatch_NoMapMerge`, `TestSettingsAudit_SecurityKeysBeforeAfter`, `TestSettings_KillSwitchWins`.
- 거부 카드: `TestConfirmationDenied_FixedErrorCodeNoText`.

| ID | 지적 | 반영 |
|---|---|---|
| Sec-M1 | revise 규칙 | §3.1 (404, 소유 재확인, 조건부 1행 void, 체인 상한, root+24h, 상속, 필터 재실행) |
| Sec-M2 | follow-up 툴 | §3.2 (scheduler·taint 거부, thread 고정, 30일, 활성 10개, 템플릿 허용 목록) |
| Sec-L | settings strict decode·감사·킬스위치, SSE 토큰 금지, read 404 | §2, §3.4 |
| 디자이너 | 위험 스타일 send·delete만, job 스레드 첫 실행 생성, 후보 `updated_at`, 고정 error_code, 빈 본문 revise, `template_kind` | §1 Q4·Q5·Q9·Q11·Q13·Q14 |

## 4. 확장 메모 (청사진)
- **job 스레드 표시(Q5)**: 목록에서 job이 만든 스레드를 구분해야 하면 Thread에 `job_id`를 노출(MINOR). P1은 메시지 `provenance`·`template_kind`로 충분.
- **매칭 없음 신호(Q16)**: 필요하면 SSE `tool_result`에 `status:"no_match"`를 노출(MINOR).
- **스레드별 메모리 끄기(Q18)**: `threads.settings` JSONB(`{"memory":false}`) 또는 `PATCH /v1/threads/{id} {"memory":false}`. 프롬프트 동작 변경이라 리뷰 대상.
- **`kind` 값 추가(Q4)**: `digest` 외 값은 MINOR, UI 기본 처리 규칙으로 하위 호환.
- **daily_cap 노출**: 예약 키, 1–3 낮추기만.
- **시간대**: `quiet_hours.tz` 예약.
