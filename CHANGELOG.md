# Changelog

## 5.1.0 (unreleased)

> 캘린더 연동·아침 브리핑(M1). Everything is off by default. Switches and enabling order:
> `docs/runbooks/5.1.0-calendar-briefing.md`.

### Fixed
- **`allowed_users` fails closed (#131).** An empty allowlist already admits nobody; this is now pinned by tests
  (a valid paired session is refused and does not fall through to the API key, `eve-gw pair` refuses every login).
  A comma- or whitespace-joined login (`a@x, b@y`) is never a login: such an entry in `allowed_users` /
  `GW_ALLOWED_USERS` (YAML item or space-separated list) now makes the gateway refuse to start with a clear error
  instead of silently allowlisting nothing, and `ValidLogin` rejects it in `AllowedLogin` and the session check.
  `tailscale-identity` itself was removed in 4.0.0, so there is no header path left to harden.

### Changed (security review follow-up, Low findings)
- `calendar_add_event` now **requires `calendar_id`** (from `calendar_list_events`); the confirmation card always
  names the target calendar. There is no implicit default calendar. (L-1)
- Text fields refuse U+2028/U+2029 in addition to control and format characters; the helper applies the same rule. (L-2)
- Titles in the model's calendar/reminder results and in the briefing are cut to 200 characters. (L-3)
- The helper gives up on a request that has not fully arrived 3 s after connecting and backs off on accept errors. (L-5)
- New cleanup: event/reminder text left in `pending_actions` of finished or expired pim write cards is blanked 24 h
  after they ended (job every 6 h; hash and status stay). (I-2)
- Docs: `GW_PIM_WRITES_ENABLED` and the four other keys, defaults and the external-sync warning for writes.

## 5.0.2 (unreleased)

### Fixed
- **Approving a memory add card on Postgres returned 422 `payload_mismatch`.** JSONB stores object keys in its own
  order, so the hash computed over the producer's key order never matched the stored payload. `payload_hash` is now
  over the canonical (sorted-key) JSON, used at create and verify. Approve also accepts the pre-5.0.2 hash; a pending
  card whose key order JSONB had already changed cannot verify and expires within 24h (propose again). New Postgres
  test `TestPostgresActions_PayloadHashSurvivesJSONB` (required by CI).

### Added
- **Eve self-description in chat requests of interactive sessions.** The gateway puts a short Korean block at the start of the system
  prompt (before client system text; the memory block stays last): the build version, this user's memory state
  (on, off, on but the first-use intro not seen yet, or unavailable) with how to view, add, edit, delete and export
  memories (`설정 > 메모리`, `/v1/memory`) and how proposal cards work, and the list of tools that are enabled
  (web search with its state for this request, `memory_propose`). It is generated from runtime state
  (`internal/selfdesc`, `internal/handlers/selfdesc.go`), so a feature that is off is not mentioned and a new tool
  appears when its entry is added to the list. Per request, not stored in threads. Budget: under 200 tokens.
  Only requests with the `interactive` scope get it (browser/app sessions); API-key requests are unchanged.
- `memory_propose` is now offered whenever memory is active, also when web search is off for the request (and when
  `GW_WEB_ENABLED=false`). In that case it is the only tool; a web tool call is refused (`tool_unavailable`) and never
  executed. Taint checks are unchanged and now look at the raw request history.
- With web off and `stream: true` the memory loop streams: text reaches the client as it is generated and the gateway
  only buffers once the model starts a tool call (native or textual), so a memory-active request is not held back by tool handling. Measured first token with memory on is about 1.15s versus 0.18s with memory off (a flat difference, not a regression of the streaming path).

### Security
- Without a server-side thread (no `thread_id`), `memory_propose` is not offered when the request carries earlier
  assistant turns (they could hold web content the gateway cannot check). Documented in `docs/architecture/memory.md`.
- The same rule applies to `thread_id: "new"`: the thread is created by that request, so assistant turns in its body are
  client-supplied and do not count as trusted.
- A negative `index` in a streamed tool-call delta is ignored instead of panicking (500).

### Changed
- Requests of a user with active memory take the tool path even with web off (streamed, see above; non-streaming
  clients get the answer after the tool round, as with web search on).

## 5.0.0 (unreleased)

> **Breaking (MAJOR): personalized memory is on by default.** Stacked on 4.0.0. Contract: #54 (memory.md §14,
> ui-api-contract §3.9.7). Runbook and rollback: `docs/runbooks/5.0.0-memory-default-on.md`.

### Changed
- Server `memory.enabled` / `GW_MEMORY_ENABLED` defaults to `true`. Resolution is "off wins": either source set to
  `false` turns memory off. Values must be exactly `true`/`false`; anything else (empty, `on`, `yes`, `!!str false`)
  is a startup error. One startup log line records `memory_enabled` and its source.
- Per-user `memory.enabled`: a missing key now reads `true` (opt-out). An explicit `false` stays off. No migration:
  existing rows are not rewritten, so rolling back the binary restores the old default.
- Until the user has seen the one-time intro (`ui.memory_intro_seen`), nothing is injected into prompts and the
  `memory.propose` tool is not offered. Viewing, editing, deleting and exporting work before that.

### Security
- The API-key (Bearer) virtual user has memory fixed off (no injection, no tool).
- Browser-session only (Bearer gets `403 scope_denied`): `POST /v1/actions/{id}/approve`, `PATCH /v1/settings`,
  `GET`/`DELETE /v1/memory`, `GET /v1/memory/export`, `POST /v1/memory/items`, `PATCH`/`DELETE /v1/memory/items/{id}`,
  in addition to the 4.0.0 list (`GET /v1/actions`, reissue, revise, `DELETE /v1/session`, `/v1/push/*`).
- `TestRouteAuthMatrix` compares, as a set, every route registered anywhere in `internal/handlers` (incl.
  `memory.go`) with the Bearer / session / unauthenticated matrix.
- Pairing returns its reserved guess slot via `defer`, so a store panic cannot leak it.
- Config loading rejects unknown YAML keys (`KnownFields`); the repo `config.yaml` is tested to load strictly.

### Added
- Setting `ui.memory_intro_seen` (bool, default false; PATCH from a browser session only).
- **Deploys no longer need a sudo password (optional step the host owner must approve and run himself).** Changes a
  privilege boundary: one root-owned entrypoint may run via sudo without a password, only for releases signed with a
  pinned key. Install, removal and design: `deploy/nopasswd/README.md`.

## 4.0.0 (unreleased)

> **Breaking: every existing browser must re-pair.** After upgrading, browsers that used 3.x get `401` until they
> pair once: run `gateway pair --user <login>` (or `eve-gw pair --user <login>`) on the host, then enter the printed
> `XXXX-XXXX` code in the UI within 10 minutes. Scripts keep working with `Authorization: Bearer <api key>`.

Design: `docs/architecture/identity-transport.md`. Cutover: `docs/runbooks/4.0.0-loopback-session.md`.

### Security
- **No request header is identity any more.** `Tailscale-User-*` headers are stripped on every request and only
  logged as `ts_login_hint`. The `tailscale-identity` provider and the unix-socket transport are removed.
- Auth is a browser session cookie (`eve_session`; HttpOnly, Secure, SameSite=Strict, Path=/) or the API key
  (Bearer). Only `GET /health` and `POST /v1/pair` are unauthenticated.
- Pairing codes: CSPRNG, Crockford base32, 8 chars, valid 10 minutes, single use. 5 failed attempts lock pairing
  for 10 minutes and invalidate all outstanding codes.
- Sessions: 256-bit token, 30 days idle / 180 days absolute. Codes and tokens are stored only as
  HMAC-SHA256(pepper, ...). `DELETE /v1/session` logs out; `gateway sessions list|revoke <id>` manages them.
- The gateway binds only to `127.0.0.1` or `::1` and refuses to start on any other address (including `localhost`).
- Host header allowlist: `localhost`, `127.0.0.1`, `::1` plus `allowed_hosts` / `GW_ALLOWED_HOSTS`. Others get `421`,
  a missing Host gets `400`.
- The API key file and the session pepper file must be mode 0600 (no group/other bits), or startup is refused.
- API-key-authenticated requests get `403 scope_denied` on browser-only routes (`GET /v1/actions`, action reissue/revise).

### Changed
- `GET /health` returns exactly `{"status":"ok"}` (no version or other fields).
- Now require auth: `/ready`, `/metrics`, `/stats`, `/openapi.json`, `/docs`. `X-Eve-Version` is sent only on
  authenticated responses. `/ui/` static files stay public so the pairing screen can load.
- Default `auth.providers`: `[session, api-key]`.

### Added
- `pair`, `sessions list`, `sessions revoke` subcommands (in `gateway` and the new `bin/eve-gw`).
- `POST /v1/pair`, `DELETE /v1/session`.
- Config: `allowed_hosts` (`GW_ALLOWED_HOSTS`), `session_pepper_file` (`GW_SESSION_PEPPER_FILE`, default
  `<base>/.session_pepper`).
- Migration `00008_sessions` (reversible; renumbered from 00007 after main added `00007_user_settings`): tables `sessions` and `pairing_codes`. No existing table is changed.

### Removed (startup refuses these if set)
- `unix_socket` / `GW_UNIX_SOCKET`, `tcp_disabled` / `GW_TCP_DISABLED`, provider `tailscale-identity`,
  `BIND_IP` in `deploy/install-launchd-go.sh`.

### UI (eve-web, the renamed UI repo)
- Needs a pairing screen that posts the code to `POST /v1/pair` and handles `401 invalid_session` by showing it.
