package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gun025bba/llm-serving/internal/apierr"
	"github.com/gun025bba/llm-serving/internal/auth"
	"github.com/gun025bba/llm-serving/internal/metrics"
	"github.com/gun025bba/llm-serving/internal/proxy"
	"github.com/gun025bba/llm-serving/internal/tools"
)

const maxBody = 8 << 20

// prepareBody validates the request and fills config defaults for omitted fields.
func (s *Server) prepareBody(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, apierr.BadRequest("Request body must be a JSON object")
	}
	if msgs, ok := body["messages"].([]any); !ok || len(msgs) == 0 {
		return nil, apierr.BadRequest("'messages' must be a non-empty array")
	}
	if _, ok := body["model"]; !ok {
		body["model"] = s.Cfg.Model
	}
	for k, v := range s.Cfg.Defaults {
		if _, ok := body[k]; !ok {
			body[k] = v
		}
	}
	return body, nil
}

type record struct {
	stream     bool
	status     int
	t0         time.Time
	q          time.Duration
	pt, ct     *int64
	genS, ttft *float64
	code       string
	clientGone bool
}

func fp(v float64) *float64 { return &v }

func (s *Server) record(r *http.Request, rec record) {
	total := time.Since(rec.t0).Seconds()
	var tps *float64
	if rec.ct != nil && *rec.ct > 0 && rec.genS != nil && *rec.genS > 0 {
		tps = fp(float64(*rec.ct) / *rec.genS)
	}
	s.M.Lat.WithLabelValues(fmt.Sprint(rec.stream)).Observe(total)
	s.M.Reqs.WithLabelValues("/v1/chat/completions", fmt.Sprint(rec.status)).Inc()
	if rec.pt != nil && *rec.pt > 0 {
		s.M.PTok.Add(float64(*rec.pt))
	}
	if rec.ct != nil && *rec.ct > 0 {
		s.M.CTok.Add(float64(*rec.ct))
	}
	if tps != nil {
		s.M.TPS.Observe(*tps)
	}
	if rec.ttft != nil {
		s.M.TTFT.Observe(*rec.ttft)
	}
	if rec.code != "" {
		s.M.Errs.WithLabelValues(rec.code).Inc()
	}
	q := rec.q.Seconds()
	s.M.Recent.Add(metrics.Sample{OK: rec.code == "" && rec.status < 400, Code: rec.code, Lat: fp(total), Q: fp(q), TPS: tps, TTFT: rec.ttft})
	m := s.Mem.Sample(s.M)
	var ttftMs, tpsR, code any
	if rec.ttft != nil {
		ttftMs = int(*rec.ttft * 1000)
	}
	if tps != nil {
		tpsR = metrics.Round(*tps, 1)
	}
	if rec.code != "" {
		code = rec.code
	}
	idt, _ := auth.FromContext(r.Context())
	kv := []any{"request_id", rid(r), "user", idt.User, "auth_provider", idt.Provider, "stream", rec.stream, "status", rec.status, "error_code", code, "queue_ms", int(q * 1000),
		"ttft_ms", ttftMs, "total_ms", int(total * 1000), "prompt_tokens", rec.pt, "completion_tokens", rec.ct,
		"tokens_per_s", tpsR, "mlx_rss_gb", m.MLXRSSGB, "mem_avail_gb", m.AvailGB, "queue_depth", s.Q.Waiting(), "inflight", s.Q.Inflight()}
	if rec.clientGone {
		kv = append(kv, "client_disconnected", true)
	}
	s.Log.Info("chat_completion", kv...)
}

func upstreamStatusErr(status int, txt string) *apierr.Error {
	if status >= 500 {
		return apierr.New(502, "upstream_error", fmt.Sprintf("Upstream %d: %s", status, txt), "server_error")
	}
	return apierr.New(400, "bad_request", fmt.Sprintf("Upstream %d: %s", status, txt), "invalid_request_error")
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.fail(w, r, apierr.BadRequest("Request body too large or unreadable"))
		return
	}
	body, err := s.prepareBody(raw)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	webMode, webExplicit, err := s.takeWebMode(body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pr, err := s.beginPersist(r, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if mode := s.resolveWebMode(r.Context(), webMode, webExplicit, pr); mode == tools.ModeAuto || mode == tools.ModeForce {
		s.webChat(w, r, body, mode, pr)
		return
	}
	if pr != nil {
		pr.setHeaders(w.Header())
	}
	stream, _ := body["stream"].(bool)
	if stream {
		if _, ok := body["stream_options"]; !ok {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	payload, _ := json.Marshal(body)

	t0 := time.Now()
	q, err := s.Q.Acquire(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.fail(w, r, err)
		return
	}
	s.M.QWait.Observe(q.Seconds())
	released := false
	release := func() {
		if !released {
			released = true
			s.Q.Release()
		}
	}
	defer release()

	tg := time.Now()
	resp, err := s.Up.Do(r.Context(), "POST", "/v1/chat/completions", payload, 0)
	if err != nil {
		release()
		if r.Context().Err() != nil {
			return
		}
		s.fail(w, r, err)
		return
	}
	defer resp.Body.Close()

	if !stream {
		data, err := io.ReadAll(resp.Body)
		genS := time.Since(tg).Seconds()
		release()
		if err != nil {
			if errors.Is(err, proxy.ErrIdleTimeout) {
				s.fail(w, r, s.Up.Timeout())
			} else {
				s.fail(w, r, apierr.New(502, "upstream_error", "Upstream read failed", "server_error"))
			}
			return
		}
		var v map[string]any
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			s.fail(w, r, apierr.New(502, "upstream_error", fmt.Sprintf("Upstream returned non-JSON (%d)", resp.StatusCode), "server_error"))
			return
		}
		if resp.StatusCode >= 400 {
			s.fail(w, r, upstreamStatusErr(resp.StatusCode, trunc(string(data), 300)))
			return
		}
		pt, ct := usage(v["usage"])
		s.record(r, record{stream: false, status: resp.StatusCode, t0: t0, q: q, pt: pt, ct: ct, genS: &genS})
		if pr != nil {
			content, reasoning := "", ""
			if cs, ok := v["choices"].([]any); ok && len(cs) > 0 {
				if c, ok := cs[0].(map[string]any); ok {
					if m, ok := c["message"].(map[string]any); ok {
						content = textOf(m["content"])
						reasoning, _ = m["reasoning"].(string)
						if reasoning == "" {
							reasoning, _ = m["reasoning_content"].(string)
						}
					}
				}
			}
			model, _ := v["model"].(string)
			pr.finish(r, content, reasoning, model, pt, ct, "complete", "")
			for k, x := range pr.ids() {
				v[k] = x
			}
			if pr.msgID != "" {
				w.Header().Set("X-Message-Id", pr.msgID)
			}
			data, _ = json.Marshal(v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		release()
		s.fail(w, r, upstreamStatusErr(resp.StatusCode, string(b)))
		return
	}
	s.streamSSE(w, r, resp.Body, t0, q, pr)
}

func usage(u any) (pt, ct *int64) {
	m, ok := u.(map[string]any)
	if !ok {
		return nil, nil
	}
	get := func(k string) *int64 {
		if n, ok := m[k].(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				return &i
			}
		}
		if f, ok := m[k].(float64); ok {
			i := int64(f)
			return &i
		}
		return nil
	}
	return get("prompt_tokens"), get("completion_tokens")
}

// streamAcc accumulates the assistant reply from SSE deltas.
type streamAcc struct {
	content, reasoning strings.Builder
	model              string
}

// streamSSE forwards upstream SSE line by line (bytes unchanged), parsing
// usage/token counts. With persistence it prepends a chunk carrying
// thread_id/user_message_id and, before [DONE], saves the reply and emits a
// "chat.completion.persisted" event. Aborts/disconnects save the partial reply
// as status "incomplete".
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request, body io.Reader, t0 time.Time, q time.Duration, pr *chatPersist) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	rec := record{stream: true, status: 200, t0: t0, q: q}
	acc := &streamAcc{}
	event := func(v map[string]any) []byte {
		b, _ := json.Marshal(v)
		return []byte("data: " + string(b) + "\n\n")
	}
	created := time.Now().Unix()
	persistEvent := func(status, reason string) []byte {
		pr.finish(r, acc.content.String(), acc.reasoning.String(), acc.model, rec.pt, rec.ct, status, reason)
		v := pr.ids()
		v["object"], v["created"], v["status"] = "chat.completion.persisted", created, status
		return event(v)
	}
	if pr != nil {
		v := pr.ids()
		delete(v, "message_id")
		v["id"], v["object"], v["created"], v["model"], v["choices"] = "persist-"+rid(r), "chat.completion.chunk", created, pr.model, []any{}
		if _, err := w.Write(event(v)); err == nil {
			flush()
		}
	}
	var ctCount int64
	var tfirst time.Time
	var line []byte
	var out bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if tfirst.IsZero() {
				tfirst = time.Now()
				rec.ttft = fp(tfirst.Sub(t0).Seconds() - q.Seconds())
			}
			line = append(line, buf[:n]...)
			out.Reset()
			for {
				i := bytes.IndexByte(line, '\n')
				if i < 0 {
					break
				}
				l := line[:i+1]
				if pr != nil && !pr.done && strings.TrimSpace(string(l)) == "data: [DONE]" {
					out.Write(persistEvent("complete", ""))
				}
				s.parseSSELine(line[:i], &rec, &ctCount, acc)
				out.Write(l)
				line = line[i+1:]
			}
			if out.Len() > 0 {
				if _, werr := w.Write(out.Bytes()); werr != nil {
					rec.clientGone = true
					break
				}
				flush()
			}
		}
		if err == io.EOF {
			if len(line) > 0 {
				w.Write(line)
			}
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				rec.clientGone = true
				break
			}
			msg := "Stream aborted: " + err.Error()
			rec.code, rec.status = "upstream_error", 502
			if errors.Is(err, proxy.ErrIdleTimeout) {
				rec.code, rec.status, msg = "upstream_timeout", 504, "Upstream stalled mid-stream"
			}
			e, _ := json.Marshal(apierr.Body(rec.code, msg, "server_error", rid(r)))
			fmt.Fprintf(w, "data: %s\n\n", e)
			if pr != nil {
				w.Write(persistEvent("incomplete", rec.code))
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			break
		}
	}
	if rec.ct == nil && ctCount > 0 {
		rec.ct = &ctCount
	}
	if pr != nil && !pr.done {
		if rec.clientGone {
			pr.finish(r, acc.content.String(), acc.reasoning.String(), acc.model, rec.pt, rec.ct, "incomplete", "client_disconnected")
		} else { // upstream ended without [DONE]
			w.Write(persistEvent("complete", ""))
			flush()
		}
	}
	if !tfirst.IsZero() {
		rec.genS = fp(time.Since(tfirst).Seconds())
	}
	s.record(r, rec)
}

func (s *Server) parseSSELine(l []byte, rec *record, ctCount *int64, acc *streamAcc) {
	ls := strings.TrimRight(string(l), "\r")
	if !strings.HasPrefix(ls, "data: ") {
		return
	}
	d := strings.TrimSpace(ls[6:])
	if d == "" || d == "[DONE]" {
		return
	}
	var j struct {
		Model   string         `json:"model"`
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Delta struct {
				Content          any    `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(d), &j) != nil {
		return
	}
	if j.Model != "" && acc.model == "" {
		acc.model = j.Model
	}
	for _, c := range j.Choices {
		if str, ok := c.Delta.Content.(string); ok {
			acc.content.WriteString(str)
		}
		acc.reasoning.WriteString(c.Delta.Reasoning)
		acc.reasoning.WriteString(c.Delta.ReasoningContent)
	}
	if j.Usage != nil {
		pt, ct := usage(j.Usage)
		if pt != nil {
			rec.pt = pt
		}
		if ct != nil && *ct > 0 {
			rec.ct = ct
		}
		return
	}
	for _, c := range j.Choices {
		if str, ok := c.Delta.Content.(string); ok && str != "" {
			*ctCount++
			return
		}
	}
}
