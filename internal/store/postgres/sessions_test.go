package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/session"
	"github.com/pressly/goose/v3"
)

func TestSessionsStore(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	m, err := session.New(s, []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	// Pairing binds to the existing users row (same users.id as threads).
	th, err := s.CreateThread(ctx, "alice@example", "t")
	if err != nil {
		t.Fatal(err)
	}
	_ = th
	uid, _ := s.uid(ctx, "alice@example")
	code, err := m.IssueCode(ctx, "alice@example")
	if err != nil {
		t.Fatal(err)
	}
	tok, sess, err := m.Pair(ctx, code)
	if err != nil || sess.UserID != uid {
		t.Fatal(sess, err)
	}
	if _, _, err := m.Pair(ctx, code); err != session.ErrPairFailed {
		t.Fatal("code reused", err)
	}
	got, err := m.Validate(ctx, tok)
	if err != nil || got.Login != "alice@example" || got.UserID != uid {
		t.Fatal(got, err)
	}
	// Nothing but hashes is stored.
	var n int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("raw token stored")
	}
	if err := s.TouchSession(ctx, sess.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ss, err := s.ListSessions(ctx)
	if err != nil || len(ss) != 1 {
		t.Fatal(ss, err)
	}
	if err := s.RevokeSession(ctx, sess.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(ctx, tok); err != session.ErrNotFound {
		t.Fatal("revoked session valid", err)
	}
	if err := s.RevokeSession(ctx, 999999, time.Now()); err != session.ErrNotFound {
		t.Fatal(err)
	}
	// expired codes are not redeemable; lockout wipes outstanding codes
	if err := s.PutPairCode(ctx, uid, "h-expired", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TakePairCode(ctx, "h-expired", time.Now()); ok {
		t.Fatal("expired code taken")
	}
	s.PutPairCode(ctx, uid, "h-live", time.Now().Add(time.Minute))
	if err := s.DeleteAllPairCodes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TakePairCode(ctx, "h-live", time.Now()); ok {
		t.Fatal("code survived DeleteAll")
	}
}

// 00007 is reversible: down drops only sessions/pairing_codes, up restores them.
func TestSessionsMigrationReversible(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, mustSub())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.uid(ctx, "keep@example"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	var reg sql.NullString
	db.QueryRow(`SELECT to_regclass('sessions')::text`).Scan(&reg)
	if reg.Valid {
		t.Fatal("sessions still exists after down")
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM users WHERE login = 'keep@example'`).Scan(&n)
	if n != 1 {
		t.Fatal("down touched users")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT to_regclass('sessions')::text`).Scan(&reg)
	if !reg.Valid {
		t.Fatal("sessions missing after up")
	}
}
