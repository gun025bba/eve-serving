// Package audit is the append-only, hash-chained tool audit ledger
// (docs/tool-platform.md §5, table tool_audit). Bodies are never stored, only
// hashes and identifiers.
//
// actor and job_id are set by the gateway from its own execution context
// (request handler vs scheduler); they are never taken from request bodies,
// headers, model output or tool arguments, and there is no default actor.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Actors.
const (
	ActorUser      = "user"
	ActorScheduler = "scheduler"
	ActorSystem    = "system"
)

// Events written by M2.
const (
	EvCallStarted    = "call_started"
	EvCallDone       = "call_done"
	EvDomainBlocked  = "domain_blocked"
	EvRegistryLoaded = "registry_loaded"
	EvKillswitch     = "killswitch"
	EvApproveRefused = "approve_refused" // approval refused by the operator kill switch before the token was spent
	EvPolicyLoaded   = "policy_loaded"
)

// Genesis is the prev_hash of the first row.
const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

// Event is one ledger row before chaining.
type Event struct {
	At              time.Time         `json:"at"`
	UserID          int64             `json:"user_id"`
	ThreadID        string            `json:"thread_id,omitempty"`
	MessageID       string            `json:"message_id,omitempty"`
	ToolCallID      string            `json:"tool_call_id,omitempty"`
	PendingActionID string            `json:"pending_action_id,omitempty"`
	Actor           string            `json:"actor"`
	JobID           string            `json:"job_id,omitempty"`
	Event           string            `json:"event"`
	Tool            string            `json:"tool,omitempty"`
	ToolVersion     string            `json:"tool_version,omitempty"`
	Scope           string            `json:"scope,omitempty"`
	PayloadSHA256   string            `json:"payload_sha256,omitempty"`
	ConfirmTokenID  string            `json:"confirm_token_id,omitempty"`
	Tainted         *bool             `json:"tainted,omitempty"`
	Status          string            `json:"status,omitempty"`
	Detail          map[string]string `json:"detail,omitempty"` // hashes, rule ids, counters only
}

// Validate enforces the server-side invariants (also enforced by the DB).
func (e *Event) Validate() error {
	switch e.Actor {
	case ActorUser, ActorSystem:
	case ActorScheduler:
		if e.JobID == "" {
			return errors.New("audit: actor scheduler requires job_id")
		}
	case "":
		return errors.New("audit: actor must be set explicitly")
	default:
		return fmt.Errorf("audit: invalid actor %q", e.Actor)
	}
	if e.Event == "" {
		return errors.New("audit: event required")
	}
	if e.At.IsZero() {
		return errors.New("audit: at required")
	}
	return nil
}

// Canonical is the byte string covered by the row hash: JSON with a fixed
// field order (struct order, map keys sorted by encoding/json), UTC
// microsecond timestamp.
func Canonical(e Event) []byte {
	e.At = e.At.UTC().Truncate(time.Microsecond)
	b, _ := json.Marshal(e)
	return b
}

// Hash returns sha256(prev_hash || "\n" || canonical(row)) as hex.
func Hash(prev string, e Event) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte("\n"))
	h.Write(Canonical(e))
	return hex.EncodeToString(h.Sum(nil))
}

// HashHost hashes a host for the audit log (hosts are not stored in clear).
func HashHost(h string) string {
	s := sha256.Sum256([]byte(strings.ToLower(h)))
	return hex.EncodeToString(s[:16])
}

// Row is a stored, chained event.
type Row struct {
	Seq      int64
	Event    Event
	PrevHash string
	Hash     string
}

// Ledger appends events. User is the authenticated login; the store maps it
// to users.id ("" for system events -> 0).
type Ledger interface {
	Append(ctx context.Context, user string, e Event) error
}

// VerifyResult summarizes a chain check.
type VerifyResult struct {
	Rows     int
	SeqGaps  int // gaps are expected after rolled-back inserts; reported, not fatal
	FirstBad int64
}

// Verify recomputes the chain over rows in seq order. The first row must
// chain from Genesis (or from a retention checkpoint hash when given).
func Verify(rows []Row, start string) (VerifyResult, error) {
	res := VerifyResult{Rows: len(rows)}
	prev := start
	if prev == "" {
		prev = Genesis
	}
	var lastSeq int64
	for i, r := range rows {
		if i > 0 && r.Seq != lastSeq+1 {
			res.SeqGaps++
		}
		if r.Seq <= lastSeq && i > 0 {
			res.FirstBad = r.Seq
			return res, fmt.Errorf("audit: seq not increasing at %d", r.Seq)
		}
		if r.PrevHash != prev {
			res.FirstBad = r.Seq
			return res, fmt.Errorf("audit: chain broken at seq %d (prev_hash mismatch)", r.Seq)
		}
		if got := Hash(r.PrevHash, r.Event); got != r.Hash {
			res.FirstBad = r.Seq
			return res, fmt.Errorf("audit: row hash mismatch at seq %d", r.Seq)
		}
		prev, lastSeq = r.Hash, r.Seq
	}
	return res, nil
}

// Memory is an in-process ledger (no database configured, and tests).
type Memory struct {
	mu   sync.Mutex
	rows []Row
	Fail error // when set, Append returns it (failure injection)
}

func (m *Memory) Append(_ context.Context, _ string, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	prev := Genesis
	if n := len(m.rows); n > 0 {
		prev = m.rows[n-1].Hash
	}
	e.At = e.At.UTC().Truncate(time.Microsecond)
	m.rows = append(m.rows, Row{Seq: int64(len(m.rows) + 1), Event: e, PrevHash: prev, Hash: Hash(prev, e)})
	return nil
}

// Rows returns a copy of the stored rows.
func (m *Memory) Rows() []Row {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Row(nil), m.rows...)
}
