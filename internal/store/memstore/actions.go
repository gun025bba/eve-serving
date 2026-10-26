package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

type action struct {
	store.Action
	user string
}

type pushSub struct {
	store.PushSubscription
	user string
}

// MarkRead sets read_at on a proactive message (test helper).
func (s *Store) MarkRead(user, id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.msgs[id]; ok && m.user == user {
		m.ReadAt = &at
	}
}

func (s *Store) CreateAction(_ context.Context, user string, a store.Action) (store.Action, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.clock()
	}
	if a.ID == "" {
		a.ID = store.NewID(a.CreatedAt)
	}
	a.RootID = a.ID
	if a.ExpiresAt.IsZero() || a.ExpiresAt.After(a.CreatedAt.Add(store.ProposalMaxAge)) {
		a.ExpiresAt = a.CreatedAt.Add(store.ProposalMaxAge)
	}
	if len(a.PayloadView) == 0 {
		a.PayloadView = []byte(`{}`)
	}
	if a.Scopes == nil {
		a.Scopes = []string{}
	}
	a.PayloadHash = store.PayloadHash(a.Payload)
	a.Status = "pending"
	s.actions[a.ID] = &action{a, user}
	return a, nil
}

func (s *Store) ListPendingActions(_ context.Context, user string) ([]store.Action, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.Action{}
	for _, a := range s.actions {
		if a.user == user && a.Status == "pending" {
			out = append(out, a.Action)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) owned(user, id string) (*action, error) {
	a, ok := s.actions[id]
	if !ok || a.user != user {
		return nil, store.ErrNotFound
	}
	return a, nil
}

func (s *Store) chainIssued(user, root string) int {
	n := 0
	for _, a := range s.actions {
		if a.user == user && a.RootID == root {
			n += a.IssueCount
		}
	}
	return n
}

func (s *Store) ReissueAction(_ context.Context, user, id string, now time.Time) (store.Action, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.owned(user, id)
	if err != nil {
		return store.Action{}, "", err
	}
	if a.Status != "pending" {
		return a.Action, "", store.ErrConflict
	}
	if !now.Before(a.ExpiresAt) {
		return a.Action, "", store.ErrExpired
	}
	if s.chainIssued(user, a.RootID) >= store.ReissueCap {
		return a.Action, "", store.ErrCapExceeded
	}
	tok, h := store.NewToken()
	texp := store.TokenExpiry(now, a.ExpiresAt)
	a.IssueCount++
	a.TokenSHA256, a.TokenExpiresAt = &h, &texp
	return a.Action, tok, nil
}

func (s *Store) ReviseAction(_ context.Context, user, id string, itemIDs []string, now time.Time) (store.Action, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, err := s.owned(user, id)
	if err != nil {
		return store.Action{}, "", err
	}
	rc := orig.CreatedAt
	if r, ok := s.actions[orig.RootID]; ok {
		rc = r.CreatedAt
	}
	if !now.Before(rc.Add(store.ProposalMaxAge)) {
		return orig.Action, "", store.ErrExpired
	}
	if orig.Status != "pending" {
		return orig.Action, "", store.ErrConflict
	}
	payload, err := store.FilterItems(orig.Payload, itemIDs)
	if err != nil {
		return orig.Action, "", err
	}
	view, err := store.FilterItems(orig.PayloadView, itemIDs)
	if err != nil {
		return orig.Action, "", err
	}
	if s.chainIssued(user, orig.RootID) >= store.ReissueCap {
		return orig.Action, "", store.ErrCapExceeded
	}
	na := orig.Action
	na.ID = store.NewID(now)
	na.Payload, na.PayloadView, na.PayloadHash = payload, view, store.PayloadHash(payload)
	na.Status, na.CreatedAt = "pending", now
	na.ExpiresAt = store.ReviseExpiry(now, rc, orig.Action)
	tok, h := store.NewToken()
	texp := store.TokenExpiry(now, na.ExpiresAt)
	na.IssueCount, na.TokenSHA256, na.TokenExpiresAt = 1, &h, &texp
	orig.Status, orig.TokenSHA256, orig.TokenExpiresAt = "voided", nil, nil
	s.actions[na.ID] = &action{na, user}
	return na, tok, nil
}

func (s *Store) AddPushSubscription(_ context.Context, user string, sub store.PushSubscription) (store.PushAddResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, mine := 0, false
	for _, p := range s.pushSubs {
		if p.user == user {
			n++
			mine = mine || p.Endpoint == sub.Endpoint
		}
	}
	if !mine && n >= store.PushSubscriptionCap {
		return 0, store.ErrCapExceeded
	}
	if p, ok := s.pushSubs[sub.Endpoint]; ok && p.user != user {
		return store.PushOwnedByOther, nil
	}
	s.pushSubs[sub.Endpoint] = &pushSub{sub, user}
	return store.PushAdded, nil
}

func (s *Store) DeletePushSubscription(_ context.Context, user, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pushSubs[endpoint]; ok && p.user == user {
		delete(s.pushSubs, endpoint)
	}
	return nil
}

// PushSubscriptionOwner returns the owner of endpoint ("" if none); test helper.
func (s *Store) PushSubscriptionOwner(endpoint string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pushSubs[endpoint]; ok {
		return p.user
	}
	return ""
}
