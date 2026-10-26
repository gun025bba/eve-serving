# Eve — Local LLM Gateway

Service name: **Eve** (repo `gun025bba/eve-serving`). On the server the clone directory (`~/llm-serving`), launchd labels (`com.llmserving.*`) and binary paths keep their original names.

`mlx_lm.server` (Qwen3-30B-A3B-4bit, 127.0.0.1:8080) ← **gateway** (Go, `cmd/gateway`; legacy Python `gateway.py` kept for rollback) ← clients
Gateway binds: `127.0.0.1:8000` (com.llmserving.gateway), `<tailscale-ip>:8000` tailnet only (com.llmserving.gateway-ts).
Note: these are two separate processes — each has its own queue/semaphore and its own metrics.

## Files
| file | purpose |
|---|---|
| cmd/gateway, internal/* | Go gateway (see *Go gateway* below) |
| gateway.py | legacy Python/FastAPI gateway (same API; kept for rollback) |
| config.yaml | defaults & tuning (env override: `GW_<KEY>`, e.g. `GW_MAX_CONC=3`, `GW_CORS_ORIGINS=a,b`) |
| gateway.log | JSON lines log (one object per line) |
| examples/client.py, examples/curl.sh | test clients |
| gateway.py.bak-m1 | previous version |

Apply config changes: `launchctl kickstart -k gui/$(id -u)/com.llmserving.gateway` (and `...gateway-ts`).

## API contract
Base URL: `http://127.0.0.1:8000` or `http://<host>.<your-tailnet>.ts.net:8000`. Swagger: `/docs`, schema: `/openapi.json`.
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
| GET/POST | /v1/threads | yes | list (keyset cursor) / create threads |
| GET/PATCH/DELETE | /v1/threads/{id} | yes | get / rename+archive / archive |
| GET | /v1/threads/{id}/messages | yes | messages, `before=<id>` pagination |
| POST | /v1/threads/import | yes | idempotent bulk import (client_id) |
| PUT/DELETE | /v1/messages/{id}/feedback | yes | 👍/👎 + comment |
| GET | /v1/search | yes | title + content search (FTS + pg_trgm), `<mark>` snippets |

Conversation storage: `thread_id` / `replace_message_id` on `/v1/chat/completions` and the endpoints above are
specified in **[docs/threads-api.md](docs/threads-api.md)** (schema: [docs/db-model.md](docs/db-model.md)).

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
| 401 | unauthenticated | no credentials (no key, no trusted tailnet identity) |
| 401 | invalid_api_key | wrong Bearer key |
| 401 | identity_not_allowed | tailnet login not in `allowed_users` |
| 429 | rate_limited | per-user rate-limit hook denied |
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
JSON log line per request (`tail -f ~/llm-serving/gateway.log`): `request_id, user, auth_provider, stream, status, error_code, queue_ms, ttft_ms, total_ms, prompt_tokens, completion_tokens, tokens_per_s, mlx_rss_gb, mem_avail_gb, queue_depth, inflight`.
Pretty: `tail -f gateway.log | jq -c '{request_id,status,total_ms,tokens_per_s}'`.

Prometheus metrics (`curl -s localhost:8000/metrics | grep ^llm_`):
`llm_requests_total{endpoint,status}`, `llm_errors_total{code}`, `llm_auth_total{auth_method,result}`, `llm_request_latency_seconds{stream}`, `llm_queue_wait_seconds`,
`llm_time_to_first_token_seconds`, `llm_tokens_per_second`, `llm_prompt_tokens_total`, `llm_completion_tokens_total`,
`llm_inflight_requests`, `llm_queue_depth`, `llm_system_memory_bytes{kind}`, `llm_mlx_server_rss_bytes`.
Non-stream tok/s = completion tokens / upstream time (includes prefill); stream tok/s = tokens / time after first token.

## Auth
Every protected route (`/v1/*`, `/admin/*`, `/whoami`) goes through one middleware that runs the
provider chain from `auth.providers` (env `GW_AUTH_PROVIDERS=a,b`), first success wins:

| provider | credential | identity (`user`) |
|---|---|---|
| `tailscale-identity` | `Tailscale-User-Login` header set by `tailscale serve`. Trusted **only** if the TCP peer is loopback (`127.0.0.1`/`::1`) and the login is in `allowed_users` (env `GW_ALLOWED_USERS=a@github,b@example.com`, case-insensitive). Empty allowlist = provider disabled. The header is stripped from non-loopback requests. | the login |
| `api-key` | `Authorization: Bearer <key>` (`.api_key`) — scripts / server-to-server | `api-key` |

Default `providers: [tailscale-identity, api-key]`. Handlers only read `auth.FromContext(ctx)` → `Identity{User, Provider}`.
Every authenticated request logs `user` and `auth_provider` (never the key); metric `llm_auth_total{auth_method,result}`.
A per-user `auth.RateLimiter` hook (keyed by `Identity.User`, default no-op) runs after auth; deny → `429 rate_limited`.

`GET /whoami` (alias `/v1/me`) → `{"authenticated":true,"user":"…","provider":"tailscale-identity|api-key"}`; the UI calls it
without a key to detect identity mode. 401 codes: `unauthenticated` (no credentials), `invalid_api_key` (wrong Bearer),
`identity_not_allowed` (tailnet login not in allowlist).

Security note: any local process on the server can send the header to `127.0.0.1:8000`; that is the accepted trust
boundary (same as the key file being readable by the user). Never bind the gateway to a non-loopback address
with `tailscale-identity` enabled unless you accept that only the API key protects that listener (the header is ignored there).

### Deployment (loopback + tailscale serve)
```bash
cd ~/llm-serving && git pull --ff-only && make build
ALLOWED_USERS=<login>@github deploy/install-launchd-go.sh     # loopback-only: gateway on 127.0.0.1:8000, -ts plist -> .prev
launchctl bootout gui/$(id -u)/com.llmserving.gateway-ts        # stop the tailnet-IP listener
launchctl bootout gui/$(id -u)/com.llmserving.gateway; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
tailscale serve --bg --https=443 http://127.0.0.1:8000          # tailnet-only HTTPS (NOT funnel)
tailscale serve status
curl -s https://<host>.<your-tailnet>.ts.net/whoami             # from another tailnet device -> provider tailscale-identity
```
Access URL: `https://<host>.<your-tailnet>.ts.net/ui/`. Scripts keep working with the key against `http://127.0.0.1:8000`
or the HTTPS URL. Requires MagicDNS + HTTPS certificates enabled in the Tailscale admin console.

Rollback:
```bash
tailscale serve --https=443 off           # or: tailscale serve reset
mv ~/Library/LaunchAgents/com.llmserving.gateway.plist.prev    ~/Library/LaunchAgents/com.llmserving.gateway.plist
mv ~/Library/LaunchAgents/com.llmserving.gateway-ts.plist.prev ~/Library/LaunchAgents/com.llmserving.gateway-ts.plist
launchctl bootout gui/$(id -u)/com.llmserving.gateway; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway-ts.plist
```
Code-only rollback: `GW_AUTH_PROVIDERS=api-key` restores key-only behaviour; or check out the previous binary
(`git checkout <prev-commit> && make build`).

### Adding an auth provider
1. Create `internal/auth/<name>.go` implementing `Authenticator`; return `ErrNoCredentials` when the request carries nothing
   for you (chain moves on), another error to reject, or an `Identity` on success.
2. Register it in `init()`; add needed inputs to `auth.Options` (filled in `cmd/gateway/main.go` from config).
3. Add the name to `auth.providers` in config (order = precedence). Add tests next to `auth_test.go`.
```go
package auth

import "net/http"

func init() {
	Register("session-cookie", func(o Options) (Authenticator, error) { return cookie{}, nil })
}

type cookie struct{}

func (cookie) Authenticate(r *http.Request) (Identity, error) {
	c, err := r.Cookie("sid")
	if err != nil {
		return Identity{}, ErrNoCredentials
	}
	user, ok := lookupSession(c.Value) // your store
	if !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: user, Provider: "session-cookie"}, nil
}
```

## Go gateway
Layout:
```
cmd/gateway/main.go      flags (-base, -config, -key-file, -host, -port), wiring, graceful shutdown
internal/config          config.yaml + GW_<KEY> env overrides
internal/auth            provider registry + chain (tailscale.go, apikey.go), Identity ctx, RateLimiter hook
internal/apierr          OpenAI-style error envelope / codes
internal/queue           semaphore (max_conc) + bounded queue (max_queue, queue_timeout_s)
internal/proxy           upstream client: connect timeout, dial-only retries w/ backoff, idle read timeout
internal/metrics         Prometheus metrics (same llm_* names), /stats window, memory sampling
internal/logging         JSON-lines logger with runtime level
internal/handlers        routes, SSE passthrough, /ui static, CORS, /docs + embedded openapi.json,
                         threads.go (threads API), persist.go (chat -> thread persistence)
internal/store           persistence interfaces (Thread/Message/Feedback/SearchStore), snippets
internal/store/postgres  pgx/pgxpool implementation + embedded goose migrations (migrations/*.sql)
internal/store/memstore  in-memory implementation for handler tests
deploy/launchd-go        launchd template for the Go binary; deploy/install-launchd-go.sh renders it
```
Build / test (Go ≥ 1.26):
```bash
make test            # go test ./...
make build           # -> bin/gateway (static, no venv)
./bin/gateway -host 127.0.0.1 -port 8001     # reads ~/llm-serving/config.yaml and .api_key
```
Flags also via env: `GW_BASE`, `GW_CONFIG`, `GW_KEY_FILE`, `GW_HOST`, `GW_PORT`. `bin/gateway -version` prints the git version.
Differences vs Python: invalid JSON / non-object bodies return `400 bad_request` (FastAPI returned 422); `/metrics` exposes `go_*`/`process_*` runtime metrics instead of `python_*`; `/docs` loads Swagger UI from jsDelivr like FastAPI did.

### Cutover to Go (launchd)
```bash
cd ~/llm-serving && git pull --ff-only && make build
BIND_IP=<tailscale-ip> deploy/install-launchd-go.sh     # rewrites com.llmserving.gateway{,-ts}.plist, backs up *.py-bak
launchctl bootout gui/$(id -u)/com.llmserving.gateway;    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
launchctl bootout gui/$(id -u)/com.llmserving.gateway-ts; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway-ts.plist
curl -s localhost:8000/health
```
Rollback: `for l in gateway gateway-ts; do mv ~/Library/LaunchAgents/com.llmserving.$l.plist.py-bak ~/Library/LaunchAgents/com.llmserving.$l.plist; done`, then the same bootout/bootstrap pair.
Updating after cutover: `git pull --ff-only && make build && launchctl kickstart -k gui/$(id -u)/com.llmserving.gateway` (and `-ts`).

## Conversation storage (PostgreSQL)
- PostgreSQL 18 + pgvector via Homebrew, managed by `brew services` (launchd), **loopback only**
  (`listen_addresses='localhost'`), peer auth on the unix socket, scram-sha-256 on 127.0.0.1/::1.
- One-time setup (idempotent): `brew install postgresql@18 pgvector && deploy/pg-setup.sh`
  → role `llmchat` (random password), DBs `llmchat` + `llmchat_test`, extensions `pg_trgm` + `vector`,
  and `~/llm-serving/.db_env` (chmod 600, gitignored) with `GW_DATABASE_URL` / `GW_TEST_DATABASE_URL`.
  The password only lives in `.db_env` and the rendered (chmod 600) gateway plist; never commit or print it.
- The gateway reads `GW_DATABASE_URL` (or `database.url`); empty = persistence disabled (thread endpoints → 503
  `persistence_disabled`, chat unaffected). Migrations (goose, embedded) run at startup unless
  `GW_DB_AUTO_MIGRATE=false`; manually: `make migrate` (`bin/gateway -migrate`).
- Adding a migration: new `internal/store/postgres/migrations/000NN_name.sql` with `-- +goose Up/Down`.
  Moving to another DB = new package implementing `store.Store`; handlers only see the interfaces.
- Tests: `make test-race` (unit, in-memory store) and `make test-integration` (real Postgres, TRUNCATEs `llmchat_test`).

### Backups
- `deploy/pg-backup.sh`: `pg_dump -Fc` of `llmchat` → `~/llm-serving/backups/llmchat-YYYYmmdd-HHMMSS.dump`
  (chmod 600, gitignored), deletes dumps older than 14 days (`RETENTION_DAYS`). Uses peer auth (no password).
- Schedule: `deploy/install-pgbackup.sh` installs/reloads LaunchAgent `com.llmserving.pgbackup` (daily 04:00,
  log `pgbackup.launchd.log`). Run now: `launchctl kickstart gui/$(id -u)/com.llmserving.pgbackup`.
- Restore (PATH needs `/opt/homebrew/opt/postgresql@18/bin`):
  ```bash
  launchctl bootout gui/$(id -u)/com.llmserving.gateway          # stop writers
  pg_restore --clean --if-exists --no-owner --role=llmchat -d llmchat ~/llm-serving/backups/llmchat-<ts>.dump
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
  ```
  To inspect first: `createdb llmchat_restore && pg_restore --no-owner -d llmchat_restore <dump>` (then `dropdb`).
  Off-host copies: copy `backups/` elsewhere (e.g. external disk); dumps contain all conversations.

### Enabling persistence on the live gateway
```bash
cd ~/llm-serving && git pull --ff-only && make build
ALLOWED_USERS=<login> deploy/install-launchd-go.sh   # adds GW_DATABASE_URL from .db_env; previous plist -> *.plist.prev
launchctl bootout gui/$(id -u)/com.llmserving.gateway; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
curl -s localhost:8000/health
```
Rollback: restore `~/Library/LaunchAgents/com.llmserving.gateway.plist.prev` (no DB env) and bootout/bootstrap; or
keep the plist and `git checkout <prev-commit> && make build && launchctl kickstart -k gui/$(id -u)/com.llmserving.gateway`.
The database is untouched by either rollback.

## Test
`.venv/bin/python examples/client.py [--base http://<tailscale-ip>:8000]` or `examples/curl.sh [base]`.

## Deploy (server)
```bash
git clone https://github.com/gun025bba/eve-serving.git ~/llm-serving   # 최초 1회
cd ~/llm-serving && python3 -m venv .venv && .venv/bin/pip install fastapi uvicorn httpx pyyaml prometheus-client mlx-lm openai
echo '<your-key>' > .api_key && chmod 600 .api_key            # 커밋 안 됨
BIND_IP=<tailscale-ip> deploy/install-launchd.sh               # macOS: launchd 템플릿 렌더
```
업데이트: `cd ~/llm-serving && git pull --ff-only` → gateway.py/config.yaml이 바뀐 경우에만
`launchctl kickstart -k gui/$(id -u)/com.llmserving.gateway` (및 `-ts`).
호스트별 값(바인드 IP 등)은 커밋하지 말고 env(`GW_*`)나 launchd 템플릿 렌더 시 주입하세요.
무시되는 로컬 파일: `.api_key`, `*.log`, `.venv/`, `web/`(eve-chat-ui 배포 사본), `config.local.yaml`.

## Workflow
- GitHub(`gun025bba/eve-serving`)가 단일 진실의 원천입니다. 수정은 clone에서 → commit → push.
- 서버/개발 환경에서는 `git pull --ff-only`만. 서버에서 직접 수정하지 마세요. force-push 금지.
- 실제 호스트명·IP·키·로그는 커밋 금지 (`<host>`, `<tailscale-ip>` 플레이스홀더 사용).
