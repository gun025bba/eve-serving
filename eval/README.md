# Korean eval suite (v1)

고정 한국어 평가셋과 회귀 테스트로 서빙 모델의 품질·속도·잘림(truncation)을 비교합니다.

## 파일

| 경로 | 설명 |
|------|------|
| `build_sets.py` | 시드 고정 생성기. JSONL을 직접 고치지 말고 이 파일을 수정한 뒤 재생성하세요. |
| `data/ko_eval_v1.jsonl` | 고정 평가셋 (~200–300문항) |
| `data/regression_v1.jsonl` | 규칙 자동 채점 회귀셋 (~30–50문항) |
| `run_eval.py` | OpenAI 호환 `/v1/chat/completions` 러너 (요청 1건씩) |

카테고리: `chat`, `knowledge`, `reasoning_math`, `coding`, `writing`, `instruction`, `safety`, `long_context`.

## 재생성

```bash
python3 eval/build_sets.py
```

## 실행

엔드포인트와 키는 환경변수로만 넘깁니다. **실제 호스트·키·기기명을 커밋하지 마세요.**

```bash
export EVAL_BASE_URL="http://127.0.0.1:8000"   # 게이트웨이 base (trailing slash 없이)
export EVAL_API_KEY="..."                      # 또는 LLM_API_KEY, 또는 ~/llm-serving/.api_key
export EVAL_MODEL="mlx-community/Qwen3-30B-A3B-4bit"

# 회귀만 (빠름)
python3 eval/run_eval.py --suite regression --out eval/results/regression.json

# 전체 평가셋
python3 eval/run_eval.py --suite eval --out eval/results/ko_eval.json

# 둘 다
python3 eval/run_eval.py --suite both --out eval/results/full.json
```

옵션:

- `--limit N` — 스위트당 상위 N문항만
- `--sleep S` — 요청 사이 대기(초). 기본 0, 여전히 **직렬** 호출
- `--offset N` — 앞에서 N문항 건너뛰기(중단 후 재개). 연결 오류는 최대 3회 재시도
- `--append-no-think 1` — Qwen3 thinking 생략(기본 1). 프롬프트에 `/no_think` 추가

표준 라이브러리만 사용합니다 (`urllib`).

## 집계 지표

카테고리별·전체:

- 통과율 (`pass_rate`, 규칙 채점 가능 문항)
- 평균 지연(`latency_avg_s`), 대략적 토큰/초(`completion_tokens / latency`)
- **잘림 비율** (`truncated_rate`: `finish_reason == "length"`)

루브릭만 있는 서술형은 `score=null`로 두고 자동 통과율에서 제외합니다. 사람/판정 모델 채점은 후속 작업입니다.

## 서버에서 기준선 측정 (읽기 전용)

설정·모델 교체 없이 추론 호출만 합니다.

```bash
cd ~/llm-serving
git pull
# 필요 시 이 브랜치/PR을 체크아웃

export EVAL_BASE_URL="http://127.0.0.1:8000"
# 키는 ~/llm-serving/.api_key 자동 사용

mkdir -p eval/results
python3 eval/run_eval.py --suite regression --out eval/results/baseline_regression.json
python3 eval/run_eval.py --suite eval --out eval/results/baseline_ko_eval.json
```

`eval/results/`는 `.gitignore`에 넣어 결과·로그가 실수로 커밋되지 않게 합니다.
