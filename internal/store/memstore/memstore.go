// Package memstore is an in-memory store.Store for handler unit tests.
package memstore

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

type thread struct {
	store.Thread
	user string
}
type msg struct {
	store.Message
	user string
}

type Store struct {
	mu       sync.Mutex
	threads  map[string]*thread
	msgs     map[string]*msg
	feedback map[string]*store.Feedback
	clock    func() time.Time
	calls    map[string][]store.ToolCall
	sources  map[string][]store.WebSource
	settings map[string][]byte
	actions  map[string]*action
	pushSubs map[string]*pushSub // endpoint -> sub
}

var _ store.Store = (*Store)(nil)

func New() *Store {
	return &Store{threads: map[string]*thread{}, msgs: map[string]*msg{}, feedback: map[string]*store.Feedback{}, calls: map[string][]store.ToolCall{}, sources: map[string][]store.WebSource{}, settings: map[string][]byte{}, actions: map[string]*action{}, pushSubs: map[string]*pushSub{}, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

func (s *Store) view(t *thread) store.Thread {
	out := t.Thread
	var last *msg
	n := 0
	out.UnreadCount = 0
	for _, m := range s.msgs {
		if m.ThreadID == t.ID {
			n++
			if m.Origin == store.OriginProactive && m.ReadAt == nil && m.user == t.user {
				out.UnreadCount++
			}
			if last == nil || m.CreatedAt.After(last.CreatedAt) || (m.CreatedAt.Equal(last.CreatedAt) && m.ID > last.ID) {
				last = m
			}
		}
	}
	out.MessageCount = n
	if last != nil {
		p := store.Preview(last.Content, 120)
		out.LastMessagePreview = &p
	}
	return out
}

func (s *Store) CreateThread(_ context.Context, user, title string) (store.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	t := &thread{store.Thread{ID: store.NewID(now), Title: title, CreatedAt: now, UpdatedAt: now}, user}
	s.threads[t.ID] = t
	return s.view(t), nil
}

func (s *Store) getT(user, id string) (*thread, error) {
	t, ok := s.threads[id]
	if !ok || t.user != user {
		return nil, store.ErrNotFound
	}
	return t, nil
}

func (s *Store) GetThread(_ context.Context, user, id string) (store.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getT(user, id)
	if err != nil {
		return store.Thread{}, err
	}
	return s.view(t), nil
}

func (s *Store) ListThreads(ctx context.Context, user string, archived bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	return s.listThreads(user, archived, false, limit, after)
}

func (s *Store) ListUnreadThreads(ctx context.Context, user string, archived bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	return s.listThreads(user, archived, true, limit, after)
}

func (s *Store) listThreads(user string, archived, unread bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ts []*thread
	for _, t := range s.threads {
		if t.user != user || t.Archived != archived {
			continue
		}
		if unread && s.view(t).UnreadCount == 0 {
			continue
		}
		if after != nil && !(t.UpdatedAt.Before(after.UpdatedAt) || (t.UpdatedAt.Equal(after.UpdatedAt) && t.ID < after.ID)) {
			continue
		}
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool {
		if !ts[i].UpdatedAt.Equal(ts[j].UpdatedAt) {
			return ts[i].UpdatedAt.After(ts[j].UpdatedAt)
		}
		return ts[i].ID > ts[j].ID
	})
	out := []store.Thread{}
	for i := 0; i < len(ts) && i < limit; i++ {
		out = append(out, s.view(ts[i]))
	}
	return out, nil
}

func (s *Store) UpdateThread(_ context.Context, user, id string, title *string, archived *bool) (store.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getT(user, id)
	if err != nil {
		return store.Thread{}, err
	}
	if title != nil {
		t.Title = *title
	}
	if archived != nil {
		t.Archived = *archived
	}
	t.UpdatedAt = s.clock()
	return s.view(t), nil
}

func (s *Store) ImportThread(_ context.Context, user string, it store.ImportThread) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.threads {
		if t.user == user && t.ClientID != nil && *t.ClientID == it.ClientID {
			return t.ID, false, nil
		}
	}
	cid := it.ClientID
	upd := it.CreatedAt
	t := &thread{store.Thread{ID: store.NewID(it.CreatedAt), Title: it.Title, CreatedAt: it.CreatedAt, Archived: it.Archived, ClientID: &cid}, user}
	for _, m := range it.Messages {
		if m.CreatedAt.After(upd) {
			upd = m.CreatedAt
		}
		if m.ClientID != nil && s.msgClient(user, *m.ClientID) {
			continue
		}
		mm := &msg{store.Message{ID: store.NewID(m.CreatedAt), ThreadID: t.ID, Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, Model: m.Model,
			Status: store.StatusComplete, ClientID: m.ClientID, CreatedAt: m.CreatedAt}, user}
		s.msgs[mm.ID] = mm
	}
	t.UpdatedAt = upd
	s.threads[t.ID] = t
	return t.ID, true, nil
}

func (s *Store) msgClient(user, cid string) bool {
	for _, m := range s.msgs {
		if m.user == user && m.ClientID != nil && *m.ClientID == cid {
			return true
		}
	}
	return false
}

func (s *Store) AddMessage(_ context.Context, user string, m store.Message) (store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getT(user, m.ThreadID)
	if err != nil {
		return m, err
	}
	if !slices.Contains([]string{"system", "user", "assistant", "tool"}, m.Role) {
		return m, store.ErrInvalid
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.clock()
	}
	if m.ID == "" {
		m.ID = store.NewID(m.CreatedAt)
	}
	if m.Status == "" {
		m.Status = store.StatusComplete
	}
	if m.Origin == store.OriginProactive {
		if m.JobID == nil {
			return m, store.ErrInvalid
		}
		var jn *string
		if m.Provenance != nil {
			jn = m.Provenance.JobName
		}
		m.Provenance = &store.Provenance{Actor: "scheduler", JobID: m.JobID, JobName: jn}
	} else {
		m.Origin = store.APIOrigin(store.OriginUserTurn, m.Role)
		m.JobID, m.TemplateKind, m.ReadAt, m.Provenance = nil, nil, nil, nil
	}
	s.msgs[m.ID] = &msg{m, user}
	if m.CreatedAt.After(t.UpdatedAt) {
		t.UpdatedAt = m.CreatedAt
	}
	return m, nil
}

func (s *Store) ReplaceMessage(_ context.Context, user string, m store.Message) (store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.msgs[m.ID]
	if !ok || old.user != user {
		return m, store.ErrNotFound
	}
	if m.Status == "" {
		m.Status = store.StatusComplete
	}
	now := s.clock()
	o := &old.Message
	o.Content, o.Reasoning, o.Model, o.Params, o.Usage, o.LatencyMs, o.Metadata, o.Status, o.UpdatedAt =
		m.Content, m.Reasoning, m.Model, m.Params, m.Usage, m.LatencyMs, m.Metadata, m.Status, &now
	delete(s.feedback, m.ID)
	delete(s.calls, m.ID)
	delete(s.sources, m.ID)
	s.threads[o.ThreadID].UpdatedAt = now
	return *o, nil
}

func (s *Store) withFB(m store.Message) store.Message {
	if m.Origin == "" {
		m.Origin = store.APIOrigin(store.OriginUserTurn, m.Role)
	}
	if f, ok := s.feedback[m.ID]; ok {
		c := *f
		m.Feedback = &c
	}
	return m
}

func (s *Store) GetMessage(_ context.Context, user, id string) (store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.user != user {
		return store.Message{}, store.ErrNotFound
	}
	return s.withFB(m.Message), nil
}

func less(a, b store.Message) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func (s *Store) ListMessages(_ context.Context, user, threadID string, limit int, before string) ([]store.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getT(user, threadID); err != nil {
		return nil, false, err
	}
	var bm *store.Message
	if before != "" {
		m, ok := s.msgs[before]
		if !ok || m.user != user || m.ThreadID != threadID {
			return nil, false, store.ErrNotFound
		}
		bm = &m.Message
	}
	var all []store.Message
	for _, m := range s.msgs {
		if m.ThreadID == threadID && (bm == nil || less(m.Message, *bm)) {
			all = append(all, s.withFB(m.Message))
		}
	}
	sort.Slice(all, func(i, j int) bool { return less(all[i], all[j]) })
	more := len(all) > limit
	if more {
		all = all[len(all)-limit:]
	}
	if all == nil {
		all = []store.Message{}
	}
	return all, more, nil
}

func (s *Store) SetFeedback(_ context.Context, user, id, rating string, reasons []string, comment *string) (store.Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.user != user {
		return store.Feedback{}, store.ErrNotFound
	}
	now := s.clock()
	f, ok := s.feedback[id]
	if !ok {
		f = &store.Feedback{MessageID: id, CreatedAt: now}
		s.feedback[id] = f
	}
	if reasons == nil {
		reasons = []string{}
	}
	f.Rating, f.Reasons, f.Comment, f.UpdatedAt = rating, append([]string{}, reasons...), comment, now
	return *f, nil
}

func (s *Store) DeleteFeedback(_ context.Context, user, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.user != user {
		return store.ErrNotFound
	}
	delete(s.feedback, id)
	return nil
}

func (s *Store) Search(_ context.Context, user, q string, limit, offset int) ([]store.SearchHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lq := strings.ToLower(q)
	var hits []store.SearchHit
	for _, t := range s.threads {
		if t.user == user && !t.Archived && strings.Contains(strings.ToLower(t.Title), lq) {
			hits = append(hits, store.SearchHit{ThreadID: t.ID, ThreadTitle: t.Title, Snippet: store.Snippet(t.Title, q, 160), CreatedAt: t.UpdatedAt, Match: "title"})
		}
	}
	for _, m := range s.msgs {
		t := s.threads[m.ThreadID]
		if m.user == user && !t.Archived && strings.Contains(strings.ToLower(m.Content), lq) {
			id, role := m.ID, m.Role
			hits = append(hits, store.SearchHit{ThreadID: t.ID, ThreadTitle: t.Title, MessageID: &id, Role: &role, Snippet: store.Snippet(m.Content, q, 160), CreatedAt: m.CreatedAt, Match: "content"})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Match != hits[j].Match {
			return hits[i].Match == "title"
		}
		return hits[i].CreatedAt.After(hits[j].CreatedAt)
	})
	if offset >= len(hits) {
		return []store.SearchHit{}, nil
	}
	hits = hits[offset:]
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *Store) ExportFeedback(_ context.Context, since time.Time, fn func(store.FeedbackExport) error) error {
	s.mu.Lock()
	var out []store.FeedbackExport
	var ts []time.Time
	for id, f := range s.feedback {
		m := s.msgs[id]
		if m == nil || f.UpdatedAt.Before(since) {
			continue
		}
		e := store.FeedbackExport{Response: m.Content, Rating: f.Rating, Reasons: append([]string{}, f.Reasons...), Comment: f.Comment, CreatedAt: f.CreatedAt, ThreadID: m.ThreadID, MessageID: id}
		var best *msg
		for _, u := range s.msgs {
			if u.ThreadID == m.ThreadID && u.Role == "user" && u.CreatedAt.Before(m.CreatedAt) && (best == nil || u.CreatedAt.After(best.CreatedAt)) {
				best = u
			}
		}
		if best != nil {
			e.Message = best.Content
		}
		out = append(out, e)
		ts = append(ts, f.UpdatedAt)
	}
	s.mu.Unlock()
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		if !ts[idx[a]].Equal(ts[idx[b]]) {
			return ts[idx[a]].Before(ts[idx[b]])
		}
		return out[idx[a]].MessageID < out[idx[b]].MessageID
	})
	for _, i := range idx {
		if err := fn(out[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetMessageStatus(_ context.Context, user, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.user != user {
		return store.ErrNotFound
	}
	m.Status = status
	return nil
}

func (s *Store) FailStaleGenerating(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, m := range s.msgs {
		if m.Status == store.StatusGenerating {
			m.Status = store.StatusFailed
			m.Metadata = []byte(`{"incomplete":true,"reason":"gateway_restart","error_code":"gateway_restart"}`)
			n++
		}
	}
	return n, nil
}
