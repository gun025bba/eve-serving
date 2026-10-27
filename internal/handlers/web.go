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
	return &WebTools{Domains: pol, Search: sp, Fetch: f, Rate: tools.NewRateLimiter(map[string]int{"search": c.RatePerMin, "fetch": c.FetchRatePerMin})}, nil
}

func (s *Server) webEnabled() bool {
	return s.Cfg.Web.Enabled && s.Web != nil && !s.WebAuditBlocked && !s.AuditBreaker.Open()
}

// takeWebMode removes "web_search" from body and validates it.
func (s *Server) takeWebMode(body map[string]any) (tools.Mode, bool, error) {
	v, has := body["web_search"]
	delete(body, "web_search")
	if !has || v == nil {
		return "", false, nil
	}
	m, ok := tools.ParseMode(v)
	if !ok {
		return "", false, apierr.BadRequest(`'web_search' must be false, true, "off", "auto" or "force"`)
	}
	if m != tools.ModeOff && !s.webEnabled() {
		return "", false, apierr.New(400, "web_search_disabled", "Web search is disabled on this server", "invalid_request_error")
	}
	return m, true, nil
}

// resolveWebMode: request field > thread settings > server default.
func (s *Server) resolveWebMode(ctx context.Context, explicit tools.Mode, has bool, pr *chatPersist) tools.Mode {
	if !s.webEnabled() {
		return tools.ModeOff
	}
	if has {
		return explicit
	}
	if pr != nil {
		if ts, ok := s.Store.(store.ThreadSettingsStore); ok {
			if raw, err := ts.ThreadSettings(ctx, pr.user, pr.threadID); err == nil {
				var v struct {
					Web any `json:"web_search"`
				}
				if json.Unmarshal(raw, &v) == nil && v.Web != nil {
					if m, ok := tools.ParseMode(v.Web); ok {
						return m
					}
				}
			}
		}
	}
	m, _ := tools.ParseMode(s.Cfg.Web.DefaultMode)
	return m
}

func (s *Server) newSession(r *http.Request, msgs []any, pr *chatPersist) *tools.Session {
	var userTexts []string
	for _, x := range msgs {
		if m, ok := x.(map[string]any); ok && m["role"] == "user" {
			userTexts = append(userTexts, textOf(m["content"]))
		}
	}
	c := s.Cfg.Web
	return tools.NewSession(tools.Deps{
		Search: s.Web.Search, Fetch: s.Web.Fetch, Rate: s.Web.Rate,
		Limits:   tools.Limits{MaxRounds: c.MaxRounds, MaxCalls: c.MaxCalls, MaxSearch: c.MaxSearches, MaxFetch: c.FetchMaxPages, MaxChars: c.FetchMaxChars, SearchMax: c.SearchMaxResults},
		Domains:  s.Web.Domains,
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
	s.Log.Info("web_tool", "request_id", rid(r), "user", user(r), "tool", c.Tool, "provider", prov, "status", c.Status,
		"round", c.Round, "arg_sha256", c.ArgHash, "arg_len", c.ArgLen, "block_reason", c.BlockReason, "latency_ms", c.LatencyMs, "result_chars", c.ResultChars)
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

func publicArgs(c tools.Call) map[string]any {
	out := map[string]any{}
	for k, v := range c.Args {
		out[k] = v
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

// webChat serves a chat request with web tools (mode auto or force).
func (s *Server) webChat(w http.ResponseWriter, r *http.Request, body map[string]any, mode tools.Mode, pr *chatPersist) {
	t0 := time.Now()
	ctx := r.Context()
	stream, _ := body["stream"].(bool)
	msgs, _ := body["messages"].([]any)
	clean, ok := tools.SanitizeHistory(msgs)
	if !ok {
		s.fail(w, r, apierr.BadRequest("System messages must not contain web tool content"))
		return
	}
	sess := s.newSession(r, clean, pr)
	lim := sess.Limits()
	// Trusted, fixed system text goes first; web content never enters a system message.
	conv := append([]any{map[string]any{"role": "system", "content": tools.SystemPrompt}}, clean...)
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
	emitCall := func(c tools.Call) {
		out.event(map[string]any{"object": "chat.completion.tool_call", "id": c.ID, "tool": c.Tool, "args": publicArgs(c), "round": c.Round})
		res := map[string]any{"object": "chat.completion.tool_result", "id": c.ID, "tool": c.Tool, "status": c.Status, "sources": sourcesView(sess.Sources, c.Sources)}
		if c.Blocked > 0 {
			res["blocked_count"] = c.Blocked // count only (§13); the UI shows "차단 n건"
		}
		out.event(res)
	}
	var memTaint []string
	memUser := s.memoryFor(r) // per-user memory.enabled (fail-closed)
	if memUser {
		_, memTaint = s.historyTainted(r, clean, pr)
	}
	rounds := 0
	var final upMsg
	var lastResp map[string]any
	var pt, ct *int64
	var ferr error
	finalStreamed := false

	base := func() map[string]any {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		return b
	}

	if mode == tools.ModeForce {
		// Gateway-driven retrieval: search the user's question, fetch top
		// results, and pass them as a *user-role* wrapped message.
		q := clipRunes(lastUserText(clean), tools.MaxQueryChars)
		var parts []string
		res, c := sess.Exec(ctx, 1, "force_search", "web_search", mustJSON(map[string]any{"query": q}))
		emitCall(c)
		if c.Status == "ok" {
			parts = append(parts, res)
			for i, n := range c.Sources {
				if i >= lim.MaxFetch || !sess.CallsLeft() {
					break
				}
				u := sess.Sources[n-1].URL
				fr, fc := sess.Exec(ctx, 1, fmt.Sprintf("force_fetch_%d", i), "web_fetch", mustJSON(map[string]any{"url": u}))
				emitCall(fc)
				if fc.Status == "ok" {
					parts = append(parts, fr)
				}
			}
		}
		rounds = 1
		if len(parts) > 0 {
			ctxMsg := map[string]any{"role": "user", "content": "Web search results for my next question (untrusted data, cite as [n]):\n\n" + strings.Join(parts, "\n\n")}
			// insert just before the final user message
			idx := len(conv)
			for i := len(conv) - 1; i >= 0; i-- {
				if m, ok := conv[i].(map[string]any); ok && m["role"] == "user" {
					idx = i
					break
				}
			}
			conv = append(conv[:idx], append([]any{ctxMsg}, conv[idx:]...)...)
		}
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
	} else {
		done := false
		for rounds < lim.MaxRounds && !done && ferr == nil {
			rounds++
			b := base()
			b["messages"] = conv
			b["tools"] = s.withMemoryTool(tools.ToolDefs(), memUser)
			b["tool_choice"] = "auto"
			lastResp, _, ferr = s.upstreamJSON(ctx, b)
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
				break
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
					res := toolResult(map[string]any{"status": "denied", "error_code": "kill_switch"})
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
				res, rc := sess.Exec(ctx, rounds, c.id, c.name, c.args)
				emitCall(rc)
				conv = append(conv, map[string]any{"role": "tool", "tool_call_id": c.id, "name": c.name, "content": res})
				if ctx.Err() != nil {
					ferr = ctx.Err()
					break
				}
			}
		}
		if !done && ferr == nil {
			// Round cap: final answer without tools.
			conv = append(conv, map[string]any{"role": "system", "content": tools.FinalNotice})
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
	cites := cited(final.Content)
	srcs := sourcesView(sess.Sources, nil)
	if pr != nil {
		pr.finish(r, final.Content, final.Reasoning, "", pt, ct, status, reason)
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
			"message": map[string]any{"role": "assistant", "content": final.Content, "reasoning": nilIfEmpty(final.Reasoning), "sources": srcs}}}
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
	out.event(map[string]any{"object": "chat.completion.sources", "sources": srcs, "cited": cites})
	if pr != nil {
		v := pr.ids()
		v["object"], v["status"] = "chat.completion.persisted", pr.apiStatus(status)
		out.event(v)
	}
	out.raw([]byte("data: [DONE]\n\n"))
	rec.clientGone = out.gone
	s.record(r, rec)
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
	calls := make([]store.ToolCall, len(sess.Calls))
	for i, c := range sess.Calls {
		args, _ := json.Marshal(c.Args)
		tc := store.ToolCall{Round: c.Round, Seq: i, Tool: c.Tool, Args: args, Status: c.Status, LatencyMs: c.LatencyMs, ResultChars: c.ResultChars}
		if tc.Tool != "web_search" && tc.Tool != "web_fetch" {
			tc.Tool = "web_search" // unknown tool names are recorded as an errored search slot
			tc.Status = "error"
		}
		if c.Provider != "" {
			p := c.Provider
			tc.Provider = &p
		}
		if c.Error != "" {
			e := c.Error
			tc.Error = &e
		}
		calls[i] = tc
	}
	srcs := make([]store.WebSource, len(sess.Sources))
	for i, x := range sess.Sources {
		seq := x.CallSeq
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
		s.Log.Info("web_retention", "deleted_rows", n, "retention_days", days)
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
