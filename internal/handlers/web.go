package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/policy/domains"
	"github.com/gun025bba/eve-serving/internal/search"
	_ "github.com/gun025bba/eve-serving/internal/search/brave"   // registers "brave" (stub, disabled)
	_ "github.com/gun025bba/eve-serving/internal/search/searxng" // registers "searxng"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
	"github.com/gun025bba/eve-serving/internal/webfetch"
)

// WebTools are the long-lived web tool dependencies (nil = feature off).
type WebTools struct {
	Search search.Provider
	Fetch  tools.Fetcher
	Rate   *tools.RateLimiter
	// Domains is the §13 policy; nil = not loaded, web tools fail closed.
	Domains *domains.Policy
	// Budget is the shared per-user/global call budget of automatic selection.
	Budget *tools.Budget
	// MAC keys the argument hash of logs, audit events and tool_calls (§4.2-3).
	// nil = no usable key: automatic selection is off (fail closed, §6.4).
	MAC *tools.ArgMAC
}

func secsDur(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// NewWebTools builds the search chain and the dedicated web_fetch client.
// pol is enforced by the gateway on search results and on every fetch hop.
func NewWebTools(c config.WebConfig, pol *domains.Policy) (*WebTools, error) {
	sp, err := search.New(search.Config{Providers: c.Providers, SearxngURL: c.SearxngURL, BraveKeyFile: c.BraveKeyFile,
		Timeout: secsDur(c.SearchTimeoutS), MaxResults: c.SearchMaxResults})
	if err != nil {
		return nil, err
	}
	f, err := webfetch.New(webfetch.Options{Timeout: secsDur(c.FetchTimeoutS), MaxBytes: c.FetchMaxBytes, MaxChars: c.FetchMaxChars,
		AllowedPorts: c.FetchAllowedPort, DenyCIDRs: c.FetchDenyCIDRs, GlobalConc: c.FetchGlobalConc, PerHostConc: c.FetchPerHostConc,
		HostPolicy: tools.HostPolicy(pol)})
	if err != nil {
		return nil, err
	}
	bud := tools.NewBudget(tools.BudgetConfig{UserDailySearch: c.UserDailySearch, UserDailyFetch: c.UserDailyFetch,
		GlobalSearchPerMin: c.GlobalSearchPerMin, GlobalFetchPerMin: c.GlobalFetchPerMin,
		GlobalDailySearch: c.GlobalDailySearch, GlobalDailyFetch: c.GlobalDailyFetch})
	return &WebTools{Domains: pol, Search: sp, Fetch: f, Budget: bud, Rate: tools.NewRateLimiter(map[string]int{"search": c.RatePerMin, "fetch": c.FetchRatePerMin})}, nil
}

func (s *Server) webEnabled() bool {
	return s.Cfg.Web.Enabled && s.Web != nil && !s.WebAuditBlocked && !s.AuditBreaker.Open()
}

// autoOffNow is the live "auto selection is off" state: the server option
// web.auto_select_off (or GW_WEB_AUTO_SELECT_OFF, which fails closed) or the
// auto_select_off key of the kill-switch file, which the poller applies within
// 5 s without a restart and without SIGHUP (§6.7, G4). It is read for every
// request, every round and every tool call.
func (s *Server) autoOffNow() bool {
	return s.Cfg.Web.AutoSelectOff || tools.Default().Kill.AutoSelectOff()
}

// autoSelect reports whether the model may choose web tools on its own. It
// is the default for every request. It is off when web is disabled, when no
// argument-hash key is loaded (fail closed, §6.4), or when the operator
// switched it off (autoOffNow). There is no per-request, per-thread or
// per-user override in either direction.
func (s *Server) autoSelect() bool {
	return s.webEnabled() && s.Web.MAC != nil && !s.autoOffNow()
}

// webMode is the tool mode of this request: auto, or off when web is
// disabled or auto selection is switched off by the operator.
func (s *Server) webMode() tools.Mode {
	if s.autoSelect() {
		return tools.ModeAuto
	}
	return tools.ModeOff
}

// dropWebField removes the legacy request field "web_search". It is ignored
// whatever its value: the client can neither force, nor forbid, nor allow
// web tools.
func dropWebField(body map[string]any) { delete(body, "web_search") }

// newSession builds the per-request tool session. privateHistory starts the
// turn with private_read set (G1 ⓑ / P④: the history holds an answer built from
// personal data).
func (s *Server) newSession(r *http.Request, msgs []any, pr *chatPersist, protected []string, privateHistory bool) *tools.Session {
	var userTexts []string
	for _, x := range msgs {
		if m, ok := x.(map[string]any); ok && m["role"] == "user" {
			userTexts = append(userTexts, textOf(m["content"]))
		}
	}
	c := s.Cfg.Web
	wt := s.Web
	if wt == nil {
		wt = &WebTools{} // web disabled: memory-only tool path, no web call is ever executed
	}
	return tools.NewSession(tools.Deps{
		Search: wt.Search, Fetch: wt.Fetch, Rate: wt.Rate,
		Auto: true, Protected: protected, Budget: wt.Budget, StepBudget: 15 * time.Second,
		MAC: wt.MAC, AutoOff: s.autoOffNow, PrivateHistory: privateHistory, Private: s.personalTool,
		OnEcho: func(tc tools.Call, rule string) {
			s.Log.Warn("web_echo_rejected", "request_id", rid(r), "user", logUser(r), "tool", tc.Tool, "rule", rule)
		},
		OnSuspect: func(tc tools.Call, rules []string) {
			s.Log.Warn("web_result_suspect", "request_id", rid(r), "user", logUser(r), "tool", tc.Tool, "rules", strings.Join(rules, ","))
		},
		Limits:   tools.Limits{MaxRounds: c.MaxRounds, MaxCalls: c.MaxCalls, MaxSearch: c.MaxSearches, MaxFetch: c.FetchMaxPages, MaxChars: c.FetchMaxChars, SearchMax: c.SearchMaxResults},
		Domains:  wt.Domains,
		Disabled: func() bool { return s.WebAuditBlocked || s.AuditBreaker.Open() },
		OnStart:  func(tc tools.Call) { s.auditCall(r, pr, tc, audit.EvCallStarted) },
		OnCall: func(tc tools.Call) {
			s.observeCall(r, tc)
			s.auditCall(r, pr, tc, audit.EvCallDone)
		},
		OnBlock: func(reason string) { s.M.FetchBlocked.WithLabelValues(reason).Inc() },
		OnDomainBlock: func(tc tools.Call, reason, rule, host string, hop int) {
			s.auditDomainBlock(r, pr, tc, reason, rule, host, hop)
		},
	}, user(r), userTexts)
}

// observeCall records metrics and a hashed log line (never the raw query/URL).
func (s *Server) observeCall(r *http.Request, c tools.Call) {
	prov := c.Provider
	if prov == "" {
		prov = "-"
	}
	if c.Tool != "web_search" && c.Tool != "web_fetch" {
		c.Tool = "unknown"
	}
	s.M.ToolCalls.WithLabelValues(c.Tool, prov, c.Status).Inc()
	if c.Status == "blocked" {
		s.M.ToolDenied.WithLabelValues(c.Tool, c.BlockReason).Inc()
	}
	s.M.ToolLat.WithLabelValues(c.Tool, prov).Observe(float64(c.LatencyMs) / 1000)
	if c.Tool == "web_search" && c.Status == "ok" {
		s.M.SearchResults.WithLabelValues(prov).Observe(float64(c.ResultCount))
	}
	if c.Tool == "web_search" && (c.Status == "error" || c.Status == "timeout") {
		s.M.SearchErrs.WithLabelValues(prov, c.Status).Inc()
	}
	if c.Tool == "web_fetch" && c.Status == "ok" {
		s.M.FetchBytes.Observe(float64(c.FetchBytes))
	}
	s.Log.Info("web_tool", "request_id", rid(r), "user", logUser(r), "tool", c.Tool, "provider", prov, "status", c.Status,
		"round", c.Round, "arg_hmac", c.ArgHash, "kid", s.argKID(), "arg_len", c.ArgLen, "block_reason", c.BlockReason, "latency_ms", c.LatencyMs, "result_chars", c.ResultChars)
}

// sseOut writes SSE events for the web path.
type sseOut struct {
	w       http.ResponseWriter
	fl      http.Flusher
	on      bool
	gone    bool
	created int64
}

func (o *sseOut) start() {
	if !o.on {
		return
	}
	h := o.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	o.w.WriteHeader(200)
	o.fl, _ = o.w.(http.Flusher)
}

func (o *sseOut) raw(b []byte) {
	if !o.on || o.gone {
		return
	}
	if _, err := o.w.Write(b); err != nil {
		o.gone = true
		return
	}
	if o.fl != nil {
		o.fl.Flush()
	}
}

func (o *sseOut) event(v map[string]any) {
	if _, ok := v["created"]; !ok {
		v["created"] = o.created
	}
	b, _ := json.Marshal(v)
	o.raw([]byte("data: " + string(b) + "\n\n"))
}

func sourcesView(ss []tools.Source, ns []int) []map[string]any {
	out := []map[string]any{}
	for _, src := range ss {
		if ns != nil && !containsInt(ns, src.N) {
			continue
		}
		out = append(out, map[string]any{"n": src.N, "title": src.Title, "url": src.URL, "domain": src.Domain, "fetched": src.Fetched})
	}
	return out
}

func containsInt(a []int, x int) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

// publicArgs is the default-deny view of a call's arguments for the
// tool_call event: only allowlisted keys per tool, and nothing at all for a
// call that was not accepted (its Args are empty).
func publicArgs(c tools.Call) map[string]any {
	keys := map[string][]string{
		"web_search": {"query", "lang", "time_range", "max_results"},
		"web_fetch":  {"url"},
	}[c.Tool]
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := c.Args[k]; ok {
			out[k] = v
		}
	}
	return out
}

// upstreamJSON makes one non-streaming upstream call holding a queue slot.
func (s *Server) upstreamJSON(ctx context.Context, body map[string]any) (map[string]any, int, error) {
	body["stream"] = false
	delete(body, "stream_options")
	payload, _ := json.Marshal(body)
	_, rel, err := s.acquire(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer rel()
	resp, err := s.Up.Do(ctx, "POST", "/v1/chat/completions", payload, 0)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, apierr.New(502, "upstream_error", "Upstream read failed", "server_error")
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, upstreamStatusErr(resp.StatusCode, trunc(string(data), 300))
	}
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		return nil, resp.StatusCode, apierr.New(502, "upstream_error", "Upstream returned non-JSON", "server_error")
	}
	return v, resp.StatusCode, nil
}

type upMsg struct {
	Content   string
	Reasoning string
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		} `json:"function"`
	}
}

func firstMessage(v map[string]any) upMsg {
	var out upMsg
	cs, _ := v["choices"].([]any)
	if len(cs) == 0 {
		return out
	}
	c, _ := cs[0].(map[string]any)
	m, _ := c["message"].(map[string]any)
	out.Content = textOf(m["content"])
	out.Reasoning, _ = m["reasoning"].(string)
	if out.Reasoning == "" {
		out.Reasoning, _ = m["reasoning_content"].(string)
	}
	if tc, ok := m["tool_calls"]; ok {
		b, _ := json.Marshal(tc)
		json.Unmarshal(b, &out.ToolCalls)
	}
	return out
}

var citeRe = regexp.MustCompile(`\[(\d{1,2})\]`)

func cited(content string) []int {
	var out []int
	for _, m := range citeRe.FindAllStringSubmatch(content, -1) {
		n, _ := strconv.Atoi(m[1])
		if !containsInt(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func lastUserText(msgs []any) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if m, ok := msgs[i].(map[string]any); ok && m["role"] == "user" {
			return textOf(m["content"])
		}
	}
	return ""
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}

// webChat serves a chat request on the tool path. In mode auto or force the
// web tools are offered; in mode off it runs only when memory_propose is
// offered (memOffer: memory active and the history checkable), and then it is
// the only tool (web calls are refused). With web off and a streaming client
// every round is streamed: text reaches the client as it is generated and the
// loop only buffers once the model starts a tool call.
func (s *Server) webChat(w http.ResponseWriter, r *http.Request, body map[string]any, mode tools.Mode, pr *chatPersist, memOffer bool, protected []string) {
	t0 := time.Now()
	ctx := r.Context()
	stream, _ := body["stream"].(bool)
	msgs, _ := body["messages"].([]any)
	webTools := mode != tools.ModeOff
	clean := msgs
	if webTools {
		var ok bool
		clean, ok = tools.SanitizeHistory(msgs)
		if !ok {
			s.fail(w, r, apierr.BadRequest("System messages must not contain web tool content"))
			return
		}
	}
	// History checks (once): the thread's taint (memory proposals, P⑤) and the
	// personal-data history (P④, G1 ⓑ).
	var memTaint []string
	memUser := memOffer // memory active (enabled + intro seen) and history checkable (fail-closed)
	tainted := false
	if memUser || webTools {
		tainted, memTaint = s.historyTainted(r, msgs, pr) // raw request history: client tool messages taint too
	}
	protected = append([]string(nil), protected...)
	privHist := false
	if webTools {
		if tainted {
			// P⑤: in a tainted thread earlier answers may restate web results or
			// facts the model took from anywhere; they are not query material.
			protected = append(protected, assistantTexts(clean)...)
		}
		var pp []string
		pp, privHist = s.personalHistory(r, msgs, pr)
		protected = append(protected, pp...)
	}
	sess := s.newSession(r, clean, pr, protected, privHist)
	lim := sess.Limits()
	if !webTools && lim.MaxRounds > 3 {
		lim.MaxRounds = 3 // memory_propose only: propose, answer
	}
	// Trusted, fixed system text goes first; web content never enters a system message.
	conv := append([]any{}, clean...)
	freshHint := false
	if webTools {
		freshHint = tools.Gate(lastUserText(clean))
		conv = append([]any{map[string]any{"role": "system", "content": tools.Prompt(time.Now(), freshHint)}}, clean...)
	}
	out := &sseOut{w: w, on: stream, created: time.Now().Unix()}
	if pr != nil {
		pr.setHeaders(w.Header())
	}
	out.start()
	if pr != nil {
		v := pr.ids()
		delete(v, "message_id")
		v["id"], v["object"], v["model"], v["choices"] = "persist-"+rid(r), "chat.completion.chunk", pr.model, []any{}
		out.event(v)
	}
	// tool_call is sent only for calls that passed the pre-checks (the
	// session announces them right before the network request); a rejected
	// call produces just its tool_result with an error_code.
	sess.OnAccepted(func(c tools.Call) {
		out.event(map[string]any{"object": "chat.completion.tool_call", "id": c.ID, "tool": c.Tool, "args": publicArgs(c), "round": c.Round, "selected_by": "model"})
	})
	emitCall := func(c tools.Call) {
		res := map[string]any{"object": "chat.completion.tool_result", "id": c.ID, "tool": c.Tool, "status": c.Status, "sources": sourcesView(sess.Sources, c.Sources), "elapsed_ms": c.LatencyMs}
		if c.ErrorCode != "" {
			res["error_code"] = c.ErrorCode
		}
		if c.Status == "ok" && c.Tool == "web_search" {
			res["count"] = c.ResultCount
		}
		if c.Blocked > 0 {
			res["blocked_count"] = c.Blocked // count only (§13); the UI shows "차단 n건"
		}
		out.event(res)
	}
	rounds := 0
	var final upMsg
	var lastResp map[string]any
	var pt, ct *int64
	var ferr error
	finalStreamed := false
	noteSuspect := false
	preamble := "" // text already streamed to the client in rounds that went on to call a tool

	base := func() map[string]any {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		return b
	}

	done := false
	for rounds < lim.MaxRounds && !done && ferr == nil && !sess.Suspect() {
		rounds++
		b := base()
		b["messages"] = conv
		var defs []any
		if webTools {
			// Expose is recomputed before every upstream request: round 1 only
			// for web_search, nothing after a fetch result, a suspect result, two
			// echo rejections or a kill switch change (§4.3.4).
			defs = sess.ToolDefsFor(rounds)
		}
		if td := s.withMemoryTool(defs, memUser && !sess.Suspect()); len(td) > 0 {
			b["tools"], b["tool_choice"] = td, "auto"
		} else {
			delete(b, "tools")
			delete(b, "tool_choice")
		}
		fwd := ""
		if stream {
			lastResp, fwd, ferr = s.streamRound(ctx, b, out, lim.MaxCalls)
		} else {
			lastResp, _, ferr = s.upstreamJSON(ctx, b)
		}
		if ferr != nil {
			break
		}
		m := firstMessage(lastResp)
		pt, ct = usage(lastResp["usage"])
		type call struct{ id, name, args string }
		var calls []call
		for i, tc := range m.ToolCalls {
			args := ""
			switch a := tc.Function.Arguments.(type) {
			case string:
				args = a
			case nil:
			default:
				bb, _ := json.Marshal(a)
				args = string(bb)
			}
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%d", rounds, i)
			}
			calls = append(calls, call{id, tc.Function.Name, args})
		}
		content := m.Content
		if len(calls) == 0 {
			pc, rest, fails := tools.ParseTextToolCalls(m.Content, lim.MaxCalls)
			if fails > 0 {
				s.M.ToolParseFail.Add(float64(fails))
			}
			for i, c := range pc {
				calls = append(calls, call{fmt.Sprintf("call_%d_%d", rounds, i), c.Name, c.Args})
			}
			content = rest
		}
		if len(calls) == 0 {
			final, done = m, true
			finalStreamed = fwd != ""
			break
		}
		preamble += fwd
		if webTools {
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.name
			}
			sess.BeginRound(rounds, names) // G1 ⓒ: a personal read in this round locks search in the same round
		}
		var tcs []any
		for _, c := range calls {
			tcs = append(tcs, map[string]any{"id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": c.args}})
		}
		conv = append(conv, map[string]any{"role": "assistant", "content": content, "tool_calls": tcs})
		for _, c := range calls {
			if c.name == memory.ToolName && s.memoryOn() && !memUser {
				// user memory off: tool is hidden, but a call that arrives
				// anyway gets a kill_switch tool error; nothing is saved.
				code := "kill_switch"
				if s.memoryActive(r) {
					code = "tainted" // memory is on but proposals are not offered for this request's history
				}
				res := toolResult(map[string]any{"status": "denied", "error_code": code})
				conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
				continue
			}
			if c.name == memory.ToolName && memUser && sess.Suspect() {
				res := toolResult(map[string]any{"status": "denied", "error_code": "tainted"})
				conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
				continue
			}
			if c.name == memory.ToolName && memUser {
				// Any web result already in this request taints the proposal.
				reasons := append([]string(nil), memTaint...)
				for _, wc := range sess.Calls {
					reasons = append(reasons, "request:call:"+wc.Tool)
				}
				for _, src := range sess.Sources {
					reasons = append(reasons, "request:source:"+src.URL)
				}
				res := s.runMemoryPropose(r, pr, c.args, reasons, out.event)
				conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
				continue
			}
			if !webTools {
				// web tools were not offered on this request: refuse, never execute.
				res := toolResult(map[string]any{"status": "denied", "error_code": "tool_unavailable"})
				conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
				continue
			}
			res, rc := sess.Exec(ctx, rounds, c.id, c.name, c.args)
			emitCall(rc)
			conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
			if ctx.Err() != nil {
				ferr = ctx.Err()
				break
			}
		}
		if webTools && ferr == nil {
			// Gateway-owned notes for the next round (never web text).
			if sess.Suspect() && !noteSuspect {
				noteSuspect = true // the loop ends here: no further search or fetch, FinalNotice follows
				conv = append(conv, map[string]any{"role": "system", "content": tools.SuspectNotice})
			}
			if len(sess.Calls) > 0 && !sess.Verified() {
				conv = append(conv, map[string]any{"role": "system", "content": tools.UnverifiedNotice})
			}
		}
	}
	if !done && ferr == nil {
		// Round cap: final answer without tools.
		notice := tools.FinalNotice
		if webTools && len(sess.Calls) > 0 && !sess.Verified() {
			notice += " " + tools.UnverifiedNotice
		}
		if !webTools {
			notice = "The tool budget for this answer is used up. Answer now."
		}
		conv = append(conv, map[string]any{"role": "system", "content": notice})
		b := base()
		b["messages"] = conv
		if stream {
			final, pt, ct, ferr = s.streamFinal(ctx, b, out)
			finalStreamed = true
		} else {
			lastResp, _, ferr = s.upstreamJSON(ctx, b)
			if ferr == nil {
				final = firstMessage(lastResp)
				pt, ct = usage(lastResp["usage"])
			}
		}
	}

	s.M.ToolRounds.Observe(float64(rounds))

	status, reason := "complete", ""
	if ferr != nil {
		status, reason = "incomplete", "upstream_error"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "timeout"
		} else if errors.Is(ferr, context.Canceled) || ctx.Err() != nil {
			reason = "client_disconnected"
		}
	}
	saved := preamble + final.Content
	cites := validCites(cited(saved), len(sess.Sources))
	srcs := sourcesView(sess.Sources, nil)
	fresh, toolSummary := "", []map[string]any(nil)
	if webTools {
		fresh, toolSummary = freshStatus(sess, freshHint), toolsView(sess)
	}
	if pr != nil {
		if webTools {
			pr.setMeta(fresh, toolSummary)
		}
		pr.finish(r, saved, final.Reasoning, "", pt, ct, status, reason)
		s.saveWebRun(r, pr, sess, cites)
	}
	rec := record{stream: stream, status: 200, t0: t0, pt: pt, ct: ct}
	if ferr != nil && !stream {
		if ctx.Err() == nil {
			s.fail(w, r, ferr)
		}
		rec.status = 502
		s.record(r, rec)
		return
	}
	if !stream {
		if lastResp == nil {
			lastResp = map[string]any{"object": "chat.completion"}
		}
		lastResp["choices"] = []any{map[string]any{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": final.Content, "reasoning": nilIfEmpty(final.Reasoning)}}}
		if webTools {
			lastResp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["sources"] = srcs
			if fresh != "" {
				lastResp["fresh"] = fresh
			}
			if len(toolSummary) > 0 {
				lastResp["tools"] = toolSummary
			}
		}
		if pr != nil {
			for k, x := range pr.ids() {
				lastResp[k] = x
			}
			if pr.msgID != "" {
				w.Header().Set("X-Message-Id", pr.msgID)
			}
		}
		writeJSON(w, 200, lastResp)
		s.record(r, rec)
		return
	}
	if ferr != nil && ctx.Err() == nil {
		e, _ := json.Marshal(apierr.Body("upstream_error", "Web answer failed: "+trunc(ferr.Error(), 200), "server_error", rid(r)))
		out.raw([]byte("data: " + string(e) + "\n\n"))
	}
	if !finalStreamed && final.Content != "" {
		// Answer produced in a tool round (non-streamed upstream): emit as one chunk.
		out.event(map[string]any{"id": "web-" + rid(r), "object": "chat.completion.chunk", "model": body["model"],
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": final.Content}, "finish_reason": "stop"}}})
	}
	if webTools {
		out.event(map[string]any{"object": "chat.completion.sources", "sources": srcs, "cited": cites})
	}
	if pr != nil {
		v := pr.ids()
		v["object"], v["status"] = "chat.completion.persisted", pr.apiStatus(status)
		if fresh != "" {
			v["fresh"] = fresh
		}
		if len(toolSummary) > 0 {
			v["tools"] = toolSummary
		}
		out.event(v)
	}
	out.raw([]byte("data: [DONE]\n\n"))
	rec.clientGone = out.gone
	s.record(r, rec)
}

// validCites keeps only citation numbers that exist in this answer's sources.
func validCites(c []int, n int) []int {
	out := []int{}
	for _, x := range c {
		if x >= 1 && x <= n {
			out = append(out, x)
		}
	}
	return out
}

// freshStatus tells the client whether the answer rests on a verified web
// result: "ok" when at least one call returned usable sources, "unverified"
// when web calls were made (or locked out) and none did, or when the Gate hint
// said fresh and no tool was called, "" otherwise.
func freshStatus(sess *tools.Session, hint bool) string {
	switch {
	case sess.Verified():
		return "ok"
	case len(sess.Calls) > 0 || sess.Rejections() > 0 || hint:
		return "unverified" // includes: the Gate hint said fresh and no tool was called
	}
	return ""
}

// toolsView is the per-turn tool summary of the persisted event. It has no
// queries or URLs: only tool, who selected it, status, error code, count.
func toolsView(sess *tools.Session) []map[string]any {
	var out []map[string]any
	for _, c := range sess.Calls {
		v := map[string]any{"tool": c.Tool, "selected_by": "model", "status": c.Status}
		if c.ErrorCode != "" {
			v["error_code"] = c.ErrorCode
		}
		if c.Status == "ok" && c.Tool == "web_search" {
			v["count"] = c.ResultCount
		}
		out = append(out, v)
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// streamFinal streams the final generation to the client, forwarding chunks
// but not upstream [DONE] (the web path adds sources/persisted first).
func (s *Server) streamFinal(ctx context.Context, body map[string]any, out *sseOut) (upMsg, *int64, *int64, error) {
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	delete(body, "tools")
	delete(body, "tool_choice")
	payload, _ := json.Marshal(body)
	var m upMsg
	_, rel, err := s.acquire(ctx)
	if err != nil {
		return m, nil, nil, err
	}
	defer rel()
	resp, err := s.Up.Do(ctx, "POST", "/v1/chat/completions", payload, 0)
	if err != nil {
		return m, nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return m, nil, nil, upstreamStatusErr(resp.StatusCode, string(b))
	}
	rec := record{}
	acc := &streamAcc{}
	var n int64
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l := sc.Bytes()
		if bytes.Equal(bytes.TrimSpace(l), []byte("data: [DONE]")) {
			break
		}
		s.parseSSELine(l, &rec, &n, acc)
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		out.raw(append(append([]byte{}, l...), '\n', '\n'))
	}
	m.Content, m.Reasoning = acc.content.String(), acc.reasoning.String()
	if rec.ct == nil && n > 0 {
		rec.ct = &n
	}
	return m, rec.pt, rec.ct, sc.Err()
}

func (s *Server) saveWebRun(r *http.Request, pr *chatPersist, sess *tools.Session, cites []int) {
	ws, ok := s.Store.(store.WebStore)
	if !ok || pr.msgID == "" || (len(sess.Calls) == 0 && pr.replaceID == "") {
		return
	}
	// Only the web tools have tool_calls rows (the tool CHECK); a personal-data
	// read is recorded in messages.metadata.tools[] only.
	calls := make([]store.ToolCall, 0, len(sess.Calls))
	newSeq := make([]int, len(sess.Calls)) // Session.Calls index -> stored seq (-1: not stored)
	var expires *time.Time
	if d := s.Cfg.Web.ArgsRetentionDays; d > 0 {
		t := time.Now().Add(time.Duration(d) * 24 * time.Hour)
		expires = &t
	}
	for i, c := range sess.Calls {
		if c.Tool != "web_search" && c.Tool != "web_fetch" {
			newSeq[i] = -1
			continue
		}
		newSeq[i] = len(calls)
		args, _ := json.Marshal(c.Args)
		by := "model"
		tc := store.ToolCall{Round: c.Round, Seq: len(calls), Tool: c.Tool, Args: args, Status: c.Status, LatencyMs: c.LatencyMs,
			ResultChars: c.ResultChars, SelectedBy: &by, ArgsExpiresAt: expires} // "denied" is stored as "denied" (migration 00009)
		if c.Provider != "" {
			p := c.Provider
			tc.Provider = &p
		}
		if c.ErrorCode != "" {
			e := c.ErrorCode // the code only, never an internal message
			tc.Error, tc.ErrorCode = &e, &e
		}
		if c.Tool == "web_search" && c.Status == "ok" {
			n := c.ResultCount
			tc.ResultCount = &n
		}
		calls = append(calls, tc)
	}
	srcs := make([]store.WebSource, len(sess.Sources))
	for i, x := range sess.Sources {
		seq := newSeq[x.CallSeq]
		srcs[i] = store.WebSource{N: x.N, URL: x.URL, Title: x.Title, Domain: x.Domain, Snippet: x.Snippet, Fetched: x.Fetched, FinalURL: x.FinalURL,
			ContentSHA256: x.SHA256, Cited: containsInt(cites, x.N), ToolCallSeq: &seq}
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	if len(calls) > 0 || len(srcs) > 0 {
		s.markThreadTainted(r.WithContext(c), pr.user, pr.threadID) // sticky, independent of memory state
	}
	if err := ws.SaveWebRun(c, pr.user, pr.msgID, calls, srcs); err != nil {
		s.Log.Error("web_persist_failed", "request_id", rid(r), "message_id", pr.msgID, "error", logTrunc(err.Error(), 200))
	}
}

// RunWebRetention purges tool_calls/web_sources older than retention_days,
// now and then every interval, until ctx is done.
func (s *Server) RunWebRetention(ctx context.Context, interval time.Duration) {
	ws, ok := s.Store.(store.WebStore)
	days := s.Cfg.Web.RetentionDays
	if !ok || days <= 0 {
		return
	}
	run := func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := ws.PurgeWebHistory(c, time.Now().Add(-time.Duration(days)*24*time.Hour))
		if err != nil {
			s.Log.Error("web_retention_failed", "error", logTrunc(err.Error(), 200))
			return
		}
		s.M.WebPurged.Add(float64(n))
		cleared, err := ws.ClearExpiredArgs(c, time.Now())
		if err != nil {
			s.Log.Error("web_args_retention_failed", "error", logTrunc(err.Error(), 200))
		}
		s.Log.Info("web_retention", "deleted_rows", n, "retention_days", days, "args_cleared", cleared, "args_retention_days", s.Cfg.Web.ArgsRetentionDays)
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

const textToolOpen = "<tool_call>"

// partialMarker reports whether the end of s is a proper prefix of
// "<tool_call>", i.e. the next chunk may complete a textual tool call.
func partialMarker(s string) bool {
	for n := len(textToolOpen) - 1; n > 0; n-- {
		if len(s) >= n && strings.HasSuffix(s, textToolOpen[:n]) {
			return true
		}
	}
	return false
}

type heldLine struct {
	b    []byte
	text string // content text carried by the line ("" for non-content lines)
}

// streamRound is one upstream round of the web-off memory loop. The upstream
// call is streamed and the client sees text as it arrives (first token latency
// is the model's, not the whole generation's). The round only stops
// forwarding when the model starts a tool call: native tool_calls deltas are
// buffered, and so is content from the first "<tool_call>" of a textual call
// (plus, until it is decided, a tail that could still become that marker). A
// textual call that does not parse is released as ordinary text. The result has
// the shape of a non-streamed chat completion so the caller's tool loop is the
// same for both. fwd is the content text that was already sent to the client.
// An upstream that answers with a plain JSON body instead of SSE is accepted
// (nothing is forwarded; the caller emits the answer in one piece).
func (s *Server) streamRound(ctx context.Context, body map[string]any, out *sseOut, maxCalls int) (map[string]any, string, error) {
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	payload, _ := json.Marshal(body)
	_, rel, err := s.acquire(ctx)
	if err != nil {
		return nil, "", err
	}
	defer rel()
	resp, err := s.Up.Do(ctx, "POST", "/v1/chat/completions", payload, 0)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, "", upstreamStatusErr(resp.StatusCode, string(b))
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		c, err := br.Peek(1)
		if err != nil {
			break
		}
		if c[0] == ' ' || c[0] == '\n' || c[0] == '\r' || c[0] == '\t' {
			br.Discard(1)
			continue
		}
		if c[0] == '{' { // not SSE: a complete JSON completion
			data, err := io.ReadAll(io.LimitReader(br, 8<<20))
			if err != nil {
				return nil, "", apierr.New(502, "upstream_error", "Upstream read failed", "server_error")
			}
			var v map[string]any
			if json.Unmarshal(data, &v) != nil {
				return nil, "", apierr.New(502, "upstream_error", "Upstream returned non-JSON", "server_error")
			}
			return v, "", nil
		}
		break
	}

	var content, reasoning, fwd strings.Builder
	type nativeCall struct{ id, name, args string }
	var native []*nativeCall
	var held []heldLine
	var usageV any
	nativeMode, textMode := false, false
	flush := func() {
		for _, h := range held {
			out.raw(h.b)
			fwd.WriteString(h.text)
		}
		held = nil
	}

	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 {
			continue
		}
		if bytes.Equal(l, []byte("data: [DONE]")) {
			break
		}
		line := append(append([]byte{}, l...), '\n', '\n')
		var j struct {
			Usage   any `json:"usage"`
			Choices []struct {
				Delta struct {
					Content          any    `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments any    `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if !bytes.HasPrefix(l, []byte("data:")) || json.Unmarshal(bytes.TrimSpace(l[5:]), &j) != nil {
			if !nativeMode && !textMode && len(held) == 0 {
				out.raw(line) // comments / unknown lines pass through untouched
			}
			continue
		}
		if j.Usage != nil {
			usageV = j.Usage
		}
		text, hasTool := "", false
		for _, c := range j.Choices {
			if t, ok := c.Delta.Content.(string); ok {
				text += t
			}
			reasoning.WriteString(c.Delta.Reasoning)
			reasoning.WriteString(c.Delta.ReasoningContent)
			for _, tc := range c.Delta.ToolCalls {
				hasTool = true
				if tc.Index < 0 {
					continue // malformed delta; never index with it
				}
				for len(native) <= tc.Index && tc.Index < 16 {
					native = append(native, &nativeCall{})
				}
				if tc.Index >= len(native) {
					continue
				}
				n := native[tc.Index]
				if tc.ID != "" {
					n.id = tc.ID
				}
				n.name += tc.Function.Name
				switch a := tc.Function.Arguments.(type) {
				case string:
					n.args += a
				case nil:
				default:
					bb, _ := json.Marshal(a)
					n.args += string(bb)
				}
			}
		}
		content.WriteString(text)
		switch {
		case hasTool:
			if !nativeMode && !textMode {
				flush() // text held back as a possible marker was plain text after all
			}
			nativeMode = true
		case nativeMode:
			// everything after a native tool call starts is buffered: dropped
		default:
			held = append(held, heldLine{b: line, text: text})
			if !textMode && strings.Contains(content.String(), textToolOpen) {
				textMode = true
			}
			if !textMode && !partialMarker(content.String()) {
				flush()
			}
		}
	}
	serr := sc.Err()

	msg := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		msg["reasoning"] = reasoning.String()
	}
	if nativeMode {
		var tcs []any
		for _, n := range native {
			if n.name == "" {
				continue
			}
			tcs = append(tcs, map[string]any{"id": n.id, "type": "function", "function": map[string]any{"name": n.name, "arguments": n.args}})
		}
		msg["tool_calls"] = tcs
	}
	if !nativeMode {
		// Decide what is left: a textual call that parses is handled by the
		// caller (the held text is dropped); anything else is released.
		calls, _, _ := tools.ParseTextToolCalls(content.String(), maxCalls)
		if !(textMode && len(calls) > 0) {
			flush()
		}
	}
	res := map[string]any{"choices": []any{map[string]any{"message": msg}}}
	if usageV != nil {
		res["usage"] = usageV
	}
	return res, fwd.String(), serr
}
