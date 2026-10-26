package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Detached generation (docs/architecture/ui-api-contract.md §3.8).
//
// A persisted chat runs in a background goroutine whose context is detached
// from the HTTP request. Its SSE output is written into a genRun: every SSE
// event gets a monotonic seq (1, 2, ...) and is kept in memory so any number of
// subscribers (the original request, or GET .../messages/{mid}/stream after a
// reconnect) can replay from a seq and then follow live. A client disconnect
// only ends that subscriber; generation runs to completion and is saved.

// genRetain is how long a finished run stays replayable from memory.
var genRetain atomic.Int64 // nanoseconds; tests shorten it

func init() { genRetain.Store(int64(10 * time.Minute)) }

// genTimeout bounds one detached generation (all rounds, queue wait included).
var genTimeout = 15 * time.Minute

type genEvent struct {
	seq  int
	data []byte // full SSE event without the id: line, ends with "\n\n"
}

type genRun struct {
	user, threadID, msgID string

	mu      sync.Mutex
	events  []genEvent
	done    bool
	wake    chan struct{} // closed and replaced on every change
	acc     streamAcc     // partial reply, parsed from the same events the client sees
	rec     record
	nTok    int64
	partial []byte // incomplete trailing bytes of the last Write

	// http.ResponseWriter for the generation code path
	hdr     http.Header
	status  int
	errBody bytes.Buffer // non-SSE (JSON error) body
}

type genHub struct {
	mu   sync.Mutex
	runs map[string]*genRun
}

func (s *Server) hub() *genHub {
	s.genOnce.Do(func() { s.gens = &genHub{runs: map[string]*genRun{}} })
	return s.gens
}

func (h *genHub) start(user, threadID, msgID string) *genRun {
	g := &genRun{user: user, threadID: threadID, msgID: msgID, wake: make(chan struct{}), hdr: http.Header{}}
	h.mu.Lock()
	h.runs[msgID] = g
	h.mu.Unlock()
	return g
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
	if !g.done {
		if len(bytes.TrimSpace(g.partial)) > 0 {
			g.appendLocked(append(g.partial, '\n', '\n'))
		}
		g.partial = nil
		g.done = true
		close(g.wake)
		g.wake = make(chan struct{})
	}
	g.mu.Unlock()
	time.AfterFunc(time.Duration(genRetain.Load()), func() {
		h.mu.Lock()
		if h.runs[g.msgID] == g {
			delete(h.runs, g.msgID)
		}
		h.mu.Unlock()
	})
}

// --- http.ResponseWriter + Flusher used by the generation goroutine ---

func (g *genRun) Header() http.Header { return g.hdr }
func (g *genRun) WriteHeader(c int) {
	if g.status == 0 {
		g.status = c
	}
}
func (g *genRun) Flush() {}

func (g *genRun) Write(b []byte) (int, error) {
	if g.status == 0 {
		g.status = 200
	}
	g.mu.Lock()
	defer g.mu.Unlock()
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
			parseSSEInto(l, &g.rec, &g.nTok, &g.acc)
		}
	}
	if !hasData {
		return
	}
	g.events = append(g.events, genEvent{seq: len(g.events) + 1, data: ev})
	close(g.wake)
	g.wake = make(chan struct{})
}

// emitError converts a JSON error written via s.fail into an SSE error event.
func (g *genRun) emitError() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status < 400 || g.errBody.Len() == 0 {
		return ""
	}
	body := bytes.TrimSpace(g.errBody.Bytes())
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	g.errBody.Reset()
	g.appendLocked([]byte("data: " + string(body) + "\n\n"))
	g.appendLocked([]byte("data: [DONE]\n\n"))
	return e.Error.Code
}

// partialContent is the reply so far, as it would be saved.
func (g *genRun) partialContent() (content, reasoning string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acc.content.String(), g.acc.reasoning.String()
}

// follow writes events with seq > after to w, then follows live until the run
// is done or the client goes away. Returns false if the client went away.
func (g *genRun) follow(w http.ResponseWriter, r *http.Request, after int) bool {
	fl, _ := w.(http.Flusher)
	ka := time.NewTicker(15 * time.Second)
	defer ka.Stop()
	next := after
	for {
		g.mu.Lock()
		var batch []genEvent
		if next < len(g.events) {
			batch = append(batch, g.events[next:]...)
		}
		done, wake := g.done, g.wake
		g.mu.Unlock()
		for _, e := range batch {
			if _, err := w.Write([]byte("id: " + strconv.Itoa(e.seq) + "\n")); err != nil {
				return false
			}
			if _, err := w.Write(e.data); err != nil {
				return false
			}
			next = e.seq
		}
		if fl != nil && len(batch) > 0 {
			fl.Flush()
		}
		if done && len(batch) == 0 {
			return true
		}
		if len(batch) > 0 {
			continue
		}
		select {
		case <-wake:
		case <-ka.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
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
