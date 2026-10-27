// Package store defines the conversation persistence layer. Handlers depend
// only on these interfaces; internal/store/postgres is the production
// implementation and internal/store/memstore an in-memory one for tests.
//
// Every method takes the authenticated user login (auth.Identity.User) and
// scopes all reads/writes to it: another user's ids behave as ErrNotFound.
package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrInvalid    = errors.New("invalid")
	ErrGenerating = errors.New("already generating")
)

const (
	StatusComplete   = "complete"
	StatusIncomplete = "incomplete" // legacy (<3.1); API reports it as "failed"
	StatusGenerating = "generating"
	StatusFailed     = "failed"
)

// GenStore supports detached generation (assistant row created up front).
type GenStore interface {
	// MarkGenerating sets the caller's assistant message to "generating" only
	// if it is not already generating (conditional UPDATE). Missing/foreign ->
	// ErrNotFound; already generating -> ErrGenerating.
	MarkGenerating(ctx context.Context, user, id string) error
	// FailStaleGenerating marks every "generating" row failed with
	// error_code "gateway_restart" (run at startup: no run survives a restart).
	FailStaleGenerating(ctx context.Context) (int64, error)
}

type Thread struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Archived           bool      `json:"archived"`
	MessageCount       int       `json:"message_count"`
	LastMessagePreview *string   `json:"last_message_preview"`
	ClientID           *string   `json:"client_id,omitempty"`
	UnreadCount        int       `json:"unread_count"` // unread proactive messages (ui-api-contract.md Q2)
}

type Usage struct {
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
}

type Feedback struct {
	MessageID string    `json:"message_id"`
	Rating    string    `json:"rating"`
	Reasons   []string  `json:"reasons"`
	Comment   *string   `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	Sources   []WebSource     `json:"sources,omitempty"` // web citations (assistant replies)
	ID        string          `json:"id"`
	ThreadID  string          `json:"thread_id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Reasoning *string         `json:"reasoning,omitempty"`
	Model     *string         `json:"model,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Usage     *Usage          `json:"usage,omitempty"`
	LatencyMs *int64          `json:"latency_ms,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Status    string          `json:"status"`
	ErrorCode *string         `json:"error_code,omitempty"` // API only, set when status is "failed"
	ClientID  *string         `json:"client_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
	Feedback  *Feedback       `json:"feedback,omitempty"`
	// Proactive fields (ui-api-contract.md Q1/Q3/Q4). All server-set.
	Origin       string      `json:"origin"` // user|assistant|proactive (DB user_turn -> role)
	JobID        *string     `json:"job_id"`
	ReadAt       *time.Time  `json:"read_at"`
	TemplateKind *string     `json:"template_kind"`
	Provenance   *Provenance `json:"provenance,omitempty"`
}

// Origin values in the DB.
const (
	OriginUserTurn  = "user_turn"
	OriginProactive = "proactive"
)

// APIOrigin maps the DB origin to the API value.
func APIOrigin(dbOrigin, role string) string {
	if dbOrigin == OriginProactive {
		return OriginProactive
	}
	if role == "user" {
		return "user"
	}
	return "assistant"
}

// ThreadCursor is a keyset position for ListThreads (updated_at desc, id desc).
type ThreadCursor struct {
	UpdatedAt time.Time
	ID        string
}

type SearchHit struct {
	ThreadID    string    `json:"thread_id"`
	ThreadTitle string    `json:"thread_title"`
	MessageID   *string   `json:"message_id"`
	Role        *string   `json:"role"`
	Snippet     string    `json:"snippet"`
	CreatedAt   time.Time `json:"created_at"`
	Match       string    `json:"match"` // "title" | "content"
}

type ImportMessage struct {
	ClientID  *string   `json:"client_id,omitempty"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Reasoning *string   `json:"reasoning,omitempty"`
	Model     *string   `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ImportThread struct {
	ClientID  string          `json:"client_id"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
	Archived  bool            `json:"archived"`
	Messages  []ImportMessage `json:"messages"`
}

type ThreadStore interface {
	CreateThread(ctx context.Context, user, title string) (Thread, error)
	GetThread(ctx context.Context, user, id string) (Thread, error)
	ListThreads(ctx context.Context, user string, archived bool, limit int, after *ThreadCursor) ([]Thread, error)
	UpdateThread(ctx context.Context, user, id string, title *string, archived *bool) (Thread, error)
	// ImportThread is idempotent on (user, ClientID): created=false if it already exists.
	ImportThread(ctx context.Context, user string, t ImportThread) (id string, created bool, err error)
}

type MessageStore interface {
	// AddMessage inserts m (ID/CreatedAt filled if empty) and bumps the thread's updated_at.
	AddMessage(ctx context.Context, user string, m Message) (Message, error)
	// ReplaceMessage overwrites content/reasoning/model/params/usage/latency/metadata/status
	// of an existing message, sets updated_at, keeps id and created_at, and clears feedback.
	ReplaceMessage(ctx context.Context, user string, m Message) (Message, error)
	GetMessage(ctx context.Context, user, id string) (Message, error)
	// ListMessages returns up to limit messages older than before (if set), oldest->newest.
	ListMessages(ctx context.Context, user, threadID string, limit int, before string) ([]Message, bool, error)
}

type FeedbackStore interface {
	// SetFeedback fully replaces the feedback on a message (reasons nil = none).
	SetFeedback(ctx context.Context, user, messageID, rating string, reasons []string, comment *string) (Feedback, error)
	DeleteFeedback(ctx context.Context, user, messageID string) error
	// ExportFeedback calls fn for every feedback row (all users) updated at or
	// after since, oldest first.
	ExportFeedback(ctx context.Context, since time.Time, fn func(FeedbackExport) error) error
}

// FeedbackExport is one evaluation row; JSON field order is the contract.
type FeedbackExport struct {
	Message   string    `json:"message"`
	Response  string    `json:"response"`
	Rating    string    `json:"rating"`
	Reasons   []string  `json:"reasons"`
	Comment   *string   `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	ThreadID  string    `json:"thread_id"`
	MessageID string    `json:"message_id"`
}

// Allowed feedback reason codes per rating (must match migration 00003).
var FeedbackReasons = map[string][]string{
	"down": {"wrong_info", "too_long", "misunderstood", "over_refusal", "bad_source", "other"},
	"up":   {"accurate", "concise", "helpful", "good_source"},
}

type SearchStore interface {
	Search(ctx context.Context, user, q string, limit, offset int) ([]SearchHit, error)
}

// Store is everything the handlers need.
type Store interface {
	ThreadStore
	MessageStore
	FeedbackStore
	SearchStore
	ActionStore
	Ping(ctx context.Context) error
	Close()
}

// UserIDResolver maps an authenticated login to its numeric users.id
// (creating the row on first use, like every other per-user write). Optional:
// the memory feature requires it and stays off without it.
type UserIDResolver interface {
	UserID(ctx context.Context, login string) (int64, error)
}

// NewID returns a ULID for t (time-sortable, 26 chars).
// NewID returns a ULID for t. IDs minted in the same millisecond are
// strictly increasing (monotonic entropy), so (created_at, id) ordering is
// stable even when timestamps tie.
func NewID(t time.Time) string {
	idMu.Lock()
	defer idMu.Unlock()
	ms := ulid.Timestamp(t)
	if ms < idLastMS {
		// clock went back or caller passed an older time: fresh entropy
		return ulid.MustNew(ms, rand.Reader).String()
	}
	idLastMS = ms
	return ulid.MustNew(ms, idEntropy).String()
}

var (
	idMu      sync.Mutex
	idLastMS  uint64
	idEntropy = ulid.Monotonic(rand.Reader, 0)
)

// Preview shortens s to n runes on one line.
func Preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// Snippet returns an HTML-safe excerpt of text around the first match of any
// term in q. Text is HTML-escaped first, then case-insensitive matches are
// wrapped in <mark>…</mark>; no other markup can appear.
func Snippet(text, q string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	terms := strings.Fields(strings.ToLower(q))
	rs := []rune(text)
	lower := []rune(strings.ToLower(text))
	if len(lower) != len(rs) { // case folding changed length; fall back to no-op lowering
		lower = rs
	}
	type span struct{ a, b int }
	var spans []span
	for i := 0; i < len(lower); {
		best := 0
		for _, t := range terms {
			tr := []rune(t)
			if len(tr) > best && i+len(tr) <= len(lower) && string(lower[i:i+len(tr)]) == t {
				best = len(tr)
			}
		}
		if best > 0 {
			spans = append(spans, span{i, i + best})
			i += best
		} else {
			i++
		}
	}
	start, end := 0, len(rs)
	if len(rs) > width {
		c := 0
		if len(spans) > 0 {
			c = spans[0].a
		}
		start = max(0, c-width/3)
		end = min(len(rs), start+width)
		start = max(0, end-width)
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	pos := start
	for _, sp := range spans {
		if sp.b <= start || sp.a >= end {
			continue
		}
		a, z := max(sp.a, start), min(sp.b, end)
		b.WriteString(html.EscapeString(string(rs[pos:a])))
		b.WriteString("<mark>" + html.EscapeString(string(rs[a:z])) + "</mark>")
		pos = z
	}
	b.WriteString(html.EscapeString(string(rs[pos:end])))
	if end < len(rs) {
		b.WriteString("…")
	}
	return b.String()
}

// UserSettingsStore persists users.settings (namespaced JSONB keys). Optional.
type UserSettingsStore interface {
	// UserSettings returns the raw settings object ("{}" when unset).
	UserSettings(ctx context.Context, login string) (json.RawMessage, error)
	// MergeUserSettings shallow-merges patch into settings[ns] (other
	// namespaces and unknown keys are preserved) and returns the result.
	MergeUserSettings(ctx context.Context, login, ns string, patch json.RawMessage) (json.RawMessage, error)
}
