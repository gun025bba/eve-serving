# Issue #140 — 자동 툴 선택 QA: 라벨 세트 · SSRF 코퍼스 (설계: PR #134 rev 2, §6.1 / §6.8 / §8)

범위(1차): (1) 라벨 세트 (2) SSRF 고정 코퍼스. 인젝션 코퍼스·회귀 시나리오·검색 목(mock)·신선도 라이브 런은 설계 머지 후 후속.
게이트웨이/QA 인스턴스에는 접속하지 않는다(전부 in-process, 포트 8000/8001/8002 무접촉).

## 1. 라벨 세트 (`labels/`, `tools/labelset.py`)
- 총 300문항 = A 80 · B 120 · C 50 · D 50 (D 읽기 30 · 쓰기 20). 한국어 중심(ko 267 / en 23 / mix 10 → 영어·혼합 33건 = 11%; 기존 id·범주·라벨·dev/holdout 배정은 그대로 두고 문항 문장만 영어/혼합으로 바꿈).
- 공개(커밋): `labels/dev.jsonl` 210문항(70%), `labels/dev_blind_sheet.jsonl`(정답 제거본, 2차 라벨러용), `labels/manifest.json`(전체 건수 + 홀드아웃 sha256).
- 비공개(커밋 금지): 홀드아웃 90문항(30%, 층화·결정적: 층별 sha256(seed|id) 정렬 후 앞 round(0.3n))은 QA 서버의 비공개 디렉터리(0700/0600, 저장소 밖). 프롬프트를 튜닝하는 ML 엔지니어와 공유 금지.
- 스키마: id, group, category, seed_messages, question, asked_at, lang, expected_first(web|none|calendar_read|calendar_write|either), acceptable_first, also, expected_answer, key_facts, must_not_include, should_abstain, checks, notes, labels{qa,l2,final}, split. (#98 합성셋 형식을 확장)
- 2차 라벨러: 홀드아웃은 보안 봇(지정됨, 불투명 id·셔플된 별도 blind sheet는 저장소 밖에 보관, 미전송). dev는 미정. 반환 전까지 `labels.l2 = null`. 사용법:
  - `python3 tools/labelset.py validate labels/dev.jsonl --manifest labels/manifest.json`
  - `python3 tools/labelset.py adjudicate labels/dev.jsonl <l2.jsonl>`  → 일치율·Cohen kappa·불일치 목록
  - `python3 tools/labelset.py score labels/dev.jsonl <runs.jsonl>`      → §8.3 지표(Wilson 95% CI)와 PASS/FAIL, 결정적 불변식 위반
  - runs.jsonl 한 줄: `{"id":..,"seed":..,"tools":["web_search",...]}` (첫 툴 순서대로)

## 2. SSRF 코퍼스 (`ssrf/`, `go/`)
- `ssrf/ssrf_urls.txt` — **append-only**, 줄당 URL 1개, 전부 차단되어야 함(301건, 17개 범주). 제어문자는 `<TAB><LF><CR><NUL><SP>` 토큰. 범주 헤더의 `(ssrf)`는 목표가 사설/내부 주소·이름이라 `error_code:"ssrf"`가 필요한 범주, `(any)`는 어떤 차단 사유든 허용. `*.stub.example` 호스트 규약은 파일 머리말 참조(리바인딩/리다이렉트 항목은 실제 호스트명·IP 없이 스텁 이름과 비라우팅 대상만 사용). 백엔드 이관 시 `internal/webfetch/testdata/ssrf_urls.txt`로 이동(하니스가 여러 경로 탐색).
- `ssrf/ssrf_allow_urls.txt` — 과차단 대조군(공개 URL 30건, URL 사전검사 통과해야 함).
- 모든 Go 파일은 `//go:build qaharness` 이므로 일반 `go build/vet/test ./...`에는 포함되지 않는다.
- `go/webfetch/qa_ssrf_corpus_test.go` (경로 ①, 직접): 스텁 리졸버 + 실제 `Guard.Control` 미러 + **독립 오라클**이 실제로 도달한 dial을 재검증. 비공개 dial 0, 성공 0이어야 PASS. 기본 포트 / DeniedPorts를 열어 둔 설정 / 허용 대조군 / 비공개 홀드아웃(`EVE_QA_SSRF_HOLDOUT=<파일>`).
- `go/tools/qa_ssrf_tools_test.go` (경로 ②, 모델 경유): 스크립트된 `web_fetch` 호출을 실제 Session에 실행. `TestQA_SSRF_ToolsLayer`는 전부 `status=blocked`(사유 무관)인지와 차단 계층 분포, `TestQA_SSRF_ErrorCodeContract`는 `(ssrf)` 범주 항목의 `error_code=="ssrf"`를 본다. 모드 `EVE_QA_SSRF_ERRCODE=ssrf-items`(기본) | `any` | `off`.
- 확정된 결정(아키텍트): (1) 모든 차단 사유 허용 + SSRF 항목엔 error_code=ssrf 필수(현재 필드가 없어 계약 테스트는 의도적으로 실패, #136 참고); (2) 테스트 빌드에만 노출되는 export 훅 `Options.SetDialIPForTest`; (3) `*.stub.example` 규약 수용.
- **TODO(백엔드)**: 훅 파일 반입 — 제안본 `go/testhook/webfetch_testhook.go.txt`를 `internal/webfetch/testhook.go`(`//go:build qa_testhook`)로. 반입 전 임시 방식(fallback)은 아래 복사 실행.
- 실행(훅 없음, fallback): `cp tests/e2e/issue140/go/webfetch/*_test.go internal/webfetch/ && TMPDIR=<tmp> go test -tags qaharness -count=1 -v -run TestQA_SSRF ./internal/webfetch/` (복사본은 커밋 금지) 및 `go test -tags qaharness -count=1 -v ./tests/e2e/issue140/go/tools/`.
- 실행(훅 반입 후): `go test -tags 'qaharness qa_testhook' -count=1 -v ./tests/e2e/issue140/go/...` (복사 불필요, 경로 ②도 dial 0을 직접 센다).
- 알려진 실패(현재): `kubernetes.default.svc`(`.svc` 접미사 미차단, Low), error_code 계약 157/157 미충족.

## 3. 결정 기록과 남은 사항
- 확정(PM): A/C 규칙 유지 — 최신성을 명시한 문항만 A, 단순 "최신 버전"은 C. 영어·혼합 11% 반영. 홀드아웃 2차 라벨러 = 보안 봇(ML 엔지니어 제외).
- 확정(아키텍트): 위 SSRF (1)~(3).
- 참고: 쓰기 20건 중 2건은 미지원 동사(이동·삭제, 기대 none); D 혼합(캘린더+웹)은 읽기 30건에 포함.
- 참고: 사용자가 입력한 IPv6 `[..]` URL은 `ExtractURLs`가 `[` `]`를 제외해 허용목록에 들어가지 않음(안전 방향).
- 후속(설계 머지 후): 인젝션 코퍼스, 회귀 시나리오, 검색 목, 신선도 라이브 런.
