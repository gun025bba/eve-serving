# Eve 툴 플랫폼 아키텍처 (설계 제안)

- 상태: **설계 제안만**. 구현·배포·서버 설정 변경 없음. 건영님 결정(t121u)으로 툴별 로드맵/top-3는 취소하고, 앞으로 계속 추가·변경·제거될 툴의 공통 기반을 먼저 설계합니다.
- 기준 코드: main `507eab7` (1.1.0). 현재 툴 코드: `internal/tools/{session,parse,policy}.go`, `internal/handlers/web.go`, `internal/search`, `internal/webfetch`, 마이그레이션 `00002_web_search.sql`.
- 보안 기준: `tool-platform-security-checklist.md`(보안 담당, v1) 전 항목을 §11에 매핑하고 §12에서 '특히 볼 점' 5개에 답합니다.
- 원칙: **기본 거부, 서버 측 강제, 실패 시 닫힘(fail-closed)**.

## 0. 현재 구조 요약 (사실)

| 항목 | 현재 |
|---|---|
| 툴 정의 | `toolJSON` 상수 2개(web_search, web_fetch), `ToolDefs()` |
| 실행 | `Session.Exec`의 `switch name` 하드코딩, 순차 실행 |
| 파싱 | 업스트림 `message.tool_calls` 우선, 없으면 `<tool_call>` 정규식 lenient 파서 |
| 한도 | MaxRounds 3, MaxCalls 5, MaxSearch 3, MaxFetch 3, 결과 총 24000자 |
| 방어 | `WrapUntrusted` 구분자, fetch URL allowlist(검색 결과/사용자 URL만), SSRF Dialer, 레이트리밋 |
| 기록 | `tool_calls`(tool CHECK IN web_search/web_fetch), `web_sources`, 90일 purge |
| 관측 | `llm_tool_calls_total{tool,provider,status}`, `ToolParseFail`, `ToolRounds` |
| UI | SSE `chat.completion.tool_call` / `tool_result` / `sources` |

목표는 이 구조를 **레지스트리 + 정책 엔진 + 실행기 + 감사 로그 + 확인 게이트**로 일반화하는 것입니다.

## 1. 레지스트리

### 1.1 원천: 저장소의 선언 파일 (런타임 등록 없음)
- `tools/<name>/manifest.yaml` + `tools/<name>/schema.json`이 원천. 바이너리에 `go:embed`로 포함하고 기동 시 검증. **런타임 등록 API는 만들지 않는다**(엔드포인트 부재를 테스트로 고정: `POST/PUT/DELETE /v1/tools*` → 404/405).
- Go 쪽은 `tools.Register(Adapter)`를 `init()`에서 호출(현재 `search.Register` 패턴과 동일). 매니페스트와 어댑터가 1:1로 맞지 않으면 **기동 실패**.

```yaml
# tools/web_search/manifest.yaml (예)
name: web_search
version: 1.0.0                # 툴 semver (§2)
kind: builtin                 # builtin | mcp_stdio
source: github.com/gun025bba/eve-serving   # 허용 목록 repo만
digest: ""                    # mcp_stdio일 때 필수 (@sha256 / --require-hashes)
license: MIT
scopes: [web.read]            # 선언된 스코프 밖 호출은 거부
side_effect: read             # read | write | send | trade
taint_output: true            # 결과가 외부 데이터인가
egress: [127.0.0.1:8888]      # 외부 접속 allowlist (SSRF guard와 결합)
secrets: []                   # 이 툴에만 주입할 시크릿 이름
timeout: 8s
max_result_chars: 6000
rate_per_min: 10
exposure: {modes: [auto, force], max_per_turn: 3}
```
- 매니페스트는 JSON Schema로 검증, **미지정 필드 거부**(`additionalProperties:false`). 잘못된/누락 매니페스트 거부 단위 테스트.

### 1.2 툴 인자 스키마
- JSON Schema(draft 2020-12 부분집합: type/enum/min/max/maxLength/required/`additionalProperties:false`). 모델에 노출할 OpenAI `function.parameters`와 **서버 검증**에 같은 파일을 쓴다(`santhosh-tekuri/jsonschema` 등, go.sum 고정).
- 작은 모델용 규칙: 인자 ≤4개, 중첩 객체 금지, 자유 문자열 대신 enum 우선, 날짜는 `YYYY-MM-DD` 문자열.

### 1.3 enable/disable와 킬스위치
- **disable(끄는 방향)은 재배포 없이 즉시**: `GW_TOOLS_DISABLED=web_fetch,mail_send`, `GW_TOOLS_ALL_OFF=true` 또는 `/etc`의 600 파일을 SIGHUP으로 재로드. 끄는 것은 위험을 줄이는 방향이라 승인 없이 가능(사후 보고).
- **enable(켜는 방향)은 메이저 배포**: 처음 켜기, 다시 켜기 모두 코드/선언 변경으로만. 설정 파일로는 "끄기 목록"만 표현 가능하게 해서 설정으로 권한이 넓어지는 경로를 없앤다.
- 정책 로드 실패 시 **전부 꺼진 상태로** 기동(fail-closed).

### 1.4 MCP 호환
| 질문 | 답 |
|---|---|
| 툴을 MCP 서버로 만들 수 있나 | 가능. 하지만 1차는 **in-process builtin**. MCP 서버는 같은 매니페스트를 갖춘 `kind: mcp_stdio`로만 허용 |
| 게이트웨이가 MCP 클라이언트가 되나 | 가능(stdio만, HTTP/SSE 원격 MCP 금지 = tailnet 경계 유지). MCP `tools/list` 결과는 **신뢰하지 않음**: 매니페스트의 스키마/설명을 쓰고, 서버가 알려준 목록과 다르면 로드 거부(도구 설명을 통한 인젝션/"rug pull" 차단) |
| 게이트웨이를 MCP 서버로 노출? | 이번 범위 아님(노출 범위 변경 = 메이저) |
| 공급망 위험 | 서드파티 MCP 서버는 임의 코드 실행 + 시크릿 접근. 완화: digest 고정(npm/pip 해시, 컨테이너 `@sha256`), 허용 repo, 서명/cosign 검증, govulncheck/osv-scanner high↑ 실패, 라이선스·유지보수 기록, 별도 macOS 사용자로 실행(파일시스템 최소 권한), egress allowlist, 해당 툴 시크릿만 env로 주입. **원칙: 직접 짠 얇은 어댑터 > 검증된 MCP > 그 외 금지** |

## 2. 버전 관리

- **툴 semver**(매니페스트 `version`)와 **게이트웨이 semver**(git 태그, 현재 1.1.0)를 분리.
  - 툴 major: 스키마 비호환(필수 인자 추가/삭제, 의미 변경), 스코프·side_effect·egress 변경.
  - 툴 minor: 선택 인자 추가, 결과 필드 추가. 툴 patch: 버그 수정.
- **게이트웨이 배포 분류(보안 체크리스트 + t118u 규칙):** 툴의 **등록·해제·버전 변경·스코프 확대·egress 확대·enable은 툴 버전 수준과 무관하게 모두 게이트웨이 MAJOR 배포**이며 건영님 직접 승인이 필요. 즉 툴의 patch 버전 변경도 게이트웨이는 메이저. 마이너/패치로 남는 것은 툴 집합과 무관한 변경(플랫폼 내부 리팩터로 동작 동일, 관측 지표 추가, 문서)뿐. 애매하면 메이저.
- CI 가드: `tools/**` 또는 레지스트리 코드 diff가 있는데 PR 라벨/버전이 major가 아니면 실패. `CODEOWNERS`에 `tools/**` → 보안 리뷰 필수.
- 호환: `tool_calls`에 `tool_version` 기록. 히스토리 재생 시(`SanitizeHistory`) 현재 레지스트리에 없는 툴 호출은 텍스트 요약으로 접어 넣는다.
- 롤백: 이전 git 태그 바이너리로 kickstart(기존 배포 롤백 절차). 툴만 즉시 멈추려면 킬스위치(재배포 불필요). DB 마이그레이션은 additive only라 이전 바이너리와 공존 가능.

## 3. 권한 스코프, 확인 게이트, 시크릿

### 3.1 스코프
- 스코프 이름: `<domain>.<verb>`, verb ∈ `read | write | send | trade`. 예: `web.read`, `calendar.read`, `calendar.write`, `mail.send`, `stock.read`.
- 정책 파일 `policy/grants.yaml`(저장소, 코드 리뷰 대상): 사용자(Tailscale 로그인) → 허용 스코프. **기본 거부**, 신규 툴은 read 전용으로 시작. 정책은 게이트웨이가 집행하며 모델 출력/프롬프트의 권한 주장은 의미 없음(정책 입력에 모델 텍스트가 들어가지 않음).
- 사용자 식별은 기존 경계(루프백 피어의 Tailscale 헤더)만.
- `trade` 스코프는 **정책 스키마에서 금지값**(로드 시 거부). 증권 툴은 조회 전용 키만 등록, 주문 권한 키는 발급하지 않음. 주문·결제·송금 코드 경로 자체를 만들지 않음.

### 3.2 쓰기/송신 확인 게이트 (pending_actions)
흐름:
1. 모델이 write/send 툴 호출 → 실행기는 **실행하지 않고** 인자를 스키마 검증·정규화(RFC 8785 JCS) → `payload_hash = sha256(user|tool|tool_version|canonical_args)`.
2. `pending_actions` 행 생성, SSE `chat.completion.confirmation` 카드 송출(실제 실행될 페이로드 그대로: 수신자, 본문 요약, 일시). 모델에게는 `{"status":"pending_confirmation","action_id":...}`만 반환.
3. 사용자가 UI에서 승인 → `POST /v1/actions/{id}/approve` (본문: `confirm_token`, `payload_hash`). 서버는 (사용자 일치, 미만료 ≤120초, 미사용, 해시 일치) 확인 후 **원자적으로** `status: pending→executing` 업데이트(1회성) 후 실행.
4. 결과를 `executed|failed`로 기록하고 다음 턴 컨텍스트에 요약 주입.
- **confirm_token**: 서버 랜덤 32바이트, DB엔 sha256만 저장, (user, tool, payload_hash)에 바인딩, 1회용. 재사용/만료/페이로드 1자 변경 시 거부.
- **멱등성**: `idempotency_key = payload_hash + action_id`를 외부 API에 전달 가능하면 전달, 아니면 `executing` 상태 락 + `executed` 후 재시도 차단. 같은 approve 재요청은 저장된 결과를 반환.
- 모델 텍스트나 사용자의 "응/승인" 채팅 텍스트는 확인으로 **인정하지 않음**. 승인은 UI 버튼 → 별도 엔드포인트만.
- 턴당 쓰기 제안 1회.

```sql
CREATE TABLE pending_actions (
  id            TEXT PRIMARY KEY,                -- ULID
  user_id       BIGINT NOT NULL REFERENCES users(id),
  thread_id     TEXT NOT NULL REFERENCES threads(id),
  message_id    TEXT NOT NULL REFERENCES messages(id),
  tool          TEXT NOT NULL,
  tool_version  TEXT NOT NULL,
  scope         TEXT NOT NULL,
  payload_hash  TEXT NOT NULL,
  payload_view  JSONB NOT NULL,                  -- 확인 카드용 표시본(시크릿 없음, 실행 후 purge)
  token_sha256  TEXT NOT NULL,
  tainted_turn  BOOLEAN NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('pending','executing','executed','failed','rejected','expired')),
  expires_at    TIMESTAMPTZ NOT NULL,
  decided_at    TIMESTAMPTZ,
  result_ref    TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 3.3 시크릿
- 저장: macOS Keychain(`security` / keychain-go, 서비스명 `eve.tool.<name>`) 우선, 대안은 `~/.eve/secrets/<tool>`(chmod 600, 소유자 = 서비스 계정). 권한이 600이 아니거나 소유자가 다르면 **기동 거부**. 저장소·plist env·이미지에 넣지 않음.
- 툴별 분리: 실행기가 매니페스트 `secrets`에 선언된 이름만 해당 어댑터 `Context`에 주입. 다른 툴 시크릿 조회는 에러(테스트).
- 리다크션: 기동 시 로드한 시크릿 값 집합으로 `Redactor`를 만들어 (a) 툴 결과 → 모델, (b) 로그, (c) DB 쓰기, (d) 에러 메시지, (e) SSE 직전에 치환(`[REDACTED]`). `Authorization`/`Cookie` 헤더는 HTTP 클라이언트 에러 래핑에서 제거. DB엔 시크릿 참조 ID만.
- 테스트: canary 시크릿 주입 → 로그·DB 덤프·모델 입력 grep으로 부재 확인, CI에 gitleaks.
- 회전/폐기: 툴별 runbook(회전 주기, 유출 시 revoke → 킬스위치 → 교체), 회전 리허설 1회 기록.

## 4. 실행 엔진

```go
type Adapter interface {
    Name() string
    Call(ctx context.Context, in Input) (Output, error) // in.Args: 스키마 검증 끝난 값, in.Secrets: 선언분만
}
type Output struct {
    Data    any       // 모델에 줄 구조화 결과
    Sources []Source  // 인용
    Tainted bool      // 외부 데이터 포함
}
```
- 파이프라인(모든 단계 실패 시 닫힘): 킬스위치 → 레지스트리 조회 → 스코프 정책 → 턴 예산(§4.1) → taint 정책 → 스키마 검증 → 레이트리밋 → (write면 pending_action 생성 후 종료) → 감사 로그 `started` 기록(**실패 시 실행 안 함**) → 어댑터 실행(timeout) → 크기 제한·리다크션·래핑 → 감사 로그 완료.
- **타임아웃**: 매니페스트 `timeout`(기본 8s, 상한 30s), 요청 전체 데드라인 별도.
- **재시도**: read 툴만, 네트워크/5xx/timeout에 한해 1회(지수 백오프 250ms). write는 재시도 없음(멱등키 있는 외부 API만 예외).
- **동시성**: 현재처럼 한 턴 내 순차가 기본. read 툴끼리는 최대 3 병렬 허용 옵션(결과 순서는 호출 순서 유지). write는 항상 단독.
- **에러 분류** status: `ok | error | blocked | timeout | denied | pending_confirmation | mock_miss`. 모델에게는 짧은 코드만(`{"status":"denied","error":"scope_not_granted"}`), 상세는 감사 로그에.
- **결과 형식(모델 입력)**: 현재 `WrapUntrusted` 유지 + 일반화.
  ```
  <untrusted_tool_output tool="calendar_list_events" call="call_1_0" tainted="true">
  {"status":"ok","data":{...},"sources":[{"n":1,...}]}
  </untrusted_tool_output>
  ```
  web 툴은 이전 단계에서는 기존 태그명 `untrusted_web_content`를 그대로 사용(§8).

### 4.1 턴 예산
- **턴당 툴 호출 최대 5회**(현재 MaxCalls 5를 플랫폼 상한으로 고정, 설정으로 늘릴 수 없음·줄이기만 가능), 라운드 3, **쓰기 제안 1회**, 결과 총 24000자. 초과 시 `blocked` + FinalNotice.

## 5. 실행·감사 로그 (Postgres)

- `tool_calls`를 확장(기존 행·purge·cascade 호환, additive). 감사 원장은 메시지 삭제와 무관하게 남아야 하므로 별도 `tool_audit` 테이블을 append-only + 해시 체인으로 둔다.

```sql
-- 00004_tool_platform.sql (스케치, 모두 additive)
-- +goose Up
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_tool_check;     -- 이름 고정 CHECK 제거(레지스트리가 검증)
ALTER TABLE tool_calls
  ADD COLUMN thread_id     TEXT REFERENCES threads(id) ON DELETE CASCADE,
  ADD COLUMN tool_version  TEXT,
  ADD COLUMN scope         TEXT,
  ADD COLUMN side_effect   TEXT,
  ADD COLUMN args_sha256   TEXT,
  ADD COLUMN tainted_turn  BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN pending_action_id TEXT REFERENCES pending_actions(id),
  ADD COLUMN retries       SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check,
  ADD CONSTRAINT tool_calls_status_check CHECK (status IN
  ('ok','error','blocked','timeout','denied','pending_confirmation','mock_miss'));

CREATE TABLE tool_audit (
  seq          BIGSERIAL PRIMARY KEY,
  at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  user_id      BIGINT NOT NULL,          -- FK 없음: 메시지/스레드 삭제와 무관하게 보존
  thread_id    TEXT, message_id TEXT, tool_call_id TEXT, pending_action_id TEXT,
  event        TEXT NOT NULL,            -- call_started|call_done|denied|action_proposed|approved|rejected|executed|registry_loaded|killswitch
  tool TEXT, tool_version TEXT, scope TEXT,
  payload_sha256 TEXT, confirm_token_id TEXT, tainted BOOLEAN, status TEXT,
  prev_hash    TEXT NOT NULL,
  hash         TEXT NOT NULL             -- sha256(prev_hash || canonical(row))
);
REVOKE UPDATE, DELETE, TRUNCATE ON tool_audit FROM eve_app;   -- 앱 역할은 INSERT/SELECT만
-- 90일 삭제는 별도 역할 eve_retention이 오래된 앞부분만 삭제하고, 삭제 직전 마지막 hash를 체크포인트 행으로 남김
-- +goose Down
-- tool_audit/pending_actions DROP, 추가 컬럼 DROP (데이터 손실 → Down 실행 자체가 메이저 판단 대상)
```
- 본문은 저장하지 않고 해시만. `args`(JSONB)는 기존처럼 보관 기간 후 purge.
- 무결성 검증: `eve-audit verify`(체인 재계산)를 일 1회 실행, 깨지면 알림.
- 이상 탐지 알림(ntfy/로그): 거부 급증, 00–06시 쓰기, taint 턴의 쓰기 시도, 처음 보는 수신자.

## 6. 관측

- 기존 `llm_tool_calls_total{tool,provider,status}` 유지 + `tool_version` 라벨.
- 신규: `eve_tool_latency_seconds{tool}`(histogram), `eve_tool_denied_total{tool,reason}`, `eve_tool_retries_total{tool}`, `eve_tool_parse_failures_total{reason}`(기존 ToolParseFail 일반화), `eve_tools_exposed{mode}`(gauge), `eve_pending_actions_total{tool,outcome}`, `eve_audit_write_failures_total`(>0이면 경고), `eve_killswitch{tool}`.
- 실패율 = `status!="ok"` / 전체. 대시보드: 툴별 호출/실패율/p50·p95, 확인 승인율, taint 턴 비율.

## 7. 모델 통합

- **주입 방식**: 현재처럼 OpenAI `tools` + `tool_choice:"auto"`. 레지스트리에서 이번 요청에 노출할 툴만 골라 `ToolDefs()` 대신 `Registry.Expose(req)` 사용. 파서는 기존 2단계(`message.tool_calls` → `<tool_call>` lenient) 유지하되 이름 검사를 하드코딩 대신 "이번 요청에 노출된 툴 집합"으로.
- **노출 상한**: Qwen3-30B-A3B 기준 **턴당 최대 5개**(권장 3개). 평가로 3/5/8 곡선을 측정해 확정(모델 엔지니어).
- **라우팅/선택**: 1차 규칙 기반(UI 토글·모드 + 키워드/의도 사전), 예: 웹 토글 on → web 툴, "일정" → calendar 툴. 2차로 필요 시 경량 분류 호출(같은 모델, `max_tokens` 소량, enum 출력). write 툴은 사용자 메시지에 명시적 의도가 있을 때만 노출.
- **제약 JSON**: 스키마 단순화(§1.2), 서버 검증 실패 시 에러를 모델에 돌려 **1회 재시도**, 그래도 실패하면 툴 없이 답변.
- **시스템 프롬프트**: 노출 툴에 따라 게이트웨이가 고정 텍스트 조립(외부 데이터 절대 불포함). Eve 시스템 프롬프트(892bb18)는 별도 평가/승인 트랙.
- **인젝션 방어**
  - 외부 데이터는 구분자로 감싼 `role:tool` 데이터로만, system prompt에 넣지 않음. 구분자 유사 문자열은 defang(현행 `tagRe` 일반화).
  - **taint 추적**: `Output.Tainted` 또는 `taint_output:true` 툴 결과가 들어온 순간 그 턴(요청) 전체가 tainted. tainted 턴에서는 write/send 자동 실행 불가 → 항상 확인 게이트, 카드에 "외부 콘텐츠를 읽은 뒤 제안된 작업" 경고. 수신자/대상이 사용자 메시지에 없고 툴 결과에서만 나온 경우 **차단**.
  - **유출 차단**: egress는 매니페스트 allowlist + 기존 SSRF Dialer(tailnet/사설 대역 차단). URL·쿼리 인자는 (a) 사용자 메시지나 직전 검색 결과에 있던 값만(현행 web_fetch allowlist 일반화), (b) 시크릿/ tainted 텍스트의 8자 이상 부분열을 포함하면 차단. UI는 외부 이미지 차단 + CSP.
  - 모델에게 다른 툴 호출을 지시하는 툴 결과는 무시하도록 프롬프트 + 서버 측 예산/taint 규칙으로 강제(프롬프트에만 의존하지 않음).

## 8. 첫 예시: web_search/web_fetch 이전 (동작 변화 없음)

1. `tools/web_search`, `tools/web_fetch` 매니페스트 작성, `schema.json`은 현재 `toolJSON`의 `parameters`와 **바이트 단위 동일**(골든 테스트).
2. `Session.search/fetch`를 `Adapter`로 감싸기만 함. 한도·URL allowlist·SSRF·`WrapUntrusted` 태그명·SystemPrompt·FinalNotice·SSE 이벤트·`web_sources`는 그대로.
3. `Session.Exec`의 switch → `Registry.Exec`. `ParseTextToolCalls`의 이름 검사 → 노출 집합.
4. 회귀 기준: 기존 `internal/tools/tools_test.go`, `internal/handlers/web_test.go` 무수정 통과 + 녹화 대화(off/auto/force 각 5건)에서 업스트림 요청 본문과 SSE가 동일.
5. 감사 로그·확인 게이트는 web 툴(read)에도 기록만 추가(동작 영향 없음).
- 분류: 툴 집합·스코프·노출 불변이면 **마이너**(1.2.0, 내부 리팩터+additive 마이그레이션). 단 `tool_calls` CHECK 제거가 포함되므로 보안 리뷰 필수, 애매하다고 판단되면 메이저로 승인 받음(§12-1 참고, 건영님 결정 사항).

## 9. 단계별 마일스톤

| 단계 | 내용 | 분류 | 승인 |
|---|---|---|---|
| M0 | 이 설계 문서 PR, 보안 리뷰 | 문서 | — |
| M1 | 레지스트리/매니페스트 검증, 실행기 파이프라인, 킬스위치(끄기 전용), 리다크터, `GW_TOOL_MOCK_DIR`, 관측 지표, web 툴 이전(§8) | 마이너 1.2.0 (툴 집합 불변) | 보고만 (애매하면 메이저) |
| M2 | 감사 원장 `tool_audit`(해시 체인, 역할 권한 분리), 00004 마이그레이션(additive), 일일 verify, 이상 탐지 알림 | 마이너 1.3.0 | 보고만 |
| M3 | 스코프 정책 + taint 엔진 + 유출 차단 + 인젝션 회귀 세트 CI | 마이너 1.4.0 (현행 툴 동작 불변 시) | 보고만 |
| M4 | 확인 게이트(pending_actions, approve/reject API, SSE 카드) — 아직 write 툴 없음, 테스트 툴로만 | **메이저 2.0.0** (API 표면·보안 경계 변경) | 건영님 승인 |
| M5+ | 첫 신규 툴 등록(종류는 건영님 결정) | **메이저** | 건영님 승인 |
| 이후 | 툴 추가·제거·버전 변경·스코프 확대·enable·MCP 서버 도입 | **항상 메이저** | 매번 승인 |
| M3.5 | 웹 도메인 허용/차단 정책(§13), block_reason 버그 정리 | **메이저 권장** (allowlist_only는 메이저) | 건영님 승인 |
| 수시 | 킬스위치로 끄기 | 운영 조치 | 사후 보고 |

각 배포마다 버전, 변경 내용, 롤백 방법을 control plane에 보고(t118u).

## 10. 다른 봇과의 인터페이스

### 10.1 보안 담당
- §11 매핑 표가 체크리스트 대응표. 각 PR은 해당 항목의 검증 테스트를 포함해야 머지. `CODEOWNERS`: `tools/**`, `policy/**`, `internal/tools/registry*`, `migrations/*tool*` → 보안 리뷰.

### 10.2 모델 엔지니어 (평가 훅)
상세본: `/workspace/reports/eve-tool-eval-handoff.md`. 요약:
- **현재 스키마(코드 원문, `internal/tools/session.go` toolJSON)**
```json
{"type":"function","function":{"name":"web_search","description":"Search the web for current information. Returns numbered results (title, url, snippet). Call web_fetch on a result URL to read the page.","parameters":{"type":"object","properties":{"query":{"type":"string","description":"search query","maxLength":256},"lang":{"type":"string","enum":["auto","ko","en"]},"time_range":{"type":"string","enum":["","day","week","month","year"]},"max_results":{"type":"integer","minimum":1,"maximum":8}},"required":["query"]}}}
{"type":"function","function":{"name":"web_fetch","description":"Read the main text of a web page. Only URLs returned by web_search in this answer, or URLs the user wrote, are allowed.","parameters":{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"max_chars":{"type":"integer","minimum":500,"maximum":12000}},"required":["url"]}}}
```
- **호출 메커니즘**: OpenAI tools + `tool_choice:"auto"`, 라운드마다 non-stream 업스트림 호출. `message.tool_calls` 우선, 없으면 `<tool_call>` 정규식 파서(`parse.go`). 한 응답의 여러 호출 허용, 순차 실행. MaxRounds 3 / MaxCalls 5, 소진 시 FinalNotice 후 tools 없이 최종 답변. force 모드는 게이트웨이가 선실행.
- **목 주입 지점(현재)**: `search.Provider`(`internal/search/search.go`), `tools.Fetcher`·`tools.Deps`(`internal/tools/session.go`), `webfetch.Resolver`(`internal/webfetch/fetch.go`), fake: `internal/tools/tools_test.go`, `internal/handlers/web_test.go`, `internal/search/search_test.go`, fixture `internal/search/testdata/searxng.json`.
- **계획**: `GW_TOOL_MOCK_DIR`(fixtures/<tool>/<sha256(args)>.json, 운영 환경에서는 기동 거부), `Adapter` fake 등록, `tool_calls` 시퀀스 export, 노출 툴 수별 정확도·인젝션 fixture 지표.

### 10.3 프론트엔드 계약
SSE(기존 이벤트 유지 + 추가):
```json
{"object":"chat.completion.tool_call","id":"call_1_0","tool":"web_search","args":{...공개 인자},"round":1}
{"object":"chat.completion.tool_result","id":"call_1_0","tool":"web_search","status":"ok","sources":[...]}
{"object":"chat.completion.confirmation","action_id":"01J...","tool":"calendar_create_event","tool_version":"1.0.0",
 "summary":"10/28 15:00 치과 일정 추가","payload_view":{...실제 실행 값},"payload_hash":"sha256:...",
 "confirm_token":"<1회용>","expires_at":"2026-10-26T11:02:00Z","tainted":true}
{"object":"chat.completion.action_result","action_id":"01J...","status":"executed|failed|rejected|expired"}
```
엔드포인트:
- `POST /v1/actions/{id}/approve` `{confirm_token, payload_hash}` → 200 `{status:"executed",result}` / 409 `action_already_decided`(같은 결과 반환) / 410 `action_expired` / 422 `payload_mismatch` / 403 `not_owner`.
- `POST /v1/actions/{id}/reject` → 200.
- `GET /v1/actions?status=pending` → 새로고침 후 카드 복원(토큰은 재발급).
- 스레드 로드 시 메시지에 `pending_actions` 요약 포함. UI는 카드에 payload_view를 그대로 표시, 외부 이미지 금지, 텍스트 "응"은 승인으로 쓰지 않음.

## 11. 보안 체크리스트 → 설계 매핑

### 11.1 parent가 정리한 11개 핵심 항목
| # | 항목 | 구현 위치 |
|---|---|---|
| 1 | read/write 분리, 서버 allowlist | §3.1 스코프·grants.yaml, §4 파이프라인 정책 단계 |
| 2 | Keychain/600 파일, 프롬프트·로그 리다크션 | §3.3 |
| 3 | taint 추적, tainted 턴 쓰기 차단/확인 | §7 taint, §3.2 |
| 4 | 턴당 연쇄 호출 5회 | §4.1 |
| 5 | 유출 경로 차단 | §7 유출 차단, §1.1 egress |
| 6 | 1회용·페이로드 해시 바인딩 확인 토큰 | §3.2 |
| 7 | append-only 해시 체인 감사, 90일 | §5 tool_audit |
| 8 | 툴별 킬스위치 | §1.3 |
| 9 | 인젝션 회귀 테스트 CI | §7, M3, §11.2-5 |
| 10 | 증권 툴 주문 권한 없음 | §3.1 trade 금지 |
| 11 | 신규 툴 연동 배포 = MAJOR | §2, §9 |

### 11.2 보안 체크리스트 v1 전 항목
| 체크리스트 | 설계 대응 | 검증 |
|---|---|---|
| 1-1 매니페스트, 미지정 필드 거부 | §1.1 | 잘못된 매니페스트 거부 테스트 |
| 1-2 버전·digest 고정 | §1.4, 매니페스트 `digest` | CI grep(태그/latest/브랜치 참조 실패), 로드 시 digest 불일치 거부 |
| 1-3 서명·허용 repo | §1.4 | 무서명 아티팩트 거부 테스트 |
| 1-4 라이선스·CVE·유지보수 | §1.4, PR 템플릿 | govulncheck/osv-scanner high↑ 실패 |
| 1-5 등록·해제·버전·스코프 = 메이저 | §2, §9, §12-1 | CODEOWNERS, CI 라벨 가드, `registry_loaded` 감사 이벤트 |
| 1-6 선언 파일 원천, 런타임 등록 없음 | §1.1 | 엔드포인트 404/405 테스트 |
| 2-1 read/write/send/trade 분리 | §3.1 | 미선언 스코프 거부 |
| 2-2 기본 거부, 신규 read 전용 | §3.1 | 빈 정책 전부 거부 |
| 2-3 서버 강제, 모델 주장 무시 | §3.1, §4 | "권한 있음" 주장 호출 거부 |
| 2-4 외부 OAuth 최소 | §3.1(증권 조회 키), 등록 시 introspection 기록 | 스코프 초과 시 등록 거부 |
| 2-5 Tailscale 경계만 | §3.1 | 기존 auth 테스트 |
| 3-1 툴별 자격증명 | §3.3 | 타 툴 시크릿 접근 실패 |
| 3-2 평문 금지 | §3.3 리다크터, §5 해시만 | canary grep, gitleaks |
| 3-3 Keychain/600 | §3.3 | 600 아니면 기동 거부 |
| 3-4 회전·폐기 | §3.3 runbook | 리허설 기록, 폐기 토큰 실패 |
| 3-5 에러 리다크션 | §3.3 | 업스트림 에러 주입 검사 |
| 4-1 서버 1회용 토큰, 해시 바인딩, ≤2분 | §3.2 | 재사용/만료/1자 변경 거부 |
| 4-2 UI 사람 확인만 | §3.2 | 모델 확인 문구로 실행 안 됨 |
| 4-3 실제 페이로드 표시 | §3.2, §10.3 payload_view | UI 테스트 |
| 4-4 결제·주문·송금 기본 비활성 | §3.1 trade 금지(코드 경로 없음) | 해당 스코프 거부 |
| 5-1 taint 표시·전파 | §7 | 전파 단위 테스트 |
| 5-2 taint 턴 쓰기 차단/확인 | §7, §3.2 | 전달 지시 fixture 회귀 |
| 5-3 연쇄 제한(N회, 쓰기 1) | §4.1 (5회, 쓰기 1) | 상한 테스트 |
| 5-4 유출 차단, SSRF 재사용, CSP | §7, §1.1 | 쿼리스트링 유출·이미지 마크다운 시나리오 |
| 5-5 외부 콘텐츠 system prompt 금지 | §7 | 프롬프트 조립 테스트 |
| 5-6 인젝션 회귀 CI, 툴 추가 시 시나리오 필수 | M3, CI 가드(`tools/<name>/injection_test` 없으면 실패) | CI |
| 6-1 감사 필드 | §5 tool_audit | 스키마 테스트, canary 부재 |
| 6-2 append-only | §5 REVOKE + 해시 체인 | 앱 역할 UPDATE/DELETE 실패 |
| 6-3 90일, 별도 역할 | §5 eve_retention | purge 테스트 |
| 6-4 이상 탐지 | §5 | 합성 이벤트 알림 |
| 7-1 킬스위치, 일괄 off | §1.3 | 즉시 거부 테스트, 리허설 |
| 7-2 fail-closed | §4 파이프라인, §12-5 | 의존성 장애 주입 테스트 |
| 7-3 타임아웃·크기·레이트 | §4, 매니페스트 | 기존+확장 테스트 |
| 7-4 런타임 분리 권장 | §1.4 mcp_stdio는 별도 사용자. builtin은 게이트웨이 내(현재와 동일) — 별도 프로세스 분리는 열린 결정 | — |

### 11.3 보안 체크리스트 요약 항목별 명시 대응
| 요약 항목 | 이 문서의 명시 |
|---|---|
| 레지스트리 원천 = 저장소 선언 파일, 런타임 등록 API 없음 | §1.1. `tools/<name>/manifest.yaml`(+`deploy/policy/*.yaml`)만 원천, `go:embed`. `/v1/tools*` 쓰기 엔드포인트는 존재하지 않으며 404/405 테스트로 고정 |
| 스코프 read/write/send/trade, 기본 거부, 신규 툴 read 전용 | §3.1. `trade`는 정책 로드 시 거부되는 금지값 |
| 시크릿은 한 툴에만 주입, 권한 불량 시 기동 거부, canary·gitleaks | §3.3 |
| 확인 토큰 ≤2분, (user, tool, payload hash) 바인딩, UI에 페이로드 표시 | §3.2(120초, tool_version까지 바인딩), §10.3 `payload_view` |
| 턴당 N회 호출·쓰기 1회, 모델이 만든 URL fetch 금지, 외부 콘텐츠 system prompt 금지 | §4.1(N=5), §7 유출 차단(URL은 사용자 메시지/이번 검색 결과에서 온 값만 — 현행 `Session.allow` 일반화), §7 |
| 감사 테이블 앱 역할 UPDATE/DELETE 없음, 거부 급증·taint 턴 쓰기 시도·신규 수신자 알림 | §5 `REVOKE`, 해시 체인, 이상 탐지 |
| 정책·시크릿·감사·확인 서비스 장애 시 fail-closed, 타임아웃·응답 크기·레이트 한도, 별도 사용자/컨테이너 실행 권장 | §4 파이프라인, §12-5, 매니페스트 `timeout`/`max_result_chars`/`rate_per_min`, §1.4(MCP는 별도 사용자 필수, builtin 분리는 §14-3) |
| '특히 볼 점' 5개 | §12에서 하나씩 답변 |

## 12. '설계 문서와 대조할 때 특히 볼 점' 답변

1. **등록·해제·버전 변경·스코프 확대가 마이너/설정으로 분류되지 않았나?** — 모두 게이트웨이 **MAJOR**(§2, §9). 설정으로는 "끄기"만 가능하고 켜기·확대는 표현 불가(§1.3). 런타임 등록 API 없음(§1.1). 예외 후보는 M1의 web 툴 이전(툴 집합 불변) 하나뿐이며, 이것도 건영님이 메이저로 보길 원하면 메이저로 처리(열린 결정).
2. **확인 게이트가 서버에서 페이로드 해시에 바인딩되나?** — 예. 토큰 발급·검증·상태 전이 모두 게이트웨이, (user, tool, tool_version, JCS 정규화 args) sha256 바인딩, 1회용, ≤120초, 원자적 상태 전이(§3.2). 클라이언트는 버튼만, 모델 텍스트는 무효.
3. **taint 전파와 쓰기 차단이 명시됐나?** — §7: 외부 결과가 들어오면 턴 전체 tainted, tainted 턴의 write는 자동 실행 불가(확인 강제), 대상이 툴 결과에서만 온 경우 차단, 감사에 `tainted` 기록.
4. **시크릿이 DB/모델 컨텍스트로 가는 경로가 없나?** — 시크릿은 어댑터 `Input.Secrets`로만 전달되고 모델 입력·결과·로그·DB·SSE·에러 5개 출구 모두 리다크터 통과, DB엔 참조 ID만, `payload_view`엔 시크릿 필드 정의 금지(§3.3). canary 테스트로 검증.
5. **fail-open 기본값이 없나?** — 정책 로드 실패 → 전부 off로 기동, 시크릿 조회 실패 → 해당 호출 denied, 감사 `started` 기록 실패 → 실행 안 함, 확인 서비스(DB) 장애 → write 거부, 파일 권한 불량 → 기동 거부, MCP `tools/list` 불일치 → 로드 거부, 스키마 검증 실패 → 실행 안 함, 미등록 툴 이름 → 거부(§1, §3, §4). 단 **현재 web 툴은 감사 기록 실패 시에도 실행되는 구조**라 M1에서 fail-closed로 바뀌는 점이 유일한 동작 차이 후보 → 감사 실패 시 web 툴도 막을지 건영님 결정.

## 13. 웹 검색 도메인 허용/차단 정책 (설계만)

- **권한 주체는 게이트웨이**: 검색 결과 필터링과 `web_fetch` 둘 다 게이트웨이가 판정. SearXNG `hostnames` 플러그인은 심층 방어용 보조일 뿐 정책 원천이 아님(SearXNG 설정이 바뀌어도 게이트웨이 판정은 동일).
- **선언 파일**: `deploy/policy/web-domains.yaml`(저장소, CODEOWNERS 보안 리뷰), JSON Schema 검증, 미지정 필드 거부. **로드 실패 시 검색·fetch 모두 fail-closed**(`web_search_unavailable`).
  ```yaml
  version: 1
  mode: denylist          # denylist | allowlist_only
  deny:  ["*.example-bad.com", "tracker.example"]
  allow: []               # allowlist_only일 때만 의미
  ```
- **정규화**: 소문자화, IDNA punycode(`golang.org/x/net/idna` Lookup 프로필), 끝 점 제거, 포트 제거. `*.x`는 **서브도메인만** 매치(`x` 자체는 미포함, 필요하면 둘 다 적음). 같은 호스트가 둘 다 맞으면 **deny 우선**.
- **판정 시점**: 검색 결과 URL, fetch 요청 URL, **리다이렉트 매 hop**, 최종 `final_url`. 현행 `webfetch` 리다이렉트 처리(`fetch.go`의 cross-domain allowlist 검사) 지점에 도메인 정책 검사를 추가.
- **결과 형식**
  - 검색: 차단된 결과는 제거하고 툴 결과에 `blocked_count`만 포함. **차단 도메인 목록·이름은 모델에 보내지 않음**(우회 유도 방지).
  - fetch: `status:"blocked"`, `block_reason:"domain_denied"`(`webfetch` 상수 `ReasonDomainDenied` 추가). 현재 일부 경로에서 `block_reason`이 비어 기록되는 버그도 이 작업에서 모든 blocked 경로가 상수 reason을 갖도록 정리(빈 값이면 테스트 실패).
  - SSE `tool_result`에 `blocked_count` 추가 → UI는 '차단된 출처 n개' 칩 표시(도메인 목록은 UI에도 기본 비표시, 설정 화면에서만).
- **통합**: 매니페스트 `egress`와 같은 정규화·매처를 쓰는 `internal/policy/domains` 하나로 통일. `web.read` 스코프의 egress = (검색 엔진 엔드포인트 고정) + (web-domains 정책을 통과한 공개 호스트), SSRF 가드는 그 아래 층에서 그대로.
- **감사**: 모든 차단은 `tool_audit` 이벤트 `domain_blocked`(호스트 해시, 규칙 ID, hop 번호) + `eve_tool_denied_total{tool,reason="domain_denied"}`.
- **테스트**: 정규화 표(대문자, 한글 IDN, 끝 점), `*.x` 경계, deny 우선, 리다이렉트로 차단 도메인 진입, 로드 실패 fail-closed, 모델 입력에 도메인 목록 부재.
- **공수**: 약 1~1.5일.
- **분류**: 빈 denylist 기본값 도입은 동작 변화가 없어 마이너로 볼 여지가 있으나 보안 경계 정책 신설이므로 **메이저 권장**. `allowlist_only` 모드 활성화는 노출 범위 변경이라 **메이저**. 이후 deny 항목 추가는 범위를 좁히는 방향이지만 정책 파일 변경이므로 보안 리뷰 필수(분류는 열린 결정).

## 14. 건영님께 열린 결정
1. M1(web 툴 이전, 툴 집합 불변)을 마이너로 볼지 메이저로 볼지.
2. 감사 로그 쓰기 실패 시 read 툴(web 검색)까지 막을지(완전 fail-closed) 여부.
3. builtin 툴도 게이트웨이와 별도 프로세스/사용자로 분리할지(보안 ↑, 복잡도 ↑).
4. 서드파티 MCP 서버를 허용할지, 직접 만든 어댑터만 쓸지.
5. 노출 툴 상한(3 vs 5) — 모델 엔지니어 평가 결과로 확정.
6. 확인 토큰 만료(120초 제안)와 카드 복원 시 재발급 정책.
7. 이상 탐지 알림 채널(ntfy, 웹 푸시, 텔레그램 등).
8. 플랫폼 이후 첫 신규 툴의 순서.
9. 웹 도메인 정책: 빈 denylist 도입을 메이저(권장)로 할지, 이후 deny 항목 추가를 패치로 허용할지, allowlist_only 모드를 쓸지.
10. 차단 도메인 목록을 UI 설정 화면에서 보여줄지.
