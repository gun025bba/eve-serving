package postgres

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

func newProposal(t *testing.T, s *Store, user string, payload string) store.Action {
	t.Helper()
	ctx := context.Background()
	th, err := s.CreateThread(ctx, user, "t")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, user, store.Message{ThreadID: th.ID, Role: "assistant", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAction(ctx, user, store.Action{
		ServerFields: store.ServerFields{ThreadID: &th.ID, MessageID: &m.ID, Tool: "memory.propose", ToolVersion: "1.0.0",
			Scopes: []string{"memory.write"}, Provenance: store.Provenance{Actor: "user"}, Tainted: true},
		ModelFields: store.ModelFields{Summary: "s", PayloadView: json.RawMessage(payload)},
		Payload:     json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const itemsPayload = `{"op":"delete","items":[{"id":"i1"},{"id":"i2"},{"id":"i3"}]}`

func TestPostgresActions_ReissueRevise(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	p := newProposal(t, s, a, itemsPayload)
	now := time.Now().UTC()

	// owner check first, identical for missing and foreign
	if _, _, err := s.ReissueAction(ctx, b, p.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := s.ReviseAction(ctx, b, p.ID, nil, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := s.ReissueAction(ctx, a, "missing", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}

	r1, tok1, err := s.ReissueAction(ctx, a, p.ID, now)
	if err != nil || tok1 == "" || *r1.TokenSHA256 != store.TokenHash(tok1) || r1.TokenExpiresAt.After(r1.ExpiresAt) {
		t.Fatal(err, r1)
	}
	_, tok2, _ := s.ReissueAction(ctx, a, p.ID, now)
	var stored string
	s.pool.QueryRow(ctx, `SELECT token_sha256 FROM pending_actions WHERE id=$1`, p.ID).Scan(&stored)
	if stored != store.TokenHash(tok2) || tok1 == tok2 {
		t.Fatal("previous token not invalidated")
	}

	// revise to a subset: new id, new hash, inherited fields, original voided
	if _, _, err := s.ReviseAction(ctx, a, p.ID, []string{"zz"}, now); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	n, tok3, err := s.ReviseAction(ctx, a, p.ID, []string{"i2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == p.ID || n.RootID != p.ID || n.PayloadHash == p.PayloadHash || !n.Tainted || n.Provenance.Actor != "user" || n.Scopes[0] != "memory.write" || tok3 == "" {
		t.Fatalf("%+v", n)
	}
	var view map[string]any
	json.Unmarshal(n.PayloadView, &view)
	if len(view["items"].([]any)) != 1 {
		t.Fatal(view)
	}
	var st string
	var th *string
	s.pool.QueryRow(ctx, `SELECT status, token_sha256 FROM pending_actions WHERE id=$1`, p.ID).Scan(&st, &th)
	if st != "voided" || th != nil {
		t.Fatal(st, th)
	}
	if _, _, err := s.ReissueAction(ctx, a, p.ID, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("non-pending reissue", err)
	}
	if _, _, err := s.ReviseAction(ctx, a, p.ID, nil, now); !errors.Is(err, store.ErrConflict) {
		t.Fatal("non-pending revise", err)
	}
	// chain cap: 2 (root) + 1 (revise) = 3
	if _, _, err := s.ReissueAction(ctx, a, n.ID, now); !errors.Is(err, store.ErrCapExceeded) {
		t.Fatal("chain cap", err)
	}
	if _, _, err := s.ReviseAction(ctx, a, n.ID, nil, now); !errors.Is(err, store.ErrCapExceeded) {
		t.Fatal("chain cap revise", err)
	}
	ps, _ := s.ListPendingActions(ctx, a)
	if len(ps) != 1 || ps[0].ID != n.ID {
		t.Fatal(ps)
	}
	if ps, _ := s.ListPendingActions(ctx, b); len(ps) != 0 {
		t.Fatal("isolation")
	}
	// root + 24h
	if _, _, err := s.ReviseAction(ctx, a, n.ID, nil, now.Add(25*time.Hour)); !errors.Is(err, store.ErrExpired) {
		t.Fatal(err)
	}
	if _, _, err := s.ReissueAction(ctx, a, n.ID, now.Add(25*time.Hour)); !errors.Is(err, store.ErrExpired) {
		t.Fatal(err)
	}
}

func TestPostgresActions_ConcurrentReissueCap(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p := newProposal(t, s, "alice@example", `{}`)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, capped := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ReissueAction(ctx, "alice@example", p.ID, time.Now().UTC())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrCapExceeded):
				capped++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != store.ReissueCap || capped != 10-store.ReissueCap {
		t.Fatal(ok, capped)
	}
}

func TestPostgresActions_ConcurrentReviseOnlyOneWins(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p := newProposal(t, s, "alice@example", `{}`)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflict := 0, 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ReviseAction(ctx, "alice@example", p.ID, nil, time.Now().UTC())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, store.ErrConflict) {
				conflict++
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflict != 4 {
		t.Fatal(ok, conflict)
	}
}

func key65(t *testing.T) string {
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	return fmt.Sprintf("%x", k.PublicKey().Bytes())
}

func TestPostgresPush_CapOwnershipDelete(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	sub := func(ep string) store.PushSubscription {
		return store.PushSubscription{Endpoint: ep, P256dh: key65(t), Auth: "x", VAPIDKeyID: "k1"}
	}
	if r, err := s.AddPushSubscription(ctx, b, sub("https://web.push.apple.com/bob")); err != nil || r != store.PushAdded {
		t.Fatal(r, err)
	}
	// concurrent: exactly 5 of 8 succeed
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, capped := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AddPushSubscription(ctx, a, sub(fmt.Sprintf("https://web.push.apple.com/a%d", i)))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, store.ErrCapExceeded) {
				capped++
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 5 || capped != 3 {
		t.Fatal(ok, capped)
	}
	// cap checked before ownership
	if _, err := s.AddPushSubscription(ctx, a, sub("https://web.push.apple.com/bob")); !errors.Is(err, store.ErrCapExceeded) {
		t.Fatal(err)
	}
	var mine []string
	rows, _ := s.pool.Query(ctx, `SELECT endpoint FROM push_subscriptions p JOIN users u ON u.id=p.user_id WHERE u.login=$1`, a)
	for rows.Next() {
		var e string
		rows.Scan(&e)
		mine = append(mine, e)
	}
	rows.Close()
	if err := s.DeletePushSubscription(ctx, a, mine[0]); err != nil {
		t.Fatal(err)
	}
	// other user's endpoint: no transfer, no row for me
	r, err := s.AddPushSubscription(ctx, a, sub("https://web.push.apple.com/bob"))
	if err != nil || r != store.PushOwnedByOther {
		t.Fatal(r, err)
	}
	var owner string
	s.pool.QueryRow(ctx, `SELECT u.login FROM push_subscriptions p JOIN users u ON u.id=p.user_id WHERE endpoint=$1`, "https://web.push.apple.com/bob").Scan(&owner)
	if owner != b {
		t.Fatal(owner)
	}
	// foreign delete is a no-op without error
	if err := s.DeletePushSubscription(ctx, a, "https://web.push.apple.com/bob"); err != nil {
		t.Fatal(err)
	}
	var cnt int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions WHERE endpoint=$1`, "https://web.push.apple.com/bob").Scan(&cnt)
	if cnt != 1 {
		t.Fatal("foreign delete removed row")
	}
}

func TestPostgresProactiveMessagesUnread(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	t1, _ := s.CreateThread(ctx, a, "plain")
	s.AddMessage(ctx, a, store.Message{ThreadID: t1.ID, Role: "user", Content: "hi"})
	t2, _ := s.CreateThread(ctx, a, "brief")
	job, kind, name := "J1", "briefing", "아침 브리핑"
	pm, err := s.AddMessage(ctx, a, store.Message{ThreadID: t2.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive,
		JobID: &job, TemplateKind: &kind, Provenance: &store.Provenance{JobName: &name}})
	if err != nil {
		t.Fatal(err)
	}
	// proactive without job_id violates the CHECK
	if _, err := s.AddMessage(ctx, a, store.Message{ThreadID: t2.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	tb, _ := s.CreateThread(ctx, b, "bob")
	s.AddMessage(ctx, b, store.Message{ThreadID: tb.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive, JobID: &job})

	ts, err := s.ListUnreadThreads(ctx, a, false, 10, nil)
	if err != nil || len(ts) != 1 || ts[0].ID != t2.ID || ts[0].UnreadCount != 1 {
		t.Fatalf("%v %+v", err, ts)
	}
	all, _ := s.ListThreads(ctx, a, false, 10, nil)
	if len(all) != 2 {
		t.Fatal(all)
	}
	m, _ := s.GetMessage(ctx, a, pm.ID)
	if m.Origin != "proactive" || *m.TemplateKind != "briefing" || m.Provenance == nil || m.Provenance.Actor != "scheduler" || *m.Provenance.JobName != name || *m.Provenance.JobID != job {
		t.Fatalf("%+v", m)
	}
	ms, _, _ := s.ListMessages(ctx, a, t1.ID, 10, "")
	if ms[0].Origin != "user" || ms[0].Provenance != nil || ms[0].TemplateKind != nil {
		t.Fatalf("%+v", ms[0])
	}
	if _, err := s.GetMessage(ctx, b, pm.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("nid owner check")
	}
	s.pool.Exec(ctx, `UPDATE messages SET read_at=now() WHERE id=$1`, pm.ID)
	if ts, _ := s.ListUnreadThreads(ctx, a, false, 10, nil); len(ts) != 0 {
		t.Fatal(ts)
	}
}
