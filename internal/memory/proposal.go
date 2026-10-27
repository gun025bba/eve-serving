package memory

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

const (
	ToolPropose        = "memory.propose"
	ToolProposeVersion = "1"
	ScopeWrite         = "memory.write"
	ProposalTTL        = 24 * time.Hour
)

var (
	ErrTainted   = errors.New("memory: denied in tainted thread")
	ErrScheduler = errors.New("memory: scheduler cannot write")
	ErrNoMatch   = errors.New("memory: no matching items") // no card; the model answers (Q16)
)

type viewItem struct {
	ID        string     `json:"id,omitempty"`
	Text      string     `json:"text"`
	Kind      string     `json:"kind"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type payloadItem struct {
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Payload is what an approved memory.propose executes. items carry ids for
// delete (sorted, so payload_hash binds the exact id set, S10-L4) and the
// normalized text for add.
type Payload struct {
	Op    string        `json:"op"`
	Items []payloadItem `json:"items"`
}

// ProposalCtx is server-set provenance; never taken from model output.
type ProposalCtx struct {
	ThreadID, MessageID string
	Actor               string // "user" | "scheduler"
	Tainted             bool
}

func (c ProposalCtx) check() error {
	if c.Actor == "scheduler" {
		return ErrScheduler
	}
	if c.Tainted {
		return ErrTainted
	}
	return nil
}

func action(c ProposalCtx, summary string, payload, view any, now time.Time) (store.Action, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return store.Action{}, err
	}
	v, err := json.Marshal(view)
	if err != nil {
		return store.Action{}, err
	}
	a := store.Action{Payload: p}
	a.Tool, a.ToolVersion, a.Scopes = ToolPropose, ToolProposeVersion, []string{ScopeWrite}
	a.PayloadHash = store.PayloadHash(p)
	a.Provenance = store.Provenance{Actor: c.Actor}
	a.Tainted = c.Tainted
	a.Status = "pending"
	a.CreatedAt = now.UTC()
	a.ExpiresAt = a.CreatedAt.Add(ProposalTTL)
	if c.ThreadID != "" {
		t := c.ThreadID
		a.ThreadID = &t
	}
	if c.MessageID != "" {
		m := c.MessageID
		a.MessageID = &m
	}
	a.Summary, a.PayloadView = summary, v
	return a, nil
}

// ForgetProposal builds the "잊어줘" delete card for the matched item ids.
// Only ids present in the user's file are accepted; ambiguous=true shows the
// candidates as checkboxes (partial selection = revise with item_ids).
// There is no path that deletes without a card.
func ForgetProposal(st Store, user string, ids []string, ambiguous bool, c ProposalCtx, now time.Time) (store.Action, error) {
	if err := c.check(); err != nil {
		return store.Action{}, err
	}
	ids = SortedIDs(ids)
	if len(ids) == 0 {
		return store.Action{}, ErrNoMatch
	}
	d, err := st.Load(user)
	if err != nil {
		return store.Action{}, err
	}
	p := Payload{Op: "delete"}
	view := struct {
		Op        string     `json:"op"`
		Items     []viewItem `json:"items"`
		Ambiguous bool       `json:"ambiguous"`
	}{Op: "delete", Ambiguous: ambiguous || len(ids) > 1}
	for _, id := range ids {
		i := d.find(id)
		if i < 0 {
			return store.Action{}, ErrNotFound
		}
		it := d.Items[i]
		u := it.UpdatedAt
		p.Items = append(p.Items, payloadItem{ID: it.ID})
		view.Items = append(view.Items, viewItem{ID: it.ID, Text: it.Text, Kind: it.Kind, UpdatedAt: &u})
	}
	return action(c, "메모리에서 삭제", p, view, now)
}

// AddProposal builds a confirmation card for a model-suggested item; the
// card shows the normalized text that will be stored (S5-M3).
func AddProposal(text, kind string, c ProposalCtx, now time.Time) (store.Action, error) {
	if err := c.check(); err != nil {
		return store.Action{}, err
	}
	if !kinds[kind] {
		return store.Action{}, ErrInvalid
	}
	t, err := Clean(text)
	if err != nil {
		return store.Action{}, err
	}
	p := Payload{Op: "add", Items: []payloadItem{{Text: t, Kind: kind}}}
	view := struct {
		Op        string     `json:"op"`
		Items     []viewItem `json:"items"`
		Ambiguous bool       `json:"ambiguous"`
	}{Op: "add", Items: []viewItem{{Text: t, Kind: kind}}}
	return action(c, "메모리에 추가", p, view, now)
}

func parsePayload(raw json.RawMessage) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil || (p.Op != "add" && p.Op != "delete") || len(p.Items) == 0 {
		return Payload{}, ErrInvalid
	}
	return p, nil
}

// Revalidate re-runs the creation checks on a (possibly item_ids-filtered)
// payload before a revise is accepted (ui-api-contract §3.1): delete ids must
// still be owned (else ErrNotFound -> 409 item_gone), add text must still
// pass normalization and the secret filter.
func Revalidate(st Store, user string, raw json.RawMessage, c ProposalCtx) error {
	if err := c.check(); err != nil {
		return err
	}
	p, err := parsePayload(raw)
	if err != nil {
		return err
	}
	if p.Op == "add" {
		for _, it := range p.Items {
			t, err := Clean(it.Text)
			if err != nil {
				return err
			}
			if t != it.Text || !kinds[it.Kind] {
				return ErrInvalid
			}
		}
		return nil
	}
	d, err := st.Load(user)
	if err != nil {
		return err
	}
	for _, it := range p.Items {
		if d.find(it.ID) < 0 {
			return ErrNotFound
		}
	}
	return nil
}

// Execute applies an approved memory.propose payload. Callers must have
// verified the confirm token and that store.PayloadHash(raw) equals the
// card's payload_hash. The approve endpoint is P1 follow-up work.
func Execute(st Store, user string, raw json.RawMessage, c ProposalCtx, src *Source, now time.Time) ([]string, error) {
	if err := Revalidate(st, user, raw, c); err != nil {
		return nil, err
	}
	p, _ := parsePayload(raw)
	var ids []string
	if p.Op == "delete" {
		for _, it := range p.Items {
			ids = append(ids, it.ID)
		}
		return ids, Delete(st, user, ids, now)
	}
	for _, it := range p.Items {
		n, err := Add(st, user, NewItem{Kind: it.Kind, Text: it.Text, Confidence: "confirmed", Source: src}, now)
		if err != nil {
			return ids, err
		}
		ids = append(ids, n.ID)
	}
	return ids, nil
}
