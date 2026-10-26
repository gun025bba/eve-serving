package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/metrics"
)

// Detached generation (docs/architecture/ui-api-contract.md §3.8).
//
// A persisted chat runs in a background goroutine whose context is detached
// from the HTTP request. Its SSE output is written into a genRun: every SSE
// event gets a monotonic seq (1, 2, ...) and is kept in a bounded in-memory
// log so subscribers (the original request, or GET .../messages/{mid}/stream
// after a reconnect) can replay from a seq and then follow live. A client
// disconnect only ends that subscriber.
//
// Bounds (config.GenConfig): per-user active runs, per-run event log
// (bytes/events, oldest dropped -> resume gets eve.snapshot), partial reply
// size (over -> failed too_large), hub-wide runs/bytes (finished runs evicted
// first), subscribers per run, idle auto-cancel, per-write deadline.
//
// Lock order: genRun.mu may be held while taking genHub.mu, never the reverse.

var (
	errUserCap    = errors.New("user_cap")
	errHubFull    = errors.New("hub_full")
	errSubscriber = errors.New("subscribers")
	errRunning    = errors.New("running")
)

type genEvent struct {
	seq  int
	data []byte // full SSE event without the id: line, ends with "\n\n"
}

type genRun struct {
	hub                   *genHub
	user, threadID, msgID string
	ctx                   context.Context
	cancelFn              context.CancelFunc

	mu         sync.Mutex
	events     []genEvent // seqs lastSeq-len(events)+1 .. lastSeq
	lastSeq    int
	bytes      atomic.Int64
	done       bool
	finishedAt atomic.Int64  // unix nanos; 0 = running (read without mu: hub never takes run locks)
	final      string        // API status after finish (complete|failed)
	wake       chan struct{} // closed and replaced on every change
	acc        streamAcc     // partial reply, parsed from the same events the client sees
	rec        record
	nTok       int64
	partial    []byte // incomplete trailing bytes of the last Write
	overflowed bool
	sawDone    bool
	cancelCode string
	subs       int
	idleSince  time.Time

	// http.ResponseWriter for the generation code path
	hdr     http.Header
	status  int
	errBody bytes.Buffer // non-SSE (JSON error) body
}

type genHub struct {
	cfg        config.GenConfig
	m          *metrics.Metrics
	mu         sync.Mutex
	runs       map[string]*genRun
	active     map[string]int // user -> running (incl. reserved) generations
	reserved   int
	totalBytes atomic.Int64
}

func (s *Server) hub() *genHub {
	s.genOnce.Do(func() {
		c := s.Cfg.Generation
		d := config.DefaultGen()
		fill := func(p *int, v int) {
			if *p <= 0 {
				*p = v
			}
		}
		fillf := func(p *float64, v float64) {
			if *p <= 0 {
				*p = v
			}
		}
		fill(&c.UserMaxActive, d.UserMaxActive)
		fill(&c.RunMaxBytes, d.RunMaxBytes)
		fill(&c.RunMaxEvents, d.RunMaxEvents)
		fill(&c.PartialMaxBytes, d.PartialMaxBytes)
		fill(&c.HubMaxRuns, d.HubMaxRuns)
		fill(&c.HubMaxBytes, d.HubMaxBytes)
		fill(&c.MaxSubscribers, d.MaxSubscribers)
		fillf(&c.TimeoutS, d.TimeoutS)
		fillf(&c.IdleCancelS, d.IdleCancelS)
		fillf(&c.RetainS, d.RetainS)
		fillf(&c.WriteTimeoutS, d.WriteTimeoutS)
		s.gens = &genHub{cfg: c, m: s.M, runs: map[string]*genRun{}, active: map[string]int{}}
	})
	return s.gens
}

func dur(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }

func (h *genHub) gauges() { // h.mu held
	h.m.GenRuns.Set(float64(len(h.runs)))
	h.m.GenBytes.Set(float64(h.totalBytes.Load()))
}

// evictFinishedLocked drops the oldest finished run. h.mu held.
func (h *genHub) evictFinishedLocked() bool {
	var old *genRun
	var oldAt time.Time
	for _, g := range h.runs {
		if at, ok := g.finishedAtSnapshot(); ok && (old == nil || at.Before(oldAt)) {
			old, oldAt = g, at
		}
	}
	if old == nil {
		return false
	}
	h.removeLocked(old)
	h.m.GenEvictions.Inc()
	return true
}

func (h *genHub) removeLocked(g *genRun) {
	if h.runs[g.msgID] == g {
		delete(h.runs, g.msgID)
		h.totalBytes.Add(-g.bytes.Load())
	}
	h.gauges()
}

// reserve admits one more generation for user (before queue admission).
func (h *genHub) reserve(user string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[user] >= h.cfg.UserMaxActive {
		h.m.GenRejected.WithLabelValues("user_cap").Inc()
		return errUserCap
	}
	for len(h.runs)+h.reserved >= h.cfg.HubMaxRuns {
		if !h.evictFinishedLocked() {
			h.m.GenRejected.WithLabelValues("hub_full").Inc()
			return errHubFull
		}
	}
	// Running runs alone can hold more than HubMaxBytes (they are never
	// evicted); refuse new work until finished runs are freed or runs end.
	for h.totalBytes.Load() >= int64(h.cfg.HubMaxBytes) {
		if !h.evictFinishedLocked() {
			h.m.GenRejected.WithLabelValues("hub_bytes").Inc()
			return errHubFull
		}
	}
	h.active[user]++
	h.reserved++
	return nil
}

func (h *genHub) unreserve(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reserved--
	h.decUserLocked(user)
}

func (h *genHub) decUserLocked(user string) {
	if h.active[user]--; h.active[user] <= 0 {
		delete(h.active, user)
	}
}

// start turns a reservation into a run. A running run for the same message
// (concurrent regenerate) -> errRunning; the reservation is kept for the caller to release.
func (h *genHub) start(ctx context.Context, cancel context.CancelFunc, user, threadID, msgID string) (*genRun, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.runs[msgID]; old != nil {
		if _, finished := old.finishedAtSnapshot(); !finished {
			h.m.GenRejected.WithLabelValues("conflict").Inc()
			return nil, errRunning
		}
		h.removeLocked(old)
	}
	g := &genRun{hub: h, user: user, threadID: threadID, msgID: msgID, ctx: ctx, cancelFn: cancel,
		wake: make(chan struct{}), hdr: http.Header{}, idleSince: time.Now()}
	h.reserved--
	h.runs[msgID] = g
	h.gauges()
	return g, nil
}

// get returns the run only to its owner (callers turn nil into a uniform 404).
func (h *genHub) get(user, msgID string) *genRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := h.runs[msgID]
	if g == nil || g.user != user {
		return nil
	}
	return g
}

func (h *genHub) finish(g *genRun) {
	g.mu.Lock()
	if g.done {
		g.mu.Unlock()
		return
	}
	if len(bytes.TrimSpace(g.partial)) > 0 {
		g.appendLocked(append(g.partial, '\n', '\n'))
	}
	g.partial = nil
	g.done = true
	g.finishedAt.Store(time.Now().UnixNano())
	g.wakeLocked()
	g.mu.Unlock()
	h.mu.Lock()
	h.decUserLocked(g.user)
	h.mu.Unlock()
	time.AfterFunc(dur(h.cfg.RetainS), func() {
		h.mu.Lock()
		h.removeLocked(g)
		h.mu.Unlock()
	})
}

func (g *genRun) finishedAtSnapshot() (time.Time, bool) {
	n := g.finishedAt.Load()
	return time.Unix(0, n), n != 0
}

func (g *genRun) wakeLocked() {
	close(g.wake)
	g.wake = make(chan struct{})
}

// cancel stops the generation; the first code wins. Returns false if already done.
func (g *genRun) cancel(code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancelLocked(code)
}

func (g *genRun) cancelLocked(code string) bool {
	if g.done {
		return false
	}
	if g.cancelCode == "" {
		g.cancelCode = code
		g.hub.m.GenCancelled.WithLabelValues(code).Inc()
	}
	g.cancelFn()
	return true
}

// failCode is the error_code for a run that did not complete: cancel code,
// "timeout" for the run deadline, else "".
func (g *genRun) failCode() string {
	g.mu.Lock()
	c := g.cancelCode
	g.mu.Unlock()
	if c != "" {
		return c
	}
	if errors.Is(g.ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return ""
}

// --- http.ResponseWriter + Flusher used by the generation code path ---

func (g *genRun) Header() http.Header { return g.hdr }
func (g *genRun) WriteHeader(c int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == 0 {
		g.status = c
	}
}

// emitMeta appends the eve.meta event without touching the generation's HTTP
// status; it must be called before the generation goroutine starts so the
// event is always seq 1.
func (g *genRun) emitMeta(b []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.appendLocked(b)
}
func (g *genRun) Flush() {}

func (g *genRun) Write(b []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == 0 {
		g.status = 200
	}
	if g.status >= 400 {
		g.errBody.Write(b)
		return len(b), nil
	}
	g.partial = append(g.partial, b...)
	for {
		i := bytes.Index(g.partial, []byte("\n\n"))
		if i < 0 {
			break
		}
		ev := append([]byte{}, g.partial[:i+2]...)
		g.partial = g.partial[i+2:]
		g.appendLocked(ev)
	}
	return len(b), nil
}

// appendLocked stores one SSE event. Comment-only events (keepalives) are not
// stored; subscribers send their own keepalives.
func (g *genRun) appendLocked(ev []byte) {
	hasData := false
	for _, l := range bytes.Split(ev, []byte("\n")) {
		if bytes.HasPrefix(l, []byte("data:")) || bytes.HasPrefix(l, []byte("event:")) {
			hasData = true
		}
		if bytes.HasPrefix(l, []byte("data: ")) {
			if string(bytes.TrimSpace(l[6:])) == "[DONE]" {
				g.sawDone = true
			}
			parseSSEInto(l, &g.rec, &g.nTok, &g.acc)
		}
	}
	if !hasData {
		return
	}
	cfg := g.hub.cfg
	if g.acc.content.Len()+g.acc.reasoning.Len() > cfg.PartialMaxBytes && g.cancelCode == "" {
		g.hub.m.GenOverflows.WithLabelValues("too_large").Inc()
		g.cancelLocked("too_large")
	}
	g.lastSeq++
	g.events = append(g.events, genEvent{seq: g.lastSeq, data: ev})
	n := int64(len(ev))
	g.bytes.Add(n)
	g.hub.totalBytes.Add(n)
	for len(g.events) > 1 && (g.bytes.Load() > int64(cfg.RunMaxBytes) || len(g.events) > cfg.RunMaxEvents) {
		d := int64(len(g.events[0].data))
		g.events[0] = genEvent{}
		g.events = g.events[1:]
		g.bytes.Add(-d)
		g.hub.totalBytes.Add(-d)
		if !g.overflowed {
			g.overflowed = true
			g.hub.m.GenOverflows.WithLabelValues("events").Inc()
		}
	}
	if g.hub.totalBytes.Load() > int64(cfg.HubMaxBytes) {
		g.hub.mu.Lock()
		for g.hub.totalBytes.Load() > int64(cfg.HubMaxBytes) && g.hub.evictFinishedLockedExcept(g) {
		}
		g.hub.gauges()
		g.hub.mu.Unlock()
	}
	g.wakeLocked()
}

// evictFinishedLockedExcept is evictFinishedLocked for callers holding self.mu
// (self is running, so it is never a candidate; other runs' locks are free).
func (h *genHub) evictFinishedLockedExcept(self *genRun) bool {
	var old *genRun
	var oldAt time.Time
	for _, g := range h.runs {
		if g == self {
			continue
		}
		if at, ok := g.finishedAtSnapshot(); ok && (old == nil || at.Before(oldAt)) {
			old, oldAt = g, at
		}
	}
	if old == nil {
		return false
	}
	h.removeLocked(old)
	h.m.GenEvictions.Inc()
	return true
}

// emitError converts a JSON error written via s.fail into an SSE error event
// (adds retry_after from a Retry-After header). Returns code and HTTP status.
func (g *genRun) emitError() (string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status < 400 || g.errBody.Len() == 0 {
		return "", 0
	}
	body := bytes.TrimSpace(g.errBody.Bytes())
	g.errBody.Reset()
	var e map[string]any
	code := "upstream_error"
	if json.Unmarshal(body, &e) == nil {
		if em, ok := e["error"].(map[string]any); ok {
			if c, ok := em["code"].(string); ok && c != "" {
				code = c
			}
			if ra, err := strconv.Atoi(g.hdr.Get("Retry-After")); err == nil {
				em["retry_after"] = ra
			}
			body, _ = json.Marshal(e)
		}
	}
	g.appendLocked([]byte("data: " + string(body) + "\n\n"))
	return code, g.status
}

// closeOut ends a stream that stopped without [DONE] (cancel, timeout):
// error event, persisted event, [DONE].
func (g *genRun) closeOut(code string, persisted map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sawDone {
		return
	}
	e, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": "Generation ended: " + code, "type": "server_error"}})
	g.appendLocked([]byte("data: " + string(e) + "\n\n"))
	if persisted != nil {
		p, _ := json.Marshal(persisted)
		g.appendLocked([]byte("data: " + string(p) + "\n\n"))
	}
	g.appendLocked([]byte("data: [DONE]\n\n"))
}

// partialContent is the reply so far, as it would be saved.
func (g *genRun) partialContent() (content, reasoning string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acc.content.String(), g.acc.reasoning.String()
}

func (g *genRun) subscribe() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.subs >= g.hub.cfg.MaxSubscribers {
		g.hub.m.GenRejected.WithLabelValues("subscribers").Inc()
		return errSubscriber
	}
	g.subs++
	return nil
}

func (g *genRun) unsubscribe() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.subs--; g.subs == 0 {
		g.idleSince = time.Now()
	}
}

// watchIdle auto-cancels the run when nobody has been subscribed for IdleCancelS.
func (g *genRun) watchIdle() {
	idle := dur(g.hub.cfg.IdleCancelS)
	step := idle / 5
	if step > 30*time.Second {
		step = 30 * time.Second
	}
	if step < 10*time.Millisecond {
		step = 10 * time.Millisecond
	}
	t := time.NewTicker(step)
	defer t.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-t.C:
			g.mu.Lock()
			if g.done {
				g.mu.Unlock()
				return
			}
			if g.subs == 0 && time.Since(g.idleSince) >= idle {
				g.cancelLocked("no_subscriber")
				g.mu.Unlock()
				return
			}
			g.mu.Unlock()
		}
	}
}

// follow writes events with seq > after to w, then follows live until the run
// is done or the client goes away. Cursors older than the retained log get
// an eve.snapshot (partial message so far) and continue after it. Returns
// false if the client went away. Caller holds a subscription.
func (g *genRun) follow(w http.ResponseWriter, r *http.Request, after int) bool {
	fl, _ := w.(http.Flusher)
	rc := http.NewResponseController(w)
	wt := dur(g.hub.cfg.WriteTimeoutS)
	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(wt))
		_, err := w.Write(b)
		return err == nil
	}
	ka := time.NewTicker(15 * time.Second)
	defer ka.Stop()
	next := after
	for {
		g.mu.Lock()
		var batch []genEvent
		var snap []byte
		first := g.lastSeq - len(g.events) + 1
		if next < first-1 {
			// Cursor older than the retained log: one snapshot of everything so
			// far (same accumulation as the saved copy), then continue live.
			v := map[string]any{"id": g.msgID, "thread_id": g.threadID, "role": "assistant", "content": g.acc.content.String(),
				"reasoning": nilIfEmpty(g.acc.reasoning.String()), "status": apiGenerating, "last_event_id": g.lastSeq}
			if g.done {
				v["status"] = g.final
			}
			b, _ := json.Marshal(v)
			snap = []byte("id: " + strconv.Itoa(g.lastSeq) + "\nevent: eve.snapshot\ndata: " + string(b) + "\n\n")
			if g.done {
				snap = append(snap, "data: [DONE]\n\n"...)
			}
			next = g.lastSeq
		}
		if next < g.lastSeq {
			batch = append(batch, g.events[next-first+1:]...)
		}
		done, wake := g.done, g.wake
		g.mu.Unlock()
		if snap != nil && !write(snap) {
			return false
		}
		for _, e := range batch {
			if !write([]byte("id: "+strconv.Itoa(e.seq)+"\n")) || !write(e.data) {
				return false
			}
			next = e.seq
		}
		if fl != nil && (len(batch) > 0 || snap != nil) {
			_ = rc.SetWriteDeadline(time.Now().Add(wt))
			fl.Flush()
		}
		if done && len(batch) == 0 {
			if fl != nil && snap != nil {
				fl.Flush()
			}
			return true
		}
		if len(batch) > 0 {
			continue
		}
		select {
		case <-wake:
		case <-ka.C:
			if !write([]byte(": keepalive\n\n")) {
				return false
			}
			if fl != nil {
				fl.Flush()
			}
		case <-r.Context().Done():
			return false
		}
	}
}
