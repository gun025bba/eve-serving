# Changelog

## 6.0.0 (unreleased)

**Breaking (MAJOR).** Web tool use is now chosen by the model for every request, whenever the server has `web.enabled`.

### Changed
- **Automatic tool selection is the default and the only mode.** There is no per-user allowlist, opt-in or force path. The request field
  `web_search` (any value, including `"force"` and `"off"`), the thread setting `web_search` and `web.default_mode` are accepted but ignored; the
  400 `web_search_disabled` response is gone. This also applies to API-key requests (they get the web tools and the gateway tool prompt, but still no
  reference block and no memory tool). Frontends must stop relying on `web_search`.
- **Rollback is server-side only:** `web.auto_select_off` / `GW_WEB_AUTO_SELECT_OFF`. It fails closed: an unparsable env value turns auto selection
  off, and the env can only turn it off, never back on over the config file. With it set, the request path is byte-identical to 5.x with web off
  (golden `off_*` cases). `web.enabled=false` still removes the tools completely.
- The gateway system prompt for tool turns carries today's date in Asia/Seoul and, for time-sensitive questions (cheap local gate), a hint to search.
- `capabilities.web_search_default` is now `"auto"` (or `"off"` when web is disabled or rolled back); new `capabilities.auto_tool_selection`.
- Golden fixtures: `force_*` removed; `auto_*` regenerated (new prompt, no `tool_call` event for rejected calls, `error_code`, `count`).

### Added
- **Echo filter** (model-written queries and URLs may not copy protected text such as memory facts; 3 normalized 4-grams not present in the user's own
  text, e-mail/phone/4+ digit/URL tokens; queries are capped at 128 runes; URL path/query are percent-decoded and checked without NFKC). Two rejections
  take the web tools away for the rest of the request. Rejected calls store no arguments.
- **Budgets** per user per Asia/Seoul day (150 searches / 100 fetches) and global (30/60 per minute, 1500/800 per day), configurable; in memory.
- **Suspect-result rules R1-R7** (instruction-like text, tool names, fake delimiters, exfiltration URLs, hidden characters with emoji-sequence
  exclusions, encoded payloads, other languages). The result item/page is dropped (only rule ids are logged); `web_fetch` and the memory tool are
  withdrawn for the rest of the request.
- Untrusted text is NFKC-normalized and stripped of control/format/variation/tag characters; delimiter look-alikes (`untrusted_web_content`,
  `user_memory`, `tool_call`, `tool_response`, `system`) are defanged.
- **SSE contract:** `chat.completion.tool_result` carries `error_code` on failure (QA contract `TestQA_SSRF_ErrorCodeContract`: every ssrf-class corpus item
  reports `ssrf`) plus `count`/`blocked_count`; `tool_call` is sent only for calls that passed the pre-checks, with allowlisted arguments only;
  `chat.completion.persisted` carries `fresh` (`ok`/`unverified`) and a query-free `tools[]`; `cited` is limited to valid source numbers. When the lookup failed the
  model is told to say it could not verify current information. The first tool round streams.
- `qa_testhook` build tag: `webfetch.Options.SetDialIPForTest` (test-only dial counter for the SSRF harness).

### Security
- `.svc` (Kubernetes service names) is denied by the fetch guard (#141 Low). Wildcard-DNS names (nip.io style) report `error_code: ssrf`.

### Not in this change
- The `tool_calls` columns (`selected_by`, `error_code`, `result_count`, `args_expires_at`), HMAC argument logging, retention changes and persisted message metadata
  (#137) and the calendar/PIM parts of the echo filter (5.1.0 code) are not included; see the PR description.

## 5.0.2 (unreleased)

### Fixed
- **Approving a memory add card on Postgres returned 422 `payload_mismatch`.** JSONB stores object keys in its own
  order, so the hash computed over the producer's key order never matched the stored payload. `payload_hash` is now
  over the canonical (sorted-key) JSON, used at create and verify. Approve also accepts the pre-5.0.2 hash; a pending
  card whose key order JSONB had already changed cannot verify and expires within 24h (propose again). New Postgres
  test `TestPostgresActions_PayloadHashSurvivesJSONB` (required by CI).

### Added
- **Eve feature reference in chat requests of interactive sessions.** The gateway puts a short Korean block at the start of the system
  prompt (before client system text; the memory block stays last). It is labeled as reference material (facts, not instructions) and has one
  preamble with the behavior rules: only the listed app features exist ("not supported right now" for anything else, general conversation is
  unaffected), no details about the model, server, device or internals. The list holds only features that are available: web search (on, off, or off
  because it is temporarily unavailable when the audit blocks or its breaker is open), and this user's memory (on, not started yet, off; the line is omitted when
  unavailable). The memory line says a memory is not saved before the user approves the card. No version, commit, function names or API paths are
  included. Generated from runtime state (`internal/selfdesc`, `internal/handlers/selfdesc.go`); a new feature appears when its entry is added,
  and only while it is on. Per request, not stored in threads. Size guard in tests: under 300 estimated tokens (the estimator is approximate; measure with the real tokenizer).
  A regression test (`TestBuild_NoSensitiveInfo`) asserts the block never contains API keys or tokens, DB DSNs or passwords, hostnames, IPs, URLs or file paths in any state.
  Only requests with the `interactive` scope get it (browser/app sessions); API-key requests are unchanged, with no block and no tools.
- The `memory_propose` result the model reads says nothing is saved yet until the user approves the card.
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
  privilege boundary: one root-owned entrypoint may run via sudo without a password, only for four exact verbs, no argument
  (`deploy`, `deploy-web`, `rollback`, `rollback-web`) and only for releases signed **by CI** (Sigstore keyless, identity pinned to the
  reusable `deploy-sign.yml` at `main`, verified offline). No key is generated or kept anywhere; the one-time install is a single line.
  Releases: tag `vX.Y.Z` -> `release.yml`; the UI (eve-web) uses the same reusable workflow. Design, threat model and the repository
  settings it relies on: `docs/deploy-signing.md`; install and removal: `deploy/nopasswd/README.md`.

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
