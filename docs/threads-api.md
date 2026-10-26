# Conversation storage & threads API

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
  "metadata": {"incomplete":true,"reason":"client_disconnected"}?,
  "status": "complete|incomplete", "client_id": "…"?, "created_at": "…Z",
  "updated_at": "…Z"? /* set when regenerated */,
  "feedback": {"message_id","rating":"up|down","comment":null|"…","created_at","updated_at"}? }
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
3. Stream aborted by upstream error/timeout or client disconnect → whatever was received is saved with
   `status:"incomplete"` and `metadata:{"incomplete":true,"reason":"client_disconnected|upstream_error|upstream_timeout"}`.
   Upstream errors before any output save no assistant message.
4. Regenerate (`replace_message_id`): no new user message; the reply **overwrites the same message id**
   (content, reasoning, model, params, usage, latency, status, metadata), keeps `created_at`, sets `updated_at`,
   and **clears its feedback**. Response `message_id` = the replaced id, `user_message_id` = null.

Non-stream response: headers `X-Thread-Id`, `X-Message-Id`, `X-User-Message-Id` (absent on regenerate), and the
JSON body gains `thread_id`, `message_id`, `user_message_id` (and `replaced_message_id` on regenerate).

Stream response: header `X-Thread-Id` (+`X-User-Message-Id`). The **first** SSE event is

```json
{"id":"persist-<request_id>","object":"chat.completion.chunk","created":1761462000,"model":"…","choices":[],
 "thread_id":"01J…","user_message_id":"01J…"}
```
then upstream chunks unchanged, then **right before `data: [DONE]`**:

```json
{"object":"chat.completion.persisted","created":1761462000,"status":"complete",
 "thread_id":"01J…","message_id":"01J…","user_message_id":"01J…"}
```
(On upstream mid-stream errors: error event, then the persisted event with `status:"incomplete"`, then `[DONE]`.)

Errors: unknown/foreign `thread_id` or `replace_message_id` → 404; `replace_message_id` that is not an assistant
message → 400; no user message in the request → 400; no database → 503 `persistence_disabled`.

## Feedback

| Method & path | Body | Response |
|---|---|---|
| `PUT /v1/messages/{id}/feedback` | `{"rating":"up"\|"down","comment"?: "…"}` | 200 Feedback (upsert) |
| `DELETE /v1/messages/{id}/feedback` | | 204 (toggle off; idempotent) |

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
