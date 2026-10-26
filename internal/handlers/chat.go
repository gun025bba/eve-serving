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
	"github.com/gun025bba/llm-serving/internal/metrics"
	"github.com/gun025bba/llm-serving/internal/proxy"
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
	kv := []any{"request_id", rid(r), "stream", rec.stream, "status", rec.status, "error_code", code, "queue_ms", int(q * 1000),
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
	s.streamSSE(w, r, resp.Body, t0, q)
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

// streamSSE forwards upstream bytes verbatim, parsing complete lines for usage/token counts.
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request, body io.Reader, t0 time.Time, q time.Duration) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	rec := record{stream: true, status: 200, t0: t0, q: q}
	var ctCount int64
	var tfirst time.Time
	var line []byte
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if tfirst.IsZero() {
				tfirst = time.Now()
				rec.ttft = fp(tfirst.Sub(t0).Seconds() - q.Seconds())
			}
			chunk := buf[:n]
			line = append(line, chunk...)
			for {
				i := bytes.IndexByte(line, '\n')
				if i < 0 {
					break
				}
				s.parseSSELine(line[:i], &rec, &ctCount)
				line = line[i+1:]
			}
			if _, werr := w.Write(chunk); werr != nil {
				rec.clientGone = true
				break
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err == io.EOF {
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
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", e)
			if fl != nil {
				fl.Flush()
			}
			break
		}
	}
	if rec.ct == nil && ctCount > 0 {
		rec.ct = &ctCount
	}
	if !tfirst.IsZero() {
		rec.genS = fp(time.Since(tfirst).Seconds())
	}
	s.record(r, rec)
}

func (s *Server) parseSSELine(l []byte, rec *record, ctCount *int64) {
	ls := strings.TrimRight(string(l), "\r")
	if !strings.HasPrefix(ls, "data: ") {
		return
	}
	d := strings.TrimSpace(ls[6:])
	if d == "" || d == "[DONE]" {
		return
	}
	var j struct {
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Delta struct {
				Content any `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(d), &j) != nil {
		return
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
