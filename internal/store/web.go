package store

import (
	"context"
	"encoding/json"
	"time"
)

// ToolCall is one web tool invocation attached to an assistant message.
// Args hold the query/URL (user data, purged after the retention period);
// raw tool results are never stored.
type ToolCall struct {
	ID          string          `json:"id"`
	MessageID   string          `json:"message_id"`
	Round       int             `json:"round"`
	Seq         int             `json:"seq"`
	Tool        string          `json:"tool"`
	Provider    *string         `json:"provider,omitempty"`
	Args        json.RawMessage `json:"args"`
	Status      string          `json:"status"`
	Error       *string         `json:"error,omitempty"`
	LatencyMs   int64           `json:"latency_ms"`
	ResultChars int             `json:"result_chars"`
	CreatedAt   time.Time       `json:"created_at"`
	// Added in 6.0.0 (migration 00009); nil for older rows.
	SelectedBy    *string    `json:"selected_by,omitempty"`
	ErrorCode     *string    `json:"error_code,omitempty"`
	ResultCount   *int       `json:"result_count,omitempty"`
	ArgsExpiresAt *time.Time `json:"args_expires_at,omitempty"`
}

// WebSource is a numbered citation for an assistant message (metadata only).
type WebSource struct {
	MessageID     string    `json:"-"`
	ToolCallSeq   *int      `json:"-"` // index into the ToolCall slice passed to SaveWebRun
	N             int       `json:"n"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Domain        string    `json:"domain"`
	Snippet       string    `json:"snippet,omitempty"`
	Fetched       bool      `json:"fetched"`
	FinalURL      string    `json:"final_url,omitempty"`
	ContentSHA256 string    `json:"-"`
	Cited         bool      `json:"cited"`
	CreatedAt     time.Time `json:"-"`
}

// WebStore is implemented by stores that persist web tool history.
type WebStore interface {
	// SaveWebRun replaces all tool_calls/web_sources of messageID (regenerate = replace).
	SaveWebRun(ctx context.Context, user, messageID string, calls []ToolCall, sources []WebSource) error
	// Sources returns citations per message id (only the caller's messages).
	Sources(ctx context.Context, user string, messageIDs []string) (map[string][]WebSource, error)
	// ToolCallsByMessage returns the stored tool calls (tool, status; args are
	// not needed) per message id, only for the caller's messages.
	ToolCallsByMessage(ctx context.Context, user string, messageIDs []string) (map[string][]ToolCall, error)
	// PurgeWebHistory deletes tool_calls and web_sources created before cutoff.
	PurgeWebHistory(ctx context.Context, cutoff time.Time) (int64, error)
	// ClearExpiredArgs sets args to {} on every tool_calls row whose
	// args_expires_at is before now. The row itself stays.
	ClearExpiredArgs(ctx context.Context, now time.Time) (int64, error)
}

// ThreadSettingsStore persists per-thread settings JSON ({"web_search":"auto"}).
type ThreadSettingsStore interface {
	ThreadSettings(ctx context.Context, user, threadID string) (json.RawMessage, error)
	SetThreadSettings(ctx context.Context, user, threadID string, settings json.RawMessage) error
}
