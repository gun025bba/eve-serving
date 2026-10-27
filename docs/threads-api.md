# Eve: Conversation storage & threads API

Status: **implemented** (gateway, PostgreSQL). Schema details: [`db-model.md`](db-model.md).

All endpoints require auth (Tailscale identity via `tailscale serve`, or `Authorization: Bearer <key>`).
Every row is scoped to the authenticated `Identity.User`; another user's ids return **404 `not_found`**.
If the gateway has no database (`GW_DATABASE_URL` unset) every endpoint below returns
**503 `persistence_disabled`**, while plain `/v1/chat/completions` keeps working.

Errors keep the gateway envelope: `{"error":{"code","message","type","request_id"}}`.
Codes used here: `bad_request` (400), `not_found` (404), `payload_too_large` (413),
`persistence_disabled` (503), plus the usual auth/queue/upstream codes.

Timestamps are RFC 3339 in UTC. Ids are ULIDs (26 chars, time-sortable).

## Objects

```jsonc
// Thread
{ "id": "01J…", "title": "제주도 여행", "created_at": "…Z", "updated_at": "…Z",
  "archived": false, "message_count": 4, "last_message_preview": "흑돼지 거리…" | null,
  "client_id": "local-1" /* only for imported threads */ }

// Message
{ "id": "01J…", "thread_id": "01J…", "role": "user|assistant|system|tool",
  "content": "…", "reasoning": "…"?, "model": "…"?, "params": {"temperature":0.7,…}?,
  "usage": {"prompt_tokens":12,"completion_tokens":34}?, "latency_ms": 812?,
  "metadata": {"incomplete":true,"reason":"…","error_code":"…"}?,
  "status": "generating|complete|failed", "error_code": "…"? /* only when failed */, "client_id": "…"?, "created_at": "…Z",
  "updated_at": "…Z"? /* set when regenerated */,
  "feedback": {"message_id","rating":"up|down","reasons":["…"],"comment":null|"…","created_at","updated_at"}? }
```
`?` = omitted when empty.

## Threads

| Method & path | Body / query | Response |
|---|---|---|
| `GET /v1/threads` | `limit` (1–100, def 20), `cursor`, `archived` (def `false`) | `{"data":[Thread], "next_cursor": "…"\|null}` ordered `updated_at` desc (keyset cursor, opaque) |
| `POST /v1/threads` | `{"title"?: "…"}` (body optional) | **201** Thread |
| `GET /v1/threads/{id}` | | Thread |
| `PATCH /v1/threads/{id}` | `{"title"?: "…", "archived"?: bool}` | Thread (unknown fields → 400) |
| `DELETE /v1/threads/{id}` | | **204** (soft delete = `archived:true`; undo with PATCH) |
| `GET /v1/threads/{id}/messages/{message_id}/stream` | `Last-Event-ID` header or `after=<seq>` | SSE replay + live follow, ends `[DONE]`; snapshot event if no longer in memory or the cursor is older than the retained log (§3.8). `Cache-Control: no-store`; max 4 subscribers per message (5th → 429 `too_many_subscribers`) |
| `POST /v1/threads/{id}/messages/{message_id}/cancel` | (CSRF headers, no body) | **204** always for an owned assistant message (idempotent); a running generation becomes `failed`/`cancelled`. Unknown/foreign → 404 |
| `GET /v1/threads/{id}/messages` | `limit` (1–200, def 50), `before=<message_id>` | `{"data":[Message] oldest→newest, "has_more": bool}` — the newest `limit` messages older than `before` |

Titles are trimmed to one line, max 200 chars. `updated_at` moves on new messages, rename,
archive and regenerate.

## Chat persistence — `POST /v1/chat/completions`

Still OpenAI-compatible. Two optional extra fields (both stripped before forwarding upstream):

| field | meaning |
|---|---|
| `thread_id` | existing thread id, or `"new"` to create one titled from the last user message (~40 chars) |
| `replace_message_id` | regenerate: id of an **assistant** message in that thread. Requires an existing `thread_id`. |

Behaviour
1. The **last user message** is saved immediately, before the upstream call (not on regenerate).
2. The assistant reply is saved when generation ends: content, `reasoning` (`delta.reasoning`/`reasoning_content`),
   `model`, `params` (request fields except messages/model/stream/stream_options), `usage`, `latency_ms`, `status:"complete"`.
3. **Since 3.1.0 (detached generation, ui-api-contract.md §3.8):** the assistant row is created when generation
   starts (`status:"generating"`); generation runs to the end even if the client disconnects, then the row becomes
   `complete`, or `failed` with `error_code` (upstream error/timeout, 15-minute limit, gateway restart). A client
   disconnect no longer cancels or truncates the reply. Resume with
   `GET /v1/threads/{id}/messages/{message_id}/stream` (`Last-Event-ID` or `?after=`); see §3.8.
4. Regenerate (`replace_message_id`): no new user message; the reply **overwrites the same message id**
   (content, reasoning, model, params, usage, latency, status, metadata), keeps `created_at`, sets `updated_at`,
   and **clears its feedback**. Response `message_id` = the replaced id, `user_message_id` = null.
5. Resend without `replace_message_id` (3.1.1+): if the thread's last user message has the **same text** and has
   no reply after it, or only one `failed` reply (incl. `error_code: cancelled`), no new user row is stored. The
   response `user_message_id` is that existing message; if a failed reply existed it is overwritten in place like
   a regenerate (`message_id` = `replaced_message_id` = the failed reply's id). A `complete` or `generating` reply,
   or different text, always starts a new turn.
   - **Intentional repeats are merged too**: sending the same sentence again on purpose, before the previous
     one got a successful reply, is treated as the same turn (one user row). To ask the same thing again as a
     new turn, wait for the reply to finish (or change the text).
   - **Concurrent resends**: the check-then-insert is serialized per thread (in-process lock; the gateway runs as
     a single process), so two identical resends arriving together store one user row and both responses carry
     the same `user_message_id`. Each request still generates its own reply.
   - Relies on `ListMessages(limit=2)` returning the two newest messages oldest→newest (fixed by test).

Non-stream response: headers `X-Thread-Id`, `X-Message-Id`, `X-User-Message-Id` (absent on regenerate), and the
JSON body gains `thread_id`, `message_id`, `user_message_id` (and `replaced_message_id` on regenerate).

Stream response: headers `X-Thread-Id`, `X-Message-Id` (+`X-User-Message-Id`). Every event carries `id: <seq>`.
The **first** event is `event: eve.meta` with `{"thread_id","message_id"}`; the next is

```json
{"id":"persist-<request_id>","object":"chat.completion.chunk","created":1761462000,"model":"…","choices":[],
 "thread_id":"01J…","user_message_id":"01J…"}
```
then upstream chunks unchanged, then **right before `data: [DONE]`**:

```json
{"object":"chat.completion.persisted","created":1761462000,"status":"complete",
 "thread_id":"01J…","message_id":"01J…","user_message_id":"01J…"}
```
(On upstream mid-stream errors: error event, then the persisted event with `status:"failed"`, then `[DONE]`.)

Limits (§3.8): 2 concurrent generations per user (429 `too_many_generations` + `Retry-After`); hub-wide (64 runs / 128MB, also when running runs alone fill the byte cap) 429
`server_busy`; partial reply > 64KB → `failed`/`too_large`; no subscriber for 5 min → `failed`/`no_subscriber`.
Queue errors (`queue_full` 429 etc.) happen **before** the assistant row is created and keep their 3.0.0 HTTP status.
Regenerating a message that is already `generating` → 409 `generation_in_progress`.
Resending the **same user turn** (same thread, identical last user message, no `replace_message_id`) while that turn
is still being answered → 409 `generation_in_progress` instead of a second answer (#30). Duplicates are judged by
same thread + same user message (its `user_message_id`); there is no `Idempotency-Key` header. The 409 `error` object adds
`thread_id`, `user_message_id` and `message_id` (the reply being generated; reattach via
`GET /v1/threads/{id}/messages/{message_id}/stream`). `message_id` is `""` in the brief window before the reply row
exists — retry shortly. The in-flight mark is released
on completion, error and client cancel; a stale `generating` row with no live run does not block. The startup sweep that marks
leftover `generating` rows `gateway_restart` assumes a **single gateway instance** per database.

Errors: unknown/foreign `thread_id` or `replace_message_id` → 404; `replace_message_id` that is not an assistant
message → 400; no user message in the request → 400; no database → 503 `persistence_disabled`.

## Feedback

| Method & path | Body | Response |
|---|---|---|
| `PUT /v1/messages/{id}/feedback` | `{"rating":"up"\|"down","reasons"?: ["…"],"comment"?: "…"}` | 200 Feedback |
| `DELETE /v1/messages/{id}/feedback` | | 204 (toggle off; idempotent) |
| `GET /v1/feedback/export?since=&format=jsonl` | | 200 JSON lines (admin only) |

`PUT` is a **full replace** and idempotent: omitted `reasons`/`comment` are cleared, so to add reasons
later send `rating` and `reasons` together again. Old clients that send only `rating` keep working.

- `reasons`: codes for the rating's polarity, deduplicated (order kept), at most 8.
  - `down`: `wrong_info`, `too_long`, `misunderstood`, `over_refusal`, `bad_source`, `other`
  - `up`: `accurate`, `concise`, `helpful`, `good_source`
  - Unknown code or wrong polarity → 400 `invalid_feedback_reason`.
- `comment`: trimmed; empty → `null`; more than 140 characters → 400 `feedback_comment_too_long`.
- The DB enforces the same lists and length with CHECK constraints (migration `00003_feedback_reasons`).

### Export (model evaluation)

`GET /v1/feedback/export?since=2026-10-01&format=jsonl` returns every user's feedback updated at or
after `since` (RFC3339 or `YYYY-MM-DD`; default: all), oldest first, as `application/x-ndjson`.
Allowed for API-key callers and Tailscale logins in `GW_ALLOWED_USERS`; others get 403.
Each line, in this field order:

```json
{"message":"<preceding user prompt>","response":"<assistant reply>","rating":"down","reasons":["wrong_info"],"comment":null,"created_at":"…Z","thread_id":"…","message_id":"…"}
```

The same query for `psql` is in `scripts/feedback-export.sql`.

## Search — `GET /v1/search?q=&limit=20&cursor=`

Searches the caller's non-archived thread titles and message contents.
Match = Postgres full-text (`to_tsvector('simple', …)`) **or** substring `ILIKE` backed by `pg_trgm` GIN
indexes (works for Korean, which has no stemmer). Ranked by `ts_rank + similarity` (title hits boosted), then recency.

```json
{"data":[{"thread_id":"…","thread_title":"…","message_id":"…"|null,"role":"user"|null,
          "snippet":"…<mark>제주도</mark>…","created_at":"…Z","match":"title"|"content"}],
 "next_cursor":"…"|null}
```
`snippet` is **HTML-escaped first**, then matches are wrapped in `<mark>`; `<mark>`/`</mark>` are the only tags
that can appear, so it is safe to set as `innerHTML`. `q` is required (≤200 bytes); `limit` 1–100.

## Import — `POST /v1/threads/import`

Bulk-import local conversations. Body ≤ 5 MB (413 `payload_too_large` → split into batches).

```json
{"threads":[{"client_id":"local-1","title":"…","created_at":"2025-03-01T09:10:11.123Z","archived":false,
  "messages":[{"role":"user","content":"…","created_at":"…","client_id":"lm-1",
               "reasoning":"…","model":"…"}]}]}
```
→ `200 {"results":[{"client_id":"local-1","thread_id":"01J…","status":"created"|"exists"}]}`

- Idempotent per thread `client_id` (per user): re-sending returns `exists` and writes nothing.
- `created_at` of threads and messages is preserved exactly; thread `updated_at` = newest message time.
- Message `client_id` is optional; when given it is unique per user (duplicates are skipped).
- Validation (400): thread `client_id`/`created_at` required; message `role` ∈ user/assistant/system/tool and `created_at` required.
