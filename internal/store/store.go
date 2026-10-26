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
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

const (
	StatusComplete   = "complete"
	StatusIncomplete = "incomplete"
)

type Thread struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Archived           bool      `json:"archived"`
	MessageCount       int       `json:"message_count"`
	LastMessagePreview *string   `json:"last_message_preview"`
	ClientID           *string   `json:"client_id,omitempty"`
}

type Usage struct {
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
}

type Feedback struct {
	MessageID string    `json:"message_id"`
	Rating    string    `json:"rating"`
	Comment   *string   `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
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
	ClientID  *string         `json:"client_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
	Feedback  *Feedback       `json:"feedback,omitempty"`
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
	SetFeedback(ctx context.Context, user, messageID, rating string, comment *string) (Feedback, error)
	DeleteFeedback(ctx context.Context, user, messageID string) error
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
	Ping(ctx context.Context) error
	Close()
}

// NewID returns a ULID for t (time-sortable, 26 chars).
func NewID(t time.Time) string {
	return ulid.MustNew(ulid.Timestamp(t), rand.Reader).String()
}

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
