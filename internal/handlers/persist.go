package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/store"
)

// chatPersist tracks one chat request that is saved to a thread.
type chatPersist struct {
	s           *Server
	user        string
	threadID    string
	newThread   bool   // thread created by this request (thread_id "new"): no stored history yet
	userMsgID   string // "" when regenerating
	replaceID   string // assistant message being regenerated
	model       string
	params      json.RawMessage
	t0          time.Time
	done        bool
	msgID       string
	placed      bool          // assistant row created up front (status generating)
	failCode    func() string // detached run: cancel/timeout code overrides the reason
	release     func()        // drops the in-flight turn mark (#30); nil when none
	inflightKey string
}

// releaseInflight drops the in-flight mark for this turn. Idempotent; called
// when the chat handler returns (completion, error or client cancel).
func (p *chatPersist) releaseInflight() {
	if p != nil && p.release != nil {
		p.release()
		p.release = nil
	}
}

// errTurnInFlight: the same user turn is already being answered (#30).
// message_id is the assistant reply being generated (attach via
// GET /v1/threads/{thread_id}/messages/{message_id}/stream); it is empty
// in the short window before that row exists. user_message_id is the turn.
func errTurnInFlight(threadID, userMsgID, msgID string) *apierr.Error {
	e := apierr.New(409, "generation_in_progress", "This message is already being answered; reattach to message_id or retry after it finishes", "invalid_request_error")
	e.Extra = map[string]any{"thread_id": threadID, "message_id": msgID, "user_message_id": userMsgID}
	return e
}

// startPlaceholder creates the assistant row (or marks the regenerated one)
// with status "generating" so a reconnecting client can find it.
func (p *chatPersist) startPlaceholder(r *http.Request) error {
	ctx := r.Context()
	if p.replaceID != "" {
		if gs, ok := p.s.Store.(store.GenStore); ok {
			if err := gs.MarkGenerating(ctx, p.user, p.replaceID); err != nil {
				return storeErr(err, "Message")
			}
		}
		p.msgID, p.placed = p.replaceID, true
		return nil
	}
	m, err := p.s.Store.AddMessage(ctx, p.user, store.Message{ThreadID: p.threadID, Role: "assistant", Model: nz(p.model),
		Params: p.params, Status: store.StatusGenerating})
	if err != nil {
		return storeErr(err, "Thread")
	}
	p.msgID, p.placed = m.ID, true
	return nil
}

// fail saves a failed status if nothing was saved yet.
func (p *chatPersist) fail(r *http.Request, code string) {
	if p == nil || p.done {
		return
	}
	p.finish(r, "", "", "", nil, nil, store.StatusFailed, code)
}

// textOf flattens OpenAI message content (string or [{type:text,text}]).
func textOf(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

var notParams = map[string]bool{"messages": true, "model": true, "stream": true, "stream_options": true}

// beginPersist strips thread_id/replace_message_id from body and, when a
// thread is requested, resolves/creates it and saves the user message right
// away (before the upstream call). Returns nil when nothing is persisted.
func (s *Server) beginPersist(r *http.Request, body map[string]any) (*chatPersist, error) {
	tidRaw, hasT := body["thread_id"]
	repRaw, hasR := body["replace_message_id"]
	delete(body, "thread_id")
	delete(body, "replace_message_id")
	if (!hasT || tidRaw == nil) && (!hasR || repRaw == nil) {
		return nil, nil
	}
	tid, _ := tidRaw.(string)
	rep, _ := repRaw.(string)
	if tid == "" {
		return nil, apierr.BadRequest("'thread_id' must be a thread id or \"new\" (required with replace_message_id)")
	}
	if s.Store == nil {
		return nil, persistenceDisabled()
	}
	ctx := r.Context()
	p := &chatPersist{s: s, user: user(r), t0: time.Now()}
	p.model, _ = body["model"].(string)
	pm := map[string]any{}
	for k, v := range body {
		if !notParams[k] {
			pm[k] = v
		}
	}
	p.params, _ = json.Marshal(pm)

	msgs, _ := body["messages"].([]any)
	var lastUser string
	found := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if m, ok := msgs[i].(map[string]any); ok && m["role"] == "user" {
			lastUser, found = textOf(m["content"]), true
			break
		}
	}
	if tid == "new" {
		if rep != "" {
			return nil, apierr.BadRequest("'replace_message_id' needs an existing thread_id")
		}
		t, err := s.Store.CreateThread(ctx, p.user, store.Preview(lastUser, 40))
		if err != nil {
			return nil, err
		}
		p.threadID = t.ID
		p.newThread = true
	} else {
		if _, err := s.Store.GetThread(ctx, p.user, tid); err != nil {
			return nil, storeErr(err, "Thread")
		}
		p.threadID = tid
	}
	if rep != "" {
		m, err := s.Store.GetMessage(ctx, p.user, rep)
		if err != nil || m.ThreadID != p.threadID {
			return nil, notFound("Message")
		}
		if m.Role != "assistant" {
			return nil, apierr.BadRequest("'replace_message_id' must be an assistant message")
		}
		p.replaceID = rep
		return p, nil
	}
	if !found {
		return nil, apierr.BadRequest("A user message is required to save to a thread")
	}
	if tid != "new" {
		// Serialize "check last turn, then insert" per thread so two identical
		// resends arriving together cannot both miss the dedup and insert twice.
		// The gateway is a single process, so an in-process lock is enough;
		// a multi-instance deployment would need SELECT ... FOR UPDATE on the
		// thread row instead.
		unlock := s.threadLocks.lock(p.user + "\x00" + p.threadID)
		defer unlock()
		// #30: the same user message (same thread, same user message_id)
		// already being answered must not start a second answer. No
		// Idempotency-Key header: the resent turn resolves to the saved user
		// message, and the in-flight mark / generating row is keyed on it.
		key := p.user + "\x00" + p.threadID + "\x00" + lastUser
		if uid, ok := s.inflight.get(key); ok {
			s.Log.Info("persist_retry_inflight", "request_id", rid(r), "thread_id", p.threadID, "user_message_id", uid)
			return nil, errTurnInFlight(p.threadID, uid, "")
		}
		if uid, aid, ok := s.lastTurnGenerating(ctx, p.user, p.threadID, lastUser); ok {
			s.Log.Info("persist_retry_inflight", "request_id", rid(r), "thread_id", p.threadID, "user_message_id", uid, "message_id", aid)
			return nil, errTurnInFlight(p.threadID, uid, aid)
		}
		p.release = s.inflight.add(key)
		p.inflightKey = key
		if uid, failedID, ok := s.retryTarget(ctx, p.user, p.threadID, lastUser); ok {
			// Client resent the same turn without replace_message_id (e.g. after
			// "Load failed"): reuse the saved user message instead of storing it
			// twice, and overwrite the failed reply if there is one.
			p.userMsgID, p.replaceID = uid, failedID
			s.inflight.setID(key, uid)
			s.Log.Info("persist_retry_dedup", "request_id", rid(r), "thread_id", p.threadID, "user_message_id", uid, "replaced", failedID != "")
			return p, nil
		}
	}
	um, err := s.Store.AddMessage(ctx, p.user, store.Message{ThreadID: p.threadID, Role: "user", Content: lastUser})
	if err != nil {
		p.releaseInflight()
		return nil, storeErr(err, "Thread")
	}
	p.userMsgID = um.ID
	if p.inflightKey != "" {
		s.inflight.setID(p.inflightKey, um.ID)
	}
	return p, nil
}

// retryTarget reports whether a new user turn is a resend of the thread's last
// user message: the thread ends with a user message with identical content,
// either with no reply after it or followed only by one failed/incomplete
// (incl. cancelled) assistant reply. A generating or complete reply, or any
// proactive/other row in between, means it is a genuinely new turn.
func (s *Server) retryTarget(ctx context.Context, user, threadID, content string) (userID, failedID string, ok bool) {
	ms, _, err := s.Store.ListMessages(ctx, user, threadID, 2, "")
	if err != nil || len(ms) == 0 {
		return "", "", false
	}
	isUser := func(m store.Message) bool {
		return m.Role == "user" && m.Origin != store.OriginProactive && m.Content == content
	}
	last := ms[len(ms)-1]
	if isUser(last) {
		return last.ID, "", true
	}
	if len(ms) == 2 && last.Role == "assistant" && (last.Status == store.StatusFailed || last.Status == store.StatusIncomplete) && isUser(ms[0]) {
		return ms[0].ID, last.ID, true
	}
	return "", "", false
}

// lastTurnGenerating reports whether the thread ends with the same user
// message followed by a reply that a live run is still generating (#30).
// A stale generating row with no run in memory stays a new turn (#24).
// It returns the user message_id and the generating reply's message_id.
func (s *Server) lastTurnGenerating(ctx context.Context, user, threadID, content string) (userMsgID, msgID string, ok bool) {
	ms, _, err := s.Store.ListMessages(ctx, user, threadID, 2, "")
	if err != nil || len(ms) != 2 {
		return "", "", false
	}
	u, a := ms[0], ms[1]
	if u.Role == "user" && u.Origin != store.OriginProactive && u.Content == content &&
		a.Role == "assistant" && a.Status == store.StatusGenerating && s.hub().get(user, a.ID) != nil {
		return u.ID, a.ID, true
	}
	return "", "", false
}

func (p *chatPersist) setHeaders(h http.Header) {
	h.Set("X-Thread-Id", p.threadID)
	if p.userMsgID != "" {
		h.Set("X-User-Message-Id", p.userMsgID)
	}
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// finish saves the assistant reply (new message, or overwrite on regenerate).
// Uses a context detached from the request so a disconnect still persists.
func (p *chatPersist) finish(r *http.Request, content, reasoning, model string, pt, ct *int64, status, reason string) {
	if p == nil || p.done {
		return
	}
	p.done = true
	if model == "" {
		model = p.model
	}
	lat := time.Since(p.t0).Milliseconds()
	m := store.Message{ThreadID: p.threadID, Role: "assistant", Content: content, Reasoning: nz(reasoning), Model: nz(model),
		Params: p.params, LatencyMs: &lat, Status: status}
	if pt != nil || ct != nil {
		m.Usage = &store.Usage{PromptTokens: pt, CompletionTokens: ct}
	}
	if status == store.StatusIncomplete && p.placed {
		status = store.StatusFailed
	}
	if status != store.StatusComplete && p.failCode != nil {
		if c := p.failCode(); c != "" {
			reason = c
		}
	}
	m.Status = status
	if status == store.StatusIncomplete || status == store.StatusFailed {
		m.Metadata, _ = json.Marshal(map[string]any{"incomplete": true, "reason": reason, "error_code": reason})
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	var err error
	if p.placed {
		m.ID = p.msgID
		_, err = p.s.Store.ReplaceMessage(ctx, p.user, m)
	} else if p.replaceID != "" {
		m.ID = p.replaceID
		_, err = p.s.Store.ReplaceMessage(ctx, p.user, m)
		p.msgID = p.replaceID
	} else {
		m, err = p.s.Store.AddMessage(ctx, p.user, m)
		p.msgID = m.ID
	}
	if err != nil {
		if !p.placed {
			p.msgID = ""
		}
		if !errors.Is(err, context.Canceled) {
			p.s.Log.Error("persist_failed", "request_id", rid(r), "thread_id", p.threadID, "error", logTrunc(err.Error(), 300))
		}
		return
	}
	p.s.Log.Info("persisted", "request_id", rid(r), "user", logUser(r), "thread_id", p.threadID, "message_id", p.msgID, "status", status, "replaced", p.replaceID != "")
}

func (p *chatPersist) ids() map[string]any {
	out := map[string]any{"thread_id": p.threadID, "message_id": nil, "user_message_id": nil}
	if p.msgID != "" {
		out["message_id"] = p.msgID
	}
	if p.userMsgID != "" {
		out["user_message_id"] = p.userMsgID
	}
	if p.replaceID != "" {
		out["replaced_message_id"] = p.replaceID
	}
	return out
}

// apiStatus maps an internal finish status to the value clients see.
func (p *chatPersist) apiStatus(status string) string {
	if status == store.StatusIncomplete && p.placed {
		return store.StatusFailed
	}
	return status
}

// keyedLocks is a set of mutexes keyed by string, created on demand and
// dropped when the last holder/waiter releases (no unbounded growth).
type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyedLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// keyedSet marks user turns in flight (#30), remembering the user
// message_id once known. add returns an idempotent release.
type keyedSet struct {
	mu  sync.Mutex
	m   map[string]int
	ids map[string]string
}

func (k *keyedSet) get(key string) (userMsgID string, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.ids[key], k.m[key] > 0
}

func (k *keyedSet) setID(key, id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m[key] > 0 {
		k.ids[key] = id
	}
}

func (k *keyedSet) add(key string) (release func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m, k.ids = map[string]int{}, map[string]string{}
	}
	k.m[key]++
	k.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			if k.m[key]--; k.m[key] <= 0 {
				delete(k.m, key)
				delete(k.ids, key)
			}
			k.mu.Unlock()
		})
	}
}
