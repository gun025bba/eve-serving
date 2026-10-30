package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

func TestPostgresBriefingRetention(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const a, b, c = "alice@example", "bob@example", "carol@example"
	mkJobT := func(login, id, tpl string) {
		u, err := s.uid(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO scheduled_jobs (id, user_id, name, when_kind, when_value, template_id, created_by)
			VALUES ($1,$2,'job','rrule','FREQ=DAILY',$3,'user')`, id, u, tpl); err != nil {
			t.Fatal(err)
		}
	}
	mkJobT(a, "JA-B", "morning_briefing")
	mkJobT(a, "JA-O", "other_template")
	mkJobT(b, "JB-B", "morning_briefing")
	mkJobT(c, "JC-B", "morning_briefing")
	th := map[string]string{}
	for _, l := range []string{a, b, c} {
		x, err := s.CreateThread(ctx, l, "t")
		if err != nil {
			t.Fatal(err)
		}
		th[l] = x.ID
	}
	kind := store.BriefingKind
	add := func(login, job, role string, ageDays int) string {
		m := store.Message{ThreadID: th[login], Role: role, Content: "x", CreatedAt: now.Add(-time.Duration(ageDays) * 24 * time.Hour)}
		if job != "" {
			m.Origin, m.JobID, m.TemplateKind = store.OriginProactive, &job, &kind
		}
		out, err := s.AddMessage(ctx, login, m)
		if err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	aOld, aNew, aEdge := add(a, "JA-B", "assistant", 31), add(a, "JA-B", "assistant", 5), add(a, "JA-B", "assistant", 29)
	aOther := add(a, "JA-O", "assistant", 90) // other template, old
	aUser, aAsst := add(a, "", "user", 90), add(a, "", "assistant", 90)
	bOld := add(b, "JB-B", "assistant", 400) // retention 0 = off
	cOld, cNew := add(c, "JC-B", "assistant", 8), add(c, "JC-B", "assistant", 6)
	if _, err := s.MergeUserSettings(ctx, b, "briefing", json.RawMessage(`{"retention_days":0}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeUserSettings(ctx, c, "briefing", json.RawMessage(`{"retention_days":7}`)); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	res, err := s.PurgeBriefings(ctx, now, 30)
	if err != nil || len(res) != 2 || res[0] != (store.BriefingPurge{Login: a, Deleted: 1, Days: 30}) || res[1] != (store.BriefingPurge{Login: c, Deleted: 1, Days: 7}) {
		t.Fatal(res, err)
	}
	for id, want := range map[string]bool{aOld: false, cOld: false, aNew: true, aEdge: true, aOther: true, aUser: true, aAsst: true, bOld: true, cNew: true} {
		if exists(id) != want {
			t.Errorf("message %s exists=%v want %v", id, !want, want)
		}
	}
	if res, err := s.PurgeBriefings(ctx, now, 30); err != nil || len(res) != 0 {
		t.Fatal("not idempotent", res, err)
	}
	// a non-numeric stored value counts as unset (default applies)
	if _, err := s.pool.Exec(ctx, `UPDATE users SET settings = '{"briefing":{"retention_days":"abc"}}' WHERE login=$1`, c); err != nil {
		t.Fatal(err)
	}
	if res, err := s.PurgeBriefings(ctx, now.Add(40*24*time.Hour), 30); err != nil || len(res) != 2 {
		t.Fatal(res, err) // a (aNew, aEdge) and c (cNew) at the 30-day default
	}
}
