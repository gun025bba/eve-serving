# Mac Studio Local LLM Gateway

`mlx_lm.server` (Qwen3-30B-A3B-4bit, 127.0.0.1:8080) ← **gateway.py** (FastAPI) ← clients
Gateway binds: `127.0.0.1:8000` (com.llmserving.gateway), `<tailscale-ip>:8000` tailnet only (com.llmserving.gateway-ts).
Note: these are two separate processes — each has its own queue/semaphore and its own metrics.

## Files
| file | purpose |
|---|---|
| gateway.py | gateway (auth, queue, retries, metrics, JSON logs, docs) |
| config.yaml | defaults & tuning (env override: `GW_<KEY>`, e.g. `GW_MAX_CONC=3`, `GW_CORS_ORIGINS=a,b`) |
| gateway.log | JSON lines log (one object per line) |
| examples/client.py, examples/curl.sh | test clients |
| gateway.py.bak-m1 | previous version |

Apply config changes: `launchctl kickstart -k gui/$(id -u)/com.llmserving.gateway` (and `...gateway-ts`).

## API contract
Base URL: `http://127.0.0.1:8000` or `http://<mac-name>.<your-tailnet>.ts.net:8000`. Swagger: `/docs`, schema: `/openapi.json`.
Auth: `Authorization: Bearer <key>` (key in `~/llm-serving/.api_key`). Every response has `X-Request-ID` (client may send its own).

| method | path | auth | description |
|---|---|---|---|
| POST | /v1/chat/completions | yes | OpenAI chat completions; `stream:true` for SSE |
| GET | /v1/models | yes | model list |
| GET | /health | no | liveness (process up) |
| GET | /ready | no | 200/503; checks upstream, model loaded, memory headroom |
| GET | /metrics | no | Prometheus text |
| GET | /stats | no | JSON summary of last `stats_window` requests (p50/p95 latency, queue wait, TTFT, avg tok/s, error rate, memory) |
| GET | /admin/config | yes | effective config (no secrets) |
| POST | /admin/log-level | yes | `{"level":"DEBUG|INFO|WARNING|ERROR"}` runtime change |

Reserved for frontend: `/` (unused) and `/ui` (static files from `static_dir`, default `~/llm-serving/web`, mounted only if it exists).
CORS: off by default; set `cors_origins: ["http://localhost:5173"]` to allow a browser app (Authorization header allowed, X-Request-ID exposed).

### Request defaults
If omitted: `model`, `max_tokens` (1024), `temperature` (0.7), `top_p` (0.9) from config.yaml. Qwen3 thinks by default; append `/no_think` to the user message to skip reasoning.

### Errors (OpenAI format)
```json
{"error":{"message":"...","type":"rate_limit_error","code":"queue_full","request_id":"ab12..."}}
```
| HTTP | code | when |
|---|---|---|
| 400 | bad_request | invalid body / invalid params |
| 401 | invalid_api_key | missing/wrong key |
| 429 | queue_full | more than `max_queue` waiting — retry with backoff |
| 429 | queue_timeout | waited > `queue_timeout_s` for a slot |
| 502 | upstream_error | model server returned 5xx / garbage |
| 503 | upstream_unavailable | model server unreachable after `retries` connection retries |
| 504 | upstream_timeout | no bytes from model for `read_timeout_s` |
| 500 | internal_error | gateway bug |
Retries happen only for connection errors before generation starts; never mid-stream.

### Streaming (SSE)
`Content-Type: text/event-stream`. Each event: `data: <chat.completion.chunk JSON>\n\n`; text is in `choices[0].delta.content`.
Last data chunk carries `usage` (gateway sets `stream_options.include_usage=true` unless provided); stream ends with `data: [DONE]`.
If the stream fails after it started (HTTP already 200), the gateway emits `data: {"error":{...}}` then `data: [DONE]` — clients should check for an `error` key in each event.
Errors before streaming begins are normal HTTP JSON errors (table above).

## Observability
JSON log line per request (`tail -f ~/llm-serving/gateway.log`): `request_id, stream, status, error_code, queue_ms, ttft_ms, total_ms, prompt_tokens, completion_tokens, tokens_per_s, mlx_rss_gb, mem_avail_gb, queue_depth, inflight`.
Pretty: `tail -f gateway.log | jq -c '{request_id,status,total_ms,tokens_per_s}'`.

Prometheus metrics (`curl -s localhost:8000/metrics | grep ^llm_`):
`llm_requests_total{endpoint,status}`, `llm_errors_total{code}`, `llm_request_latency_seconds{stream}`, `llm_queue_wait_seconds`,
`llm_time_to_first_token_seconds`, `llm_tokens_per_second`, `llm_prompt_tokens_total`, `llm_completion_tokens_total`,
`llm_inflight_requests`, `llm_queue_depth`, `llm_system_memory_bytes{kind}`, `llm_mlx_server_rss_bytes`.
Non-stream tok/s = completion tokens / upstream time (includes prefill); stream tok/s = tokens / time after first token.

## Test
`.venv/bin/python examples/client.py [--base http://<tailscale-ip>:8000]` or `examples/curl.sh [base]`.
