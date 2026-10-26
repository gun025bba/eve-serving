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
| Q4 | 플래그 대신 Message `kind` 필드 하나: `null`(일반) \| `digest`(quiet hours 종료 요약). 새 값 추가는 MINOR, UI는 모르는 값을 `null`처럼 처리. | MINOR |
| Q5 | **P1에는 job 전용 스레드 없음.** 선톡은 job의 `thread_id`(사용자 스레드 또는 follow-up 원 스레드)에만 들어간다. 스레드 표시는 메시지 `provenance`로 충분. (청사진 §3) | — |
| Q6 | `POST /v1/threads/{id}/read` → 해당 스레드의 미읽음 proactive 메시지를 모두 `read_at=now()`, 응답 `{unread_count:0}`. 기존 `POST /v1/messages/{id}/read`도 유지. CSRF·Origin 동일. | MINOR |
| Q7 | `GET /v1/settings`, `PATCH /v1/settings`(부분 갱신, merge). 키는 §2. 알 수 없는 키·타입·범위 오류는 400(PM S4-4). | MINOR |
| Q8 | 설정 응답에 읽기 전용 `system` 블록: `proactive_available`(=`GW_PROACTIVE_ENABLED` && !`proactive_off`), `tools_available`(킬스위치 `all_off`가 아님), `memory_read_only`(Q17). PATCH로 `system`을 보내면 400. | MINOR |
| Q9 | 만료된 제안의 "다시 요청" = `POST /v1/proposals/{id}/revise`에 **원래와 같은 `item_ids`**(또는 add 제안은 빈 본문)를 보내 재발급. 새 제안·새 `payload_hash`·새 토큰, 원 제안은 `voided`. 새 모델 턴 없음. 재발급 횟수 상한은 TP 규칙(3회) 공유. | MINOR |
| Q10 | SSE confirmation 이벤트에 `token_expires_at` 포함(`expires_at`과 별개, TP 두 만료 규칙). | MINOR |
| Q11 | `memory.propose` `payload_view`: `{op:"add"\|"delete", items:[{id?, text, kind}], ambiguous:bool}`. add는 `id` 없음·정규화 후 텍스트, delete는 저장된 `id`·`text`·`kind`. `created_at` 등 추가 필드 없음. | MINOR |
| Q12 | follow-up 생성 툴 이름 `proactive.propose_followup`(TP `<domain>.<verb>` 규칙, 스코프 `proactive.write`, 확인 게이트 필수). `payload_view: {when:{at:"RFC3339"}, thread_id, display:"10/28 (화) 09:00", template_id}`. `display`는 서버가 Asia/Seoul로 만든 표시 문자열. | MINOR |
| Q13 | SSE confirmation에 `scopes:["memory.write"]` 등 툴의 선언 스코프를 그대로 포함. UI는 verb가 `write`/`send`면 위험 스타일(삭제 op는 payload_view로 구분). | MINOR |
| Q14 | 서버 거부(시크릿 필터·상한·taint)는 **카드 error 상태**: SSE confirmation이 `status:"denied", error_code:"secret_detected"\|"too_long"\|"tainted_thread"\|…`로 온다(본문 없음). 모델 답변은 별개로 이어짐. 카드 문구는 UI가 코드로 결정. | MINOR |
| Q15 | `selected_ids` 없음. 부분 해제 = `POST /v1/proposals/{id}/revise {item_ids}`, `item_ids ⊆ 원 제안`(아니면 400). 새 제안·새 `payload_hash`·새 토큰을 돌려주고 원 제안은 `voided`. approve 본문은 그대로(TP). | MINOR |
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
- PATCH는 부분 merge, `system` 키 거부, 알 수 없는 키·범위 오류 400, CSRF·Origin 필수, 결과는 `tool_audit`(본문 값만, 시크릿 없음).

## 3. 확장 메모 (청사진)
- **job 전용 스레드(Q5)**: 필요해지면 Thread에 `kind:"job"` + `job_id`를 추가(MINOR). P1에서는 기존 스레드만 사용.
- **매칭 없음 신호(Q16)**: 필요하면 SSE `tool_result`에 `status:"no_match"`를 노출(MINOR).
- **스레드별 메모리 끄기(Q18)**: `threads.settings` JSONB(`{"memory":false}`) 또는 `PATCH /v1/threads/{id} {"memory":false}`. 프롬프트 동작 변경이라 리뷰 대상.
- **`kind` 값 추가(Q4)**: `digest` 외 값은 MINOR, UI 기본 처리 규칙으로 하위 호환.
- **daily_cap 노출**: 예약 키, 1–3 낮추기만.
- **시간대**: `quiet_hours.tz` 예약.
