package memstore

import (
	"context"
	"encoding/json"
)

func (s *Store) UserSettings(_ context.Context, login string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.users[login]
	if m == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(m)
}

func (s *Store) MergeUserSettings(_ context.Context, login, ns string, patch json.RawMessage) (json.RawMessage, error) {
	var p map[string]any
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[string]map[string]any{}
	}
	m := s.users[login]
	if m == nil {
		m = map[string]any{}
		s.users[login] = m
	}
	cur, _ := m[ns].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	for k, v := range p {
		cur[k] = v
	}
	m[ns] = cur
	return json.Marshal(m)
}
