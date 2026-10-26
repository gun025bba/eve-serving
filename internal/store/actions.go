package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Proactive P1 / confirmation errors (docs/architecture/ui-api-contract.md §3.3).
var (
	ErrConflict    = errors.New("conflict")     // 409 conflict: already approved/voided
	ErrExpired     = errors.New("expired")      // 410 expired: root + 24h passed
	ErrCapExceeded = errors.New("cap_exceeded") // 422 cap_exceeded
	ErrItemGone    = errors.New("item_gone")    // 409 item_gone
)

const (
	// ReissueCap is the number of tokens a revise chain (root_id) may receive,
	// first issue included (ui-api-contract.md §3.5.4 decision 3).
	ReissueCap = 3
	// TokenTTL is the confirm token lifetime (TP §3.2: 120–300s, default 120s).
	TokenTTL = 120 * time.Second
	// ProposalMaxAge bounds every proposal in a chain to root.created_at + 24h.
	ProposalMaxAge = 24 * time.Hour
	// PushSubscriptionCap is the per-user subscription limit (§3.5.3 decision 1).
	PushSubscriptionCap = 5
)

// Provenance says who created a proposal or proactive message. All fields are
// server-set (never taken from request bodies or model output).
type Provenance struct {
	Actor   string  `json:"actor"`
	JobID   *string `json:"job_id"`
	JobName *string `json:"job_name"`
}

// Action is a stored confirmation proposal. Fields are split by origin:
// ServerFields are set by the gateway; ModelFields carry model/tool-derived
// content and are the only part the redactor touches.
type Action struct {
	ServerFields
	ModelFields
	Payload json.RawMessage `json:"-"` // executed payload; never serialized
}

type ServerFields struct {
	ID             string     `json:"action_id"`
	RootID         string     `json:"-"` // server only, never exported
	ThreadID       *string    `json:"thread_id"`
	MessageID      *string    `json:"message_id"`
	Tool           string     `json:"tool"`
	ToolVersion    string     `json:"tool_version"`
	Scopes         []string   `json:"scopes"`
	PayloadHash    string     `json:"payload_hash"`
	Provenance     Provenance `json:"provenance"`
	Tainted        bool       `json:"tainted"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	IssueCount     int        `json:"-"`
	TokenSHA256    *string    `json:"-"`
	TokenExpiresAt *time.Time `json:"-"`
}

type ModelFields struct {
	Summary     string          `json:"summary"`
	PayloadView json.RawMessage `json:"payload_view"`
}

// Confirmation is the token-free API view (GET /v1/actions items).
type Confirmation struct {
	Object string `json:"object"`
	ServerFields
	ModelFields
}

// IssuedConfirmation adds the one-time token; only ever written to the
// session POST response of reissue/revise (§3.5.4), never to SSE.
type IssuedConfirmation struct {
	Confirmation
	ConfirmToken   string    `json:"confirm_token"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
}

func (a Action) View() Confirmation {
	sf := a.ServerFields
	if sf.Scopes == nil {
		sf.Scopes = []string{}
	}
	return Confirmation{Object: "chat.completion.confirmation", ServerFields: sf, ModelFields: a.ModelFields}
}

// PushSubscription is a validated Web Push subscription.
type PushSubscription struct {
	Endpoint   string
	P256dh     string
	Auth       string
	VAPIDKeyID string
}

// PushAddResult tells the handler what happened without exposing it to the
// client (both outcomes are 201).
type PushAddResult int

const (
	PushAdded PushAddResult = iota
	PushOwnedByOther
)

// ActionStore holds confirmation proposals. All methods are scoped to user:
// a missing id and another user's id behave identically (ErrNotFound) and go
// through the same query (WHERE id=$1 AND user_id=$2).
type ActionStore interface {
	// CreateAction inserts a new pending proposal (root of its own chain).
	CreateAction(ctx context.Context, user string, a Action) (Action, error)
	// ListPendingActions returns the user's status='pending' proposals, oldest first.
	ListPendingActions(ctx context.Context, user string) ([]Action, error)
	// ReissueAction issues a new token for a pending proposal, voiding the
	// previous one; counted against ReissueCap across the chain.
	ReissueAction(ctx context.Context, user, id string, now time.Time) (Action, string, error)
	// ReviseAction voids a pending proposal and creates its successor with
	// the same payload (itemIDs nil) or the subset itemIDs, plus a token.
	ReviseAction(ctx context.Context, user, id string, itemIDs []string, now time.Time) (Action, string, error)
	AddPushSubscription(ctx context.Context, user string, sub PushSubscription) (PushAddResult, error)
	// DeletePushSubscription removes the user's own subscription; no error
	// when absent or owned by someone else.
	DeletePushSubscription(ctx context.Context, user, endpoint string) error
	// MarkMessageRead/ListThreadsUnread support the unread filter (§3.5.2).
	ListUnreadThreads(ctx context.Context, user string, archived bool, limit int, after *ThreadCursor) ([]Thread, error)
}

// NewToken returns a 256-bit url-safe token and its sha256 (hex).
func NewToken() (string, string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, TokenHash(t)
}

func TokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// PayloadHash is "sha256:" + hex of the compacted payload JSON.
func PayloadHash(p json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, p); err != nil {
		buf.Reset()
		buf.Write(p)
	}
	h := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(h[:])
}

// TokenExpiry caps the token lifetime at the proposal expiry.
func TokenExpiry(now, proposalExpiry time.Time) time.Time {
	t := now.Add(TokenTTL)
	if proposalExpiry.Before(t) {
		return proposalExpiry
	}
	return t
}

// ReviseExpiry keeps the original TTL but never exceeds root + 24h.
func ReviseExpiry(now, rootCreated time.Time, orig Action) time.Time {
	ttl := orig.ExpiresAt.Sub(orig.CreatedAt)
	if ttl <= 0 {
		ttl = ProposalMaxAge
	}
	exp := now.Add(ttl)
	if lim := rootCreated.Add(ProposalMaxAge); exp.After(lim) {
		exp = lim
	}
	return exp
}

// FilterItems applies a revise item_ids selection to a payload (or view).
// Proposals with items carry {"items":[{"id":...},...]}. itemIDs nil = keep
// as is. Returns ErrInvalid when itemIDs is not a non-empty subset of the
// proposal's item ids, or when the proposal has no items.
func FilterItems(raw json.RawMessage, itemIDs []string) (json.RawMessage, error) {
	if itemIDs == nil {
		return raw, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, ErrInvalid
	}
	itemsRaw, ok := obj["items"]
	if !ok {
		return nil, ErrInvalid
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, ErrInvalid
	}
	have := map[string]json.RawMessage{}
	var order []string
	for _, it := range items {
		var x struct {
			ID *string `json:"id"`
		}
		if json.Unmarshal(it, &x) != nil || x.ID == nil {
			return nil, ErrInvalid // items without ids cannot be selected
		}
		have[*x.ID] = it
		order = append(order, *x.ID)
	}
	if len(itemIDs) == 0 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range itemIDs {
		if _, ok := have[id]; !ok || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
	}
	var kept []json.RawMessage
	for _, id := range order {
		if slices.Contains(itemIDs, id) {
			kept = append(kept, have[id])
		}
	}
	b, _ := json.Marshal(kept)
	obj["items"] = b
	out, err := json.Marshal(obj)
	return out, err
}
