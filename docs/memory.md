
## Model proposals and approve (PR 1)

- While `memory.enabled` (server) is on, the web tool loop (`web_search: auto`)
  exposes `memory_propose` (`op` add|forget). Non-web chat has no tool loop,
  so the tool is not offered there.
- The tool never writes. It creates a `pending_actions` card (tool
  `memory.propose`) and emits it as an SSE confirmation event. The model gets
  `pending_confirmation`, `no_match` (forget, no card) or `denied` + code.
- Taint: tool/tool_calls in the request history, any web call earlier in the
  same request, or persisted web sources / proactive messages in the thread
  (lookup errors count as tainted). Tainted proposals are denied, no card.
- `POST /v1/actions/{id}/approve {confirm_token, payload_hash}` exists only
  while memory is on. Token from reissue/revise; wrong/replaced token → 410
  `expired`; hash mismatch → 422 `payload_mismatch`; non-memory card →
  `scope_denied`; deleted item → 409 `item_gone`. Audit `memory_approve` is
  fail-closed; status moves to existing `approved` (no migration); replay → 404.
