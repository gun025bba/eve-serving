# Issue #140 — 자동 툴 선택 QA: 라벨 세트 · SSRF 코퍼스 (설계: PR #134 rev 2, §6.1 / §6.8 / §8)

범위(1차): (1) 라벨 세트 (2) SSRF 고정 코퍼스. 인젝션 코퍼스·회귀 시나리오·검색 목(mock)·신선도 라이브 런은 설계 머지 후 후속.
게이트웨이/QA 인스턴스에는 접속하지 않는다(전부 in-process, 포트 8000/8001/8002 무접촉).

## 1. 라벨 세트 (`labels/`, `tools/labelset.py`)
- 총 300문항 = A 80 · B 120 · C 50 · D 50 (D 읽기 30 · 쓰기 20). 한국어 중심(ko 290 / en 7 / mix 3).
- 공개(커밋): `labels/dev.jsonl` 210문항(70%), `labels/dev_blind_sheet.jsonl`(정답 제거본, 2차 라벨러용), `labels/manifest.json`(전체 건수 + 홀드아웃 sha256).
- 비공개(커밋 금지): 홀드아웃 90문항(30%, 층화·결정적: 층별 sha256(seed|id) 정렬 후 앞 round(0.3n))은 QA 서버의 비공개 디렉터리(0700/0600, 저장소 밖). 프롬프트를 튜닝하는 ML 엔지니어와 공유 금지.
- 스키마: id, group, category, seed_messages, question, asked_at, lang, expected_first(web|none|calendar_read|calendar_write|either), acceptable_first, also, expected_answer, key_facts, must_not_include, should_abstain, checks, notes, labels{qa,l2,final}, split. (#98 합성셋 형식을 확장)
- 2차 라벨러 전: `labels.l2 = null`. 사용법:
  - `python3 tools/labelset.py validate labels/dev.jsonl --manifest labels/manifest.json`
  - `python3 tools/labelset.py adjudicate labels/dev.jsonl <l2.jsonl>`  → 일치율·Cohen kappa·불일치 목록
  - `python3 tools/labelset.py score labels/dev.jsonl <runs.jsonl>`      → §8.3 지표(Wilson 95% CI)와 PASS/FAIL, 결정적 불변식 위반
  - runs.jsonl 한 줄: `{"id":..,"seed":..,"tools":["web_search",...]}` (첫 툴 순서대로)

## 2. SSRF 코퍼스 (`ssrf/`, `go/`)
- `ssrf/ssrf_urls.txt` — **append-only**, 줄당 URL 1개, 전부 차단되어야 함(301건, 16개 범주). 제어문자는 `<TAB><LF><CR><NUL><SP>` 토큰. `*.stub.example` 호스트 규약은 파일 머리말 참조. 백엔드 이관 시 `internal/webfetch/testdata/ssrf_urls.txt`로 이동(하니스가 두 경로 모두 탐색).
- `ssrf/ssrf_allow_urls.txt` — 과차단 대조군(공개 URL 30건, URL 사전검사 통과해야 함).
- `go/qa_ssrf_corpus_test.go` (package `webfetch`로 복사해 실행; 비공개 `dialIP` 훅 필요): 스텁 리졸버 + 실제 `Guard.Control` 미러 + **독립 오라클**이 실제로 도달한 dial을 재검증. 비공개 dial 0, 성공 0이어야 PASS. 기본 포트 / DeniedPorts를 열어 둔 설정 / 허용 대조군 / 비공개 홀드아웃(`EVE_QA_SSRF_HOLDOUT=<파일>`).
- `go/qa_ssrf_tools_test.go` (package `tools`로 복사): 스크립트된 `web_fetch` 호출을 실제 Session(도메인 정책+허용목록+Fetcher)에 실행, 어느 계층이 막았는지·BlockReason 분포 기록.
- 실행: `cp tests/e2e/issue140/go/qa_ssrf_corpus_test.go internal/webfetch/; cp tests/e2e/issue140/go/qa_ssrf_tools_test.go internal/tools/; TMPDIR=<tmp> go test -count=1 -v -run TestQA_SSRF ./internal/webfetch/ ./internal/tools/` (복사본은 커밋하지 말 것)

## 3. 판단·미해결(PM/아키텍트 결정 필요)
1. §6.8 기대값 `error_code:"ssrf"`: 현재 구조에선 대부분 도메인 정책(ip_literal*, invalid_host)·허용목록·host/scheme/port/url 사유가 먼저 막는다(도구층 278건 중 reason==ssrf 10건). SSE tool_result에는 `status`만 있고 BlockReason은 JSON에 없음. → "모든 차단 사유 인정" vs "ssrf만 인정" 결정 + error_code 노출 필요.
2. §6.8 "dial 0"(경로②)은 비공개 `dialIP` 훅이 필요(패키지 외부 테스트 불가) → 내보낸 테스트 훅 또는 같은 패키지 하니스 유지.
3. 한 줄 URL 코퍼스는 리바인딩/리다이렉트를 표현 못 함 → `*.stub.example` 이름 규약으로 대체(수용 가능 여부).
4. A/C 경계: "최신 버전"이 A 목록에 있으나 "파이썬 최신 버전"은 C 예시. 명시적 최신성 표현(오늘 기준/방금/이번 주)은 A, 단순 "최신 버전"은 C로 분류.
5. 2차 라벨러 지정(홀드아웃은 프롬프트 튜닝 담당 ML 엔지니어 불가). 쓰기 20건 중 2건은 미지원 동사(이동·삭제, 기대 none). D 혼합(캘린더+웹)은 읽기 30건에 포함. 영어/혼합 비중(3%) 확대 여부.
6. 사용자가 입력한 IPv6 `[..]` URL은 ExtractURLs가 `[` `]`를 제외하므로 허용목록에 절대 안 들어감(안전 방향, 인지용).
