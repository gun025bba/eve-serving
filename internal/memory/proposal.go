package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oklog/ulid/v2"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

const (
	ToolPropose        = "memory.propose"
	ToolProposeVersion = "1"
	ScopeWrite         = "memory.write"
	ProposalTTL        = 24 * time.Hour

	// Static card verbs for memory.propose (Q19). The proposal kind is
	// chosen by the server (AddProposal / ForgetProposal), never by model
	// args, so each kind has its own fixed verb.
	VerbAdd    = tools.VerbAdd
	VerbForget = tools.VerbDelete
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

type PayloadItem struct {
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Payload is what an approved memory.propose executes. items carry ids for
// delete (sorted, so payload_hash binds the exact id set, S10-L4) and the
// normalized text for add.
type Payload struct {
	Op    string        `json:"op"`
	Items []PayloadItem `json:"items"`
}

// ProposalCtx is server-set provenance; never taken from model output.
type ProposalCtx struct {
	ThreadID, MessageID string
	Actor               string // "user" | "scheduler"
	Tainted             bool
	// ActionID/ActionCreated make Execute idempotent: item ids are derived
	// from them, so a retry after a failed execution (card released back to
	// pending) never adds duplicates.
	ActionID      string
	ActionCreated time.Time
}

// Check enforces scheduler/taint denial.
func (c ProposalCtx) Check() error {
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
func ForgetProposal(st Store, uid int64, ids []string, ambiguous bool, c ProposalCtx, now time.Time) (store.Action, error) {
	if err := c.Check(); err != nil {
		return store.Action{}, err
	}
	ids = SortedIDs(ids)
	if len(ids) == 0 {
		return store.Action{}, ErrNoMatch
	}
	d, err := st.Load(uid)
	if err != nil {
		return store.Action{}, err
	}
	p := Payload{Op: "delete"}
	view := struct {
		Verb      string     `json:"verb"`
		Op        string     `json:"op"`
		Items     []viewItem `json:"items"`
		Ambiguous bool       `json:"ambiguous"`
	}{Verb: VerbForget, Op: "delete", Ambiguous: ambiguous || len(ids) > 1}
	for _, id := range ids {
		i := d.find(id)
		if i < 0 {
			return store.Action{}, ErrNotFound
		}
		it := d.Items[i]
		u := it.UpdatedAt
		p.Items = append(p.Items, PayloadItem{ID: it.ID})
		view.Items = append(view.Items, viewItem{ID: it.ID, Text: it.Text, Kind: it.Kind, UpdatedAt: &u})
	}
	return action(c, "메모리에서 삭제", p, view, now)
}

// AddProposal builds a confirmation card for a model-suggested item; the
// card shows the normalized text that will be stored (S5-M3).
func AddProposal(text, kind string, c ProposalCtx, now time.Time) (store.Action, error) {
	if err := c.Check(); err != nil {
		return store.Action{}, err
	}
	if !kinds[kind] {
		return store.Action{}, ErrInvalid
	}
	t, err := Clean(text)
	if err != nil {
		return store.Action{}, err
	}
	p := Payload{Op: "add", Items: []PayloadItem{{Text: t, Kind: kind}}}
	view := struct {
		Verb      string     `json:"verb"`
		Op        string     `json:"op"`
		Items     []viewItem `json:"items"`
		Ambiguous bool       `json:"ambiguous"`
	}{Verb: VerbAdd, Op: "add", Items: []viewItem{{Text: t, Kind: kind}}}
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
func Revalidate(st Store, uid int64, raw json.RawMessage, c ProposalCtx) error {
	if err := c.Check(); err != nil {
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
	d, err := st.Load(uid)
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
func Execute(st Store, uid int64, raw json.RawMessage, c ProposalCtx, src *Source, now time.Time) ([]string, error) {
	if err := Revalidate(st, uid, raw, c); err != nil {
		return nil, err
	}
	p, _ := parsePayload(raw)
	var ids []string
	if p.Op == "delete" {
		for _, it := range p.Items {
			ids = append(ids, it.ID)
		}
		return ids, Delete(st, uid, ids, now)
	}
	// one atomic Update for all items (no partial adds); ids are derived
	// from the action, so a repeated Execute is a no-op for items present.
	var add []Item
	for i, it := range p.Items {
		t, err := Clean(it.Text)
		if err != nil {
			return nil, err
		}
		n := now.UTC()
		item := Item{ID: actionItemID(c, i, n), Kind: it.Kind, Text: t, Pinned: it.Kind == "profile", Confidence: "confirmed",
			Source: src, CreatedAt: n, UpdatedAt: n}
		if it.Kind == "task" {
			e := n.Add(TaskTTL)
			item.ExpiresAt = &e
		}
		add = append(add, item)
		ids = append(ids, item.ID)
	}
	_, err := st.Update(uid, func(d *Doc) error {
		have := map[string]bool{}
		for _, x := range d.Items {
			have[x.ID] = true
		}
		// a retried Execute must not resurrect an item the user deleted
		// after the first run (security Low #44).
		for _, t := range d.Tombstones {
			have[t.ID] = true
		}
		for _, it := range add {
			if have[it.ID] {
				continue
			}
			if len(d.Items) >= MaxItems {
				return ErrTooMany
			}
			d.Items = append(d.Items, it)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// actionItemID is a ULID whose time is the action's creation and whose
// entropy is sha256(action id, index); without an action id it is random.
func actionItemID(c ProposalCtx, i int, now time.Time) string {
	if c.ActionID == "" {
		return ulid.Make().String()
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", c.ActionID, i)))
	at := c.ActionCreated
	if at.IsZero() {
		at = now
	}
	return ulid.MustNew(ulid.Timestamp(at), bytes.NewReader(h[:])).String()
}
