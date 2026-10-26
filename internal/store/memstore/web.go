package memstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gun025bba/llm-serving/internal/store"
)

var _ store.WebStore = (*Store)(nil)
var _ store.ThreadSettingsStore = (*Store)(nil)

func (s *Store) SaveWebRun(_ context.Context, user, id string, calls []store.ToolCall, srcs []store.WebSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.user != user {
		return store.ErrNotFound
	}
	now := s.clock()
	cs := make([]store.ToolCall, len(calls))
	for i, c := range calls {
		c.MessageID, c.ID = id, store.NewID(now)
		if c.CreatedAt.IsZero() {
			c.CreatedAt = now
		}
		cs[i] = c
	}
	ss := make([]store.WebSource, len(srcs))
	for i, w := range srcs {
		w.MessageID = id
		if w.CreatedAt.IsZero() {
			w.CreatedAt = now
		}
		ss[i] = w
	}
	s.calls[id], s.sources[id] = cs, ss
	return nil
}

func (s *Store) Sources(_ context.Context, user string, ids []string) (map[string][]store.WebSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]store.WebSource{}
	for _, id := range ids {
		if m, ok := s.msgs[id]; ok && m.user == user && len(s.sources[id]) > 0 {
			out[id] = append([]store.WebSource{}, s.sources[id]...)
		}
	}
	return out, nil
}

// ToolCalls is a test helper.
func (s *Store) ToolCalls(id string) []store.ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.ToolCall{}, s.calls[id]...)
}

func (s *Store) PurgeWebHistory(_ context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, cs := range s.calls {
		keep := cs[:0]
		for _, c := range cs {
			if c.CreatedAt.Before(cutoff) {
				n++
			} else {
				keep = append(keep, c)
			}
		}
		s.calls[id] = keep
	}
	for id, ws := range s.sources {
		keep := ws[:0]
		for _, w := range ws {
			if w.CreatedAt.Before(cutoff) {
				n++
			} else {
				keep = append(keep, w)
			}
		}
		s.sources[id] = keep
	}
	return n, nil
}

func (s *Store) ThreadSettings(_ context.Context, user, id string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getT(user, id); err != nil {
		return nil, err
	}
	if b := s.settings[id]; b != nil {
		return b, nil
	}
	return json.RawMessage(`{}`), nil
}

func (s *Store) SetThreadSettings(_ context.Context, user, id string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getT(user, id); err != nil {
		return err
	}
	s.settings[id] = append([]byte{}, v...)
	return nil
}
