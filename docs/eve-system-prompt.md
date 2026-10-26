# Eve system prompt (proposal, not deployed)

Status: proposal on branch `feat/eve-system-prompt`. Not merged, not deployed; needs approval.
Text and helpers: `internal/tools/prompt.go` (`EvePrompt`, `WithSystemPrompt`), tests in `prompt_test.go`.

## Text

Common (all modes), `{{DATE}}` = today in Asia/Seoul, e.g. `2026년 10월 26일 (월)`:

> 당신은 Eve입니다. 사용자의 개인 서버에서 동작하는 AI 어시스턴트예요.
> 오늘 날짜는 {{DATE}}입니다(한국 시간). 날짜나 요일이 필요한 질문에는 이 날짜를 기준으로 답하세요.
> 사용자가 쓴 언어로 답하고, 한국어로 물으면 자연스러운 한국어로 답하세요.

Web search off (server has it enabled, request didn't turn it on), appended:

> 지금은 웹 검색이 꺼져 있어서 실시간 정보를 조회할 수 없어요. 학습한 지식으로 답하되, 최신 정보가 필요한 질문(뉴스, 가격, 일정, 최근 출시 등)이면 답이 오래됐을 수 있다고 짧게 알려 주세요.
> 그럴 때는 "입력창의 웹 검색 버튼을 켜고 다시 물어보시면 검색해서 답할게요"처럼 안내하세요. 다른 검색 엔진을 직접 써 보라고 권하거나 "제 지식은 몇 년까지입니다"라고 말하지 마세요.

Web search on (auto/force), appended (the existing English `tools.SystemPrompt` with the
untrusted-content and `[n]` citation rules stays right after it):

> 지금은 웹 검색이 켜져 있어요. web_search와 web_fetch 도구로 최신 정보를 찾아볼 수 있으니, 검색을 할 수 없다고 말하지 마세요.
> 최신 정보나 사실 확인이 필요한 질문이면 먼저 도구로 찾아본 뒤 답하고, 근거에는 [1], [2]처럼 출처 번호를 붙이세요. 검색이 필요 없는 질문(인사, 일반 지식, 글쓰기 등)은 바로 답해도 돼요.

`GW_WEB_ENABLED=false`: common text only (never point to a button that does nothing).

## Where it goes

- `internal/handlers/chat.go` `chat()`, right after `resolveWebMode`:
  - mode off → `body["messages"] = tools.WithSystemPrompt(tools.EvePrompt(now, KST, state), msgs)`
    with `state = WebOff` if `s.webEnabled()` else `WebUnavailable`.
- `internal/handlers/web.go` `webChat()`: replace
  `conv := append([]any{system: tools.SystemPrompt}, clean...)` with
  `conv := tools.WithSystemPrompt(tools.EvePrompt(now, KST, tools.WebOn)+"\n\n"+tools.SystemPrompt, clean)`.
  Client system text still passes `SanitizeHistory` first, so web-tagged content is still rejected.
- Behind a flag `GW_EVE_PROMPT` (default false at first) so the model engineer's API-key eval
  baseline doesn't shift silently; turn on after a before/after eval run.

## Combining with client system messages

One system message: gateway text first, then the client's leading system messages joined under
`--- 추가 지시(클라이언트):`. Client instructions (tone, format) still apply; the name, date and
search facts come from the gateway. Non-leading system messages are left in place. Stored history
is unchanged (the prompt is added per request, never persisted).

## Tests

- `TestEvePrompt`: each state has/doesn't have the expected phrases; date uses KST (UTC 16:00 →
  next day); no "2024"/"Google" wording.
- `TestWithSystemPrompt`: merges leading client system messages, skips blank ones, keeps later ones, nil history.
- To add with the wiring: handler tests asserting the first upstream message is the Eve prompt for
  off/auto/force and for `GW_WEB_ENABLED=false`, that a client system message is merged not dropped,
  and that `GW_EVE_PROMPT=false` forwards messages unchanged.
