package postgres

import (
	"context"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gun025bba/eve-serving/internal/audit"
)

// TestMigrations_DownNeverDropsAudit is the lint required by §5: no goose
// Down section may drop/truncate/alter tool_audit or its roles.
func TestMigrations_DownNeverDropsAudit(t *testing.T) {
	bad := regexp.MustCompile(`(?i)\b(drop|truncate|alter|delete|revoke)\b[^;]*(tool_audit|eve_audit_owner|eve_app|eve_retention)`)
	sub := mustSub()
	ents, _ := fs.ReadDir(sub, ".")
	for _, e := range ents {
		b, _ := fs.ReadFile(sub, e.Name())
		s := string(b)
		i := strings.Index(s, "-- +goose Down")
		if i < 0 {
			continue
		}
		if m := bad.FindString(s[i:]); m != "" {
			t.Errorf("%s: Down touches the audit ledger: %q", e.Name(), m)
		}
	}
	// and the lint itself works
	if !bad.MatchString("-- +goose Down\nDROP TABLE tool_audit;") {
		t.Fatal("lint regexp broken")
	}
}

const testLogin = "eve_gw_test"

// appStore connects as a LOGIN role that only has eve_app (like the gateway).
func appStore(t *testing.T, admin *Store) *Store {
	ctx := context.Background()
	for _, q := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='` + testLogin + `') THEN
			CREATE ROLE ` + testLogin + ` LOGIN PASSWORD 'eve-gw-test-only'; END IF; END $$`,
		`GRANT eve_app TO ` + testLogin,
		`GRANT SELECT, INSERT, UPDATE ON users TO ` + testLogin,
		`GRANT USAGE ON SEQUENCE users_id_seq TO ` + testLogin,
	} {
		if _, err := admin.pool.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	cfg := admin.pool.Config().Copy()
	cfg.ConnConfig.User = testLogin
	cfg.ConnConfig.Password = "eve-gw-test-only"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return &Store{pool: p}
}

func ev(name string) audit.Event {
	return audit.Event{At: time.Now(), Actor: audit.ActorUser, Event: name, Tool: "web_search", ToolVersion: "1", Status: "ok",
		Detail: map[string]string{"host_hash": audit.HashHost("example.com"), "rule": "shortener:3", "hop": "1"}}
}

func TestAudit_PrivilegesAndAppendOnly(t *testing.T) {
	admin := open(t)
	ctx := context.Background()
	// the superuser test login must FAIL the startup gate
	if err := admin.CheckAuditPrivileges(ctx); err == nil {
		t.Fatal("superuser must fail the audit privilege check")
	}
	// Med 1: the migration login's eve_audit_owner membership is revoked
	var member bool
	if err := admin.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
		JOIN pg_roles u ON u.oid = m.member WHERE r.rolname = 'eve_audit_owner' AND u.rolname = current_user)`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	if member {
		t.Fatal("migration login must not stay a member of eve_audit_owner")
	}
	app := appStore(t, admin)
	if err := app.CheckAuditPrivileges(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Append(ctx, "alice@example", ev(audit.EvCallDone)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE tool_audit SET status='x'`, `DELETE FROM tool_audit`, `TRUNCATE tool_audit`} {
		if _, err := app.pool.Exec(ctx, q); err == nil {
			t.Errorf("app login: %s must fail", q)
		}
		if _, err := admin.pool.Exec(ctx, q); err == nil { // trigger stops even the owner/superuser
			t.Errorf("superuser: %s must fail (trigger)", q)
		}
	}
	// granting UPDATE makes the gate fail; membership too
	admin.pool.Exec(ctx, `GRANT UPDATE ON tool_audit TO `+testLogin)
	if err := app.CheckAuditPrivileges(ctx); err == nil {
		t.Error("UPDATE grant must fail the gate")
	}
	admin.pool.Exec(ctx, `REVOKE UPDATE ON tool_audit FROM `+testLogin)
	admin.pool.Exec(ctx, `GRANT eve_audit_owner TO `+testLogin)
	if err := app.CheckAuditPrivileges(ctx); err == nil {
		t.Error("owner membership must fail the gate")
	}
	admin.pool.Exec(ctx, `REVOKE eve_audit_owner FROM `+testLogin)
	if err := app.CheckAuditPrivileges(ctx); err != nil {
		t.Fatal(err)
	}
	// DB constraints: actor has no default, scheduler needs job_id
	if _, err := admin.pool.Exec(ctx, `INSERT INTO tool_audit (at,user_id,event,prev_hash,hash) VALUES (now(),0,'x',repeat('a',64),repeat('b',64))`); err == nil {
		t.Error("actor NOT NULL")
	}
	if _, err := admin.pool.Exec(ctx, `INSERT INTO tool_audit (at,user_id,actor,event,prev_hash,hash) VALUES (now(),0,'scheduler','x',repeat('a',64),repeat('b',64))`); err == nil {
		t.Error("scheduler without job_id")
	}
}

func TestAudit_ConcurrentChainAndTamper(t *testing.T) {
	admin := open(t)
	app := appStore(t, admin)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := app.Append(ctx, "bob@example", ev(audit.EvDomainBlocked)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	res, err := app.VerifyAudit(ctx)
	if err != nil || res.Rows < 40 {
		t.Fatalf("verify %+v %v", res, err)
	}
	// fork attempt: same prev_hash twice violates the UNIQUE index
	rows, _ := app.AuditRows(ctx)
	last := rows[len(rows)-1]
	_, err = app.pool.Exec(ctx, `INSERT INTO tool_audit (at,user_id,actor,event,prev_hash,hash) VALUES (now(),0,'system','x',$1,repeat('c',64))`, last.PrevHash)
	if err == nil {
		t.Fatal("fork must violate UNIQUE prev_hash")
	}
	// tamper (needs disabling the trigger as superuser), detect, restore
	tx, err := admin.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE tool_audit DISABLE TRIGGER tool_audit_no_update_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tool_audit SET status='tampered' WHERE seq=$1`, rows[len(rows)/2].Seq); err != nil {
		t.Fatal(err)
	}
	trows, err := scanAudit(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.Verify(trows, ""); err == nil {
		t.Fatal("tampered row must fail verify")
	}
	// rollback restores the row and the trigger
}
