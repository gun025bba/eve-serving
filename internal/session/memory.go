package session

import (
	"context"
	"sort"

	"sync"
	"time"
)

// Memory is an in-process Store (tests, and the no-database dev mode).
type Memory struct {
	mu       sync.Mutex
	users    map[string]int64
	codes    map[string]memCode
	sessions map[int64]*Session
	byHash   map[string]int64
	seq      int64
}

type memCode struct {
	uid     int64
	expires time.Time
}

func NewMemory() *Memory {
	return &Memory{users: map[string]int64{}, codes: map[string]memCode{}, sessions: map[int64]*Session{}, byHash: map[string]int64{}}
}

func (m *Memory) UserIDByLogin(_ context.Context, login string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.users[login]; ok {
		return id, nil
	}
	m.seq++
	m.users[login] = m.seq
	return m.seq, nil
}

func (m *Memory) login(uid int64) string {
	for l, id := range m.users {
		if id == uid {
			return l
		}
	}
	return ""
}

func (m *Memory) PutPairCode(_ context.Context, uid int64, h string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[h] = memCode{uid, exp}
	return nil
}

func (m *Memory) TakePairCode(_ context.Context, h string, now time.Time) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[h]
	delete(m.codes, h)
	if !ok || !now.Before(c.expires) {
		return 0, false, nil
	}
	return c.uid, true, nil
}

func (m *Memory) DeleteAllPairCodes(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes = map[string]memCode{}
	return nil
}

func (m *Memory) CreateSession(_ context.Context, uid int64, h string, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.sessions[m.seq] = &Session{ID: m.seq, UserID: uid, CreatedAt: now, LastSeen: now}
	m.byHash[h] = m.seq
	return m.seq, nil
}

func (m *Memory) SessionByHash(_ context.Context, h string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byHash[h]
	if !ok {
		return Session{}, ErrNotFound
	}
	s := *m.sessions[id]
	s.Login = m.login(s.UserID)
	return s, nil
}

func (m *Memory) TouchSession(_ context.Context, id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.LastSeen = now
	}
	return nil
}

func (m *Memory) RevokeSession(_ context.Context, id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if s.RevokedAt == nil {
		s.RevokedAt = &now
	}
	return nil
}

func (m *Memory) ListSessions(context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for _, s := range m.sessions {
		c := *s
		c.Login = m.login(c.UserID)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
