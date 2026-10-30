package postgres

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	mkJob(t, s, a, job)
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
	bjob := "JB"
	mkJob(t, s, b, bjob)
	if _, err := s.AddMessage(ctx, b, store.Message{ThreadID: tb.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive, JobID: &bjob}); err != nil {
		t.Fatal(err)
	}

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

func mkJob(t *testing.T, s *Store, login, id string) {
	t.Helper()
	ctx := context.Background()
	u, err := s.uid(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO scheduled_jobs (id, user_id, name, when_kind, when_value, template_id, created_by)
		VALUES ($1,$2,'job','rrule','FREQ=DAILY','briefing','user')`, id, u); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJobFK(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	th, _ := s.CreateThread(ctx, a, "t")
	pro := func(user, job string) error {
		_, err := s.AddMessage(ctx, user, store.Message{ThreadID: th.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive, JobID: &job})
		return err
	}
	// unknown job: rejected (app check and FK)
	if err := pro(a, "nope"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	u, _ := s.uid(ctx, a)
	if _, err := s.pool.Exec(ctx, `INSERT INTO messages (id, thread_id, user_id, role, content, origin, job_id) VALUES ('m-raw',$1,$2,'assistant','x','proactive','nope')`, th.ID, u); err == nil {
		t.Fatal("FK not enforced")
	}
	mkJob(t, s, b, "JB")
	if err := pro(a, "JB"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("foreign job accepted", err)
	}
	mkJob(t, s, a, "JA")
	if err := pro(a, "JA"); err != nil {
		t.Fatal(err)
	}
	// soft delete keeps references valid and new references allowed
	if _, err := s.pool.Exec(ctx, `UPDATE scheduled_jobs SET enabled=false, deleted_at=now() WHERE id='JA'`); err != nil {
		t.Fatal(err)
	}
	if err := pro(a, "JA"); err != nil {
		t.Fatal(err)
	}
	if ts, err := s.ListUnreadThreads(ctx, a, false, 10, nil); err != nil || len(ts) != 1 || ts[0].UnreadCount != 2 {
		t.Fatal(err, ts)
	}
	// hard delete of a referenced job is refused
	if _, err := s.pool.Exec(ctx, `DELETE FROM scheduled_jobs WHERE id='JA'`); err == nil {
		t.Fatal("hard delete of referenced job succeeded")
	}
	// pending_actions.job_id is FK-checked too
	if _, err := s.CreateAction(ctx, a, store.Action{ServerFields: store.ServerFields{Tool: "x", Provenance: store.Provenance{Actor: "scheduler", JobID: ptr("nope")}},
		Payload: json.RawMessage(`{}`), ModelFields: store.ModelFields{PayloadView: json.RawMessage(`{}`)}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	// user deletion cascades through jobs and messages in one statement
	if _, err := s.pool.Exec(ctx, `DELETE FROM users WHERE login=$1`, a); err != nil {
		t.Fatal("user delete", err)
	}
	var n int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_jobs`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}

func ptr(s string) *string { return &s }

func TestPostgresActions_ReviseOrderAndExpired(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a = "alice@example"
	p := newProposal(t, s, a, itemsPayload)
	now := time.Now().UTC()
	// past the proposal's own expires_at (but within root+24h): 410, no resurrection
	if _, _, err := s.ReviseAction(ctx, a, p.ID, nil, p.ExpiresAt.Add(time.Second)); !errors.Is(err, store.ErrExpired) {
		t.Fatal(err)
	}
	var st string
	s.pool.QueryRow(ctx, `SELECT status FROM pending_actions WHERE id=$1`, p.ID).Scan(&st)
	if st != "pending" {
		t.Fatal(st)
	}
	// 409 before 410: voided + expired reports conflict
	if _, _, err := s.ReviseAction(ctx, a, p.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReviseAction(ctx, a, p.ID, nil, now.Add(48*time.Hour)); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	// 410 before 400
	q := newProposal(t, s, a, itemsPayload)
	if _, _, err := s.ReviseAction(ctx, a, q.ID, []string{"zz"}, q.ExpiresAt.Add(time.Second)); !errors.Is(err, store.ErrExpired) {
		t.Fatal(err)
	}
}

func TestPostgresActions_JobOwnership(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	mkJob(t, s, b, "JB")
	mkJob(t, s, a, "JA")
	mk := func(job string) (store.Action, error) {
		return s.CreateAction(ctx, a, store.Action{ServerFields: store.ServerFields{Tool: "x", Provenance: store.Provenance{Actor: "scheduler", JobID: ptr(job)}},
			Payload: json.RawMessage(`{}`), ModelFields: store.ModelFields{PayloadView: json.RawMessage(`{}`)}})
	}
	if _, err := mk("JB"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("foreign job accepted", err)
	}
	var n int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM pending_actions`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	if _, err := mk("nope"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := mk("JA"); err != nil {
		t.Fatal(err)
	}
	s.pool.Exec(ctx, `UPDATE scheduled_jobs SET enabled=false, deleted_at=now() WHERE id='JA'`)
	if _, err := mk("JA"); err != nil {
		t.Fatal("soft-deleted own job", err)
	}
}

// I-2: finished pim write cards lose payload/payload_view after the cutoff;
// live pending, young and other-tool cards are untouched; the hash stays.
func TestPostgresActions_ScrubActionContent(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a = "alice@example"
	now := time.Now().UTC()
	mk := func(tool, status string, age time.Duration, expiresIn time.Duration) string {
		p := newProposal(t, s, a, `{"title":"치과","notes":"보험증"}`)
		if _, err := s.pool.Exec(ctx, `UPDATE pending_actions SET tool=$2, status=$3, updated_at=$4, expires_at=$5 WHERE id=$1`,
			p.ID, tool, status, now.Add(-age), now.Add(expiresIn)); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	tools := []string{"calendar_add_event", "reminders_add"}
	expired := mk("calendar_add_event", "pending", 48*time.Hour, -47*time.Hour)
	approved := mk("calendar_add_event", "approved", 48*time.Hour, -47*time.Hour)
	voided := mk("reminders_add", "voided", 48*time.Hour, -47*time.Hour)
	live := mk("calendar_add_event", "pending", 48*time.Hour, time.Hour)
	young := mk("calendar_add_event", "approved", time.Hour, time.Hour)
	other := mk("memory.propose", "approved", 48*time.Hour, -47*time.Hour)

	n, err := s.ScrubActionContent(ctx, tools, now.Add(-24*time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("scrubbed %d (%v), want 3", n, err)
	}
	content := func(id string) (string, string, string) {
		var p, v, h string
		if err := s.pool.QueryRow(ctx, `SELECT payload::text, payload_view::text, payload_hash FROM pending_actions WHERE id=$1`, id).Scan(&p, &v, &h); err != nil {
			t.Fatal(err)
		}
		return p, v, h
	}
	for name, id := range map[string]string{"expired": expired, "approved": approved, "voided": voided} {
		if p, v, h := content(id); p != "{}" || v != "{}" || h == "" {
			t.Errorf("%s: payload=%s view=%s hash=%q", name, p, v, h)
		}
	}
	for name, id := range map[string]string{"live": live, "young": young, "other tool": other} {
		if p, _, _ := content(id); p == "{}" {
			t.Errorf("%s card was scrubbed", name)
		}
	}
	if n, err := s.ScrubActionContent(ctx, tools, now.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
}

// JSONB does not keep key order ({"text","kind"} is stored and returned as
// {"kind","text"}). The payload hash must therefore be over a canonical form:
// the hash computed at create time still verifies against the payload read
// back from the database (this was a 422 payload_mismatch on memory approve).
func TestPostgresActions_PayloadHashSurvivesJSONB(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const u = "alice@example"
	const payload = `{"op":"add","items":[{"text":"고양이를 키운다","kind":"fact"}],"z":{"b":1,"a":[2,{"y":1,"x":2}]}}`
	p := newProposal(t, s, u, payload)
	if p.PayloadHash != store.PayloadHash(json.RawMessage(payload)) {
		t.Fatal("create-time hash is not the canonical hash")
	}
	acts, err := s.ListPendingActions(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Action
	for i := range acts {
		if acts[i].ID == p.ID {
			got = &acts[i]
		}
	}
	if got == nil {
		t.Fatal("proposal not listed")
	}
	// sanity: the database really changed the byte order, so the old hash
	// over the producer's bytes would not verify any more
	if store.LegacyPayloadHash(got.Payload) == store.LegacyPayloadHash(json.RawMessage(payload)) {
		t.Fatal("expected JSONB to reorder keys; the test no longer proves anything")
	}
	if got.PayloadHash != p.PayloadHash || store.PayloadHash(got.Payload) != got.PayloadHash || !store.PayloadHashMatches(got.Payload, got.PayloadHash) {
		t.Fatalf("hash does not survive the JSONB round trip: stored %s, recomputed %s", got.PayloadHash, store.PayloadHash(got.Payload))
	}
	// reissue/revise recompute the hash too, still canonical
	_, tok, err := s.ReissueAction(ctx, u, p.ID, time.Now().UTC())
	if err != nil || tok == "" {
		t.Fatal(err)
	}
	// a pending row hashed the old way over an order that still matches the
	// stored order keeps verifying
	legacy := store.LegacyPayloadHash(got.Payload)
	if _, err := s.pool.Exec(ctx, `UPDATE pending_actions SET payload_hash=$1 WHERE id=$2`, legacy, p.ID); err != nil {
		t.Fatal(err)
	}
	if !store.PayloadHashMatches(got.Payload, legacy) {
		t.Fatal("legacy hash over the stored order must still verify")
	}
	if store.PayloadHashMatches(got.Payload, "sha256:"+strings.Repeat("0", 64)) {
		t.Fatal("wrong hash verified")
	}
}
