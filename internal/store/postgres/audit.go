package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gun025bba/eve-serving/internal/audit"
)

// Append implements audit.Ledger: in one transaction it takes the
// advisory lock, reads the last hash and inserts the chained row. prev_hash
// is UNIQUE, so a missing lock surfaces as a conflict (retried once) instead
// of a fork.
func (s *Store) Append(ctx context.Context, user string, e audit.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if user != "" {
		id, err := s.uid(ctx, user)
		if err != nil {
			return fmt.Errorf("audit user: %w", err)
		}
		e.UserID = id
	}
	err := s.appendOnce(ctx, e)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		err = s.appendOnce(ctx, e)
	}
	return err
}

func (s *Store) appendOnce(ctx context.Context, e audit.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tool_audit'))`); err != nil {
		return err
	}
	prev := audit.Genesis
	err = tx.QueryRow(ctx, `SELECT hash FROM tool_audit ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	e.At = e.At.UTC()
	h := audit.Hash(prev, e)
	var detail []byte
	if len(e.Detail) > 0 {
		detail, _ = json.Marshal(e.Detail)
	}
	_, err = tx.Exec(ctx, `INSERT INTO tool_audit (at, user_id, thread_id, message_id, tool_call_id, pending_action_id, actor, job_id, event,
		tool, tool_version, scope, payload_sha256, confirm_token_id, tainted, status, detail, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		e.At.Truncate(time.Microsecond), e.UserID, ns(e.ThreadID), ns(e.MessageID), ns(e.ToolCallID), ns(e.PendingActionID), e.Actor, ns(e.JobID), e.Event,
		ns(e.Tool), ns(e.ToolVersion), ns(e.Scope), ns(e.PayloadSHA256), ns(e.ConfirmTokenID), e.Tainted, ns(e.Status), detail, prev, h)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func ns(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// AuditRows reads the ledger in seq order (for Verify).
func (s *Store) AuditRows(ctx context.Context) ([]audit.Row, error) {
	return scanAudit(ctx, s.pool)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func scanAudit(ctx context.Context, q querier) ([]audit.Row, error) {
	rows, err := q.Query(ctx, `SELECT seq, at, user_id, thread_id, message_id, tool_call_id, pending_action_id, actor, job_id, event,
		tool, tool_version, scope, payload_sha256, confirm_token_id, tainted, status, detail, prev_hash, hash FROM tool_audit ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Row
	for rows.Next() {
		var r audit.Row
		var th, msg, tc, pa, job, tool, tv, sc, ps, ct, st *string
		var detail []byte
		e := &r.Event
		if err := rows.Scan(&r.Seq, &e.At, &e.UserID, &th, &msg, &tc, &pa, &e.Actor, &job, &e.Event, &tool, &tv, &sc, &ps, &ct, &e.Tainted, &st, &detail, &r.PrevHash, &r.Hash); err != nil {
			return nil, err
		}
		e.ThreadID, e.MessageID, e.ToolCallID, e.PendingActionID, e.JobID = str(th), str(msg), str(tc), str(pa), str(job)
		e.Tool, e.ToolVersion, e.Scope, e.PayloadSHA256, e.ConfirmTokenID, e.Status = str(tool), str(tv), str(sc), str(ps), str(ct), str(st)
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &e.Detail); err != nil {
				return nil, fmt.Errorf("audit seq %d detail: %w", r.Seq, err)
			}
		}
		e.At = e.At.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerifyAudit recomputes the whole chain.
func (s *Store) VerifyAudit(ctx context.Context) (audit.VerifyResult, error) {
	rows, err := s.AuditRows(ctx)
	if err != nil {
		return audit.VerifyResult{}, err
	}
	return audit.Verify(rows, "")
}

// ErrAuditPrivileges means the gateway login could rewrite the audit ledger.
var ErrAuditPrivileges = errors.New("audit privilege check failed")

// CheckAuditPrivileges is the startup gate (§5): the gateway login must be
// able to INSERT/SELECT tool_audit but must not own it, be a member of its
// owner role, or hold UPDATE/DELETE/TRUNCATE. Superusers fail (membership).
func (s *Store) CheckAuditPrivileges(ctx context.Context) error {
	var member, appMember, upd, del, trunc, ins, sel bool
	err := s.pool.QueryRow(ctx, `SELECT
		pg_has_role(current_user, 'eve_audit_owner', 'MEMBER'),
		pg_has_role('eve_app', 'eve_audit_owner', 'MEMBER'),
		has_table_privilege(current_user, 'tool_audit', 'UPDATE'),
		has_table_privilege(current_user, 'tool_audit', 'DELETE'),
		has_table_privilege(current_user, 'tool_audit', 'TRUNCATE'),
		has_table_privilege(current_user, 'tool_audit', 'INSERT'),
		has_table_privilege(current_user, 'tool_audit', 'SELECT')`).
		Scan(&member, &appMember, &upd, &del, &trunc, &ins, &sel)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuditPrivileges, err)
	}
	var bad []string
	if member {
		bad = append(bad, "login is a member of eve_audit_owner (or superuser)")
	}
	if appMember {
		bad = append(bad, "eve_app is a member of eve_audit_owner")
	}
	if upd || del || trunc {
		bad = append(bad, "login has UPDATE/DELETE/TRUNCATE on tool_audit")
	}
	if !ins || !sel {
		bad = append(bad, "login lacks INSERT/SELECT on tool_audit (grant eve_app)")
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %v", ErrAuditPrivileges, bad)
	}
	return nil
}
