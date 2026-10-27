// Package pim is the gateway side of Jarvis M1 (issue #69): a client for the
// eve-pim helper (pim/eve-pim, #68) and the read-only model tools
// calendar_list_events and reminders_list. Only read methods are wired here;
// writes (#70) are not implemented.
//
// Nothing in this package logs or returns event or reminder content other
// than through the tool result handed to the model.
package pim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// Error codes returned to the model and written to logs (design §1).
const (
	CodeUnavailable      = "pim_unavailable"
	CodePermissionDenied = "permission_denied"
	CodeKillSwitch       = "kill_switch"
	CodeTimeout          = "timeout"
	CodeInvalidArgs      = "invalid_arguments"
)

// Caps (design §1, security condition). The helper enforces them too.
const (
	MaxRange     = 31 * 24 * time.Hour
	MaxEvents    = 50
	MaxReminders = 100
	maxResponse  = 512 << 10
)

// Error carries only a code, never content.
type Error struct{ Code string }

func (e *Error) Error() string { return "pim: " + e.Code }

// CodeOf maps any error to one of the public codes.
func CodeOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeUnavailable
}

type Event struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day"`
	Calendar string `json:"calendar"`
}

type Reminder struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Due   *string `json:"due"`
	List  string  `json:"list"`
	Done  bool    `json:"done"`
}

type Events struct {
	Events    []Event `json:"events"`
	Truncated bool    `json:"truncated"`
}

type Reminders struct {
	Reminders []Reminder `json:"reminders"`
	Truncated bool       `json:"truncated"`
}

// Reader is the read surface the tools and the briefing job use. *Client
// implements it; tests use fakes.
type Reader interface {
	ListEvents(ctx context.Context, from, to time.Time) (Events, error)
	ListReminders(ctx context.Context, dueBefore *time.Time, includeDone bool) (Reminders, error)
}

// Client talks to eve-pim over its unix socket, one request per connection.
type Client struct {
	SocketPath string
	Timeout    time.Duration // default 5s
	// Dial is a test seam; nil = unix socket dial of SocketPath.
	Dial func(ctx context.Context) (net.Conn, error)
	seq  atomic.Uint64
}

func NewClient(socketPath string) *Client { return &Client{SocketPath: socketPath} }

func (c *Client) nextID() string { return "g" + strconv.FormatUint(c.seq.Add(1), 10) }

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// requestLine encodes one request exactly as eve-pim's fixed schema expects
// (key order is stable; see pim/eve-pim/testdata/protocol/gateway_requests.jsonl).
func requestLine(id, method string, params any) ([]byte, error) {
	type req struct {
		V      int    `json:"v"`
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}
	b, err := json.Marshal(req{1, id, method, params})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type eventsParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type remindersParams struct {
	DueBefore   *string `json:"due_before,omitempty"`
	IncludeDone bool    `json:"include_done"`
}

func (c *Client) ListEvents(ctx context.Context, from, to time.Time) (Events, error) {
	var out Events
	if !to.After(from) || to.Sub(from) > MaxRange {
		return out, &Error{CodeInvalidArgs}
	}
	err := c.call(ctx, "list_events", eventsParams{rfc3339(from), rfc3339(to)}, &out)
	if len(out.Events) > MaxEvents {
		out.Events, out.Truncated = out.Events[:MaxEvents], true
	}
	return out, err
}

func (c *Client) ListReminders(ctx context.Context, dueBefore *time.Time, includeDone bool) (Reminders, error) {
	var out Reminders
	p := remindersParams{IncludeDone: includeDone}
	if dueBefore != nil {
		s := rfc3339(*dueBefore)
		p.DueBefore = &s
	}
	err := c.call(ctx, "list_reminders", p, &out)
	if len(out.Reminders) > MaxReminders {
		out.Reminders, out.Truncated = out.Reminders[:MaxReminders], true
	}
	return out, err
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	line, err := requestLine(c.nextID(), method, params)
	if err != nil {
		return &Error{CodeUnavailable}
	}
	dial := c.Dial
	if dial == nil {
		dial = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.SocketPath)
		}
	}
	conn, err := dial(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return &Error{CodeTimeout}
		}
		return &Error{CodeUnavailable}
	}
	defer conn.Close()
	dl, _ := ctx.Deadline()
	conn.SetDeadline(dl)
	if _, err := conn.Write(line); err != nil {
		return netErr(err)
	}
	raw, err := bufio.NewReaderSize(io.LimitReader(conn, maxResponse+1), 4096).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(raw) > 0) {
		return netErr(err)
	}
	if len(raw) > maxResponse {
		return &Error{CodeUnavailable}
	}
	var resp struct {
		V      int             `json:"v"`
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || resp.V != 1 {
		return &Error{CodeUnavailable}
	}
	if !resp.OK {
		if resp.Error == "permission_denied" {
			return &Error{CodePermissionDenied}
		}
		return &Error{CodeUnavailable}
	}
	if json.Unmarshal(resp.Result, result) != nil {
		return &Error{CodeUnavailable}
	}
	return nil
}

func netErr(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{CodeTimeout}
	}
	return &Error{CodeUnavailable}
}
