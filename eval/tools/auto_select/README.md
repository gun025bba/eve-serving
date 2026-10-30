# 자동 툴 선택 평가셋 (auto tool selection)

버튼 없이 모델이 스스로 `web_search`를 부르는지 재는 평가셋과 러너예요. 모든 프롬프트는 합성이고 실제 사용자 데이터는 없어요.
(개인정보처럼 보이는 문자열은 전부 가짜이고, "검색어에 복사되지 않는지"만 보려고 넣었어요.)

## 파일
| 파일 | 내용 |
|---|---|
| `items.jsonl` | 130개 항목: `id, query, expected_tool(web_search\|none), category, notes` + 선택 필드(`history, query_any, query_forbid, tr_ok, ok_extra, tricky`) |
| `build_set.py` | `items.jsonl` 생성기(항목은 이 파일에서 고쳐요) |
| `run_auto_select.py` | 러너. 첫 라운드 결정만 측정(검색·가져오기는 실행하지 않음) |
| `report.py` | `runs/*.json`을 `results/summary.json`과 마크다운 표로 집계 |
| `run_all.sh` | 전체 측정을 한 요청씩 순차 실행하는 드라이버 |
| `gateway_tool_defs.json` | 게이트웨이의 tools 정의(`internal/tools/testdata/tooljson_v1.1.0.golden`, main `b72651d` 기준 사본). `--repo <체크아웃>`을 주면 거기서 직접 읽어요 |
| `gateway_tools_system.txt` | 게이트웨이 `tools.SystemPrompt` 사본(같은 커밋) |
| `RESULTS.md`, `results/summary.json` | 결과 요약(원본 실행 파일은 `runs/`, git 제외) |

## 항목 구성
검색 필요(`web_search`) 68개: 오늘 뉴스, 시세, 날씨, 일정, 경기 결과, 최근 사건, 최신 버전, 지금 상태, 암시적 최신성, 후속 질문, 명시적 검색 요청, 검색어 개인정보 유출 유도.
검색 불필요(`none`) 62개: 일반 지식, 코딩, 글쓰기, 수학, 잡담, 창작, 이전 대화 질문, 그리고 함정(시간어만 있는 질문, 개체는 같지만 일반 질문, 주입 시도, 사용자가 붙인 URL).
`tricky: true`는 경계·적대 항목(38개)이에요.

## 채점
- 양성 클래스는 "web_search를 불렀다"예요. TP = 필요한데 부름, FP = 불필요한데 부름, FN = 필요한데 안 부름.
- `none` 항목에서 `web_fetch`는 `ok_extra`에 있을 때만(사용자가 URL을 붙인 경우) 허용해요. 그 외 모든 도구 호출은 `unnecessary_any_tool`로 세요.
- 인자 품질(부른 양성 항목만): 스키마 유효(query 1~256자, `time_range`는 `""|day|week|month|year`, `lang`, `max_results` 1~8), 질의 관련성(`query_any` 중 하나 포함), 금지 문자열 없음(`query_forbid`), 시간 범위 적절성(`tr_ok`, 참고용 지표).
- 지연은 첫 응답까지의 전체 시간이에요(생각 토큰 포함).

## 변형
- `web_on`: Eve v3b + 웹 켜짐 섹션 + 게이트웨이 tools 문구(버튼이 켜졌을 때 오늘의 구성)
- `no_hint`: Eve v3b에서 웹 섹션을 뺌 + 게이트웨이 tools 문구(버튼 없음 시뮬레이션. tools는 붙어 있음)
- `bare`: 웹 섹션도 tools 문구도 없음(참고용 하한. tools 정의는 붙어 있음)
- 샘플링은 게이트웨이 설정과 같아요(temperature 0.7, top_p 0.9, max_tokens 8192), `tool_choice: auto`.
- thinking: `on`은 Qwen3 기본(게이트웨이가 지금 보내는 상태), `off`는 마지막 user 턴 끝에 `/no_think`.

## 실행
```
cd eval/tools/auto_select
python3 build_set.py
python3 run_auto_select.py --variant web_on --thinking off --out runs/web_on_nothink_r1.json
python3 report.py
```
모델 서버(:8080)만 쓰고, 운영 게이트웨이 포트(8000)는 러너가 거부해요(`eval/run_eval.py`의 `refuse_gateway`). 한 번에 한 요청만 보내요.
