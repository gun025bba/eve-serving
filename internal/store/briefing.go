package store

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

// Briefing is the structured part of a briefing Message (design §11.3). It is
// stored in messages.metadata under the key "briefing" (no new column or
// table) and served as the Message's top-level "briefing" object; messages
// that are not briefings serve "briefing": null. Only fields declared here
// reach the API, and no opaque ids are ever stored in it.
type Briefing struct {
	Schema      int               `json:"schema"`
	Status      string            `json:"status"` // ok | partial | unavailable
	Date        string            `json:"date"`
	TZ          string            `json:"tz"`
	GeneratedAt string            `json:"generated_at"` // RFC3339, +09:00
	TextSource  string            `json:"text_source"`  // model | template | fixed
	Sections    []BriefingSection `json:"sections"`
}

// BriefingSection is one data section; Items hold BriefingEvent or
// BriefingReminder values by Kind.
type BriefingSection struct {
	Kind      string `json:"kind"`   // events | reminders
	Status    string `json:"status"` // ok | error
	ErrorCode string `json:"error_code,omitempty"`
	Count     int    `json:"count"`     // collected in total
	Truncated bool   `json:"truncated"` // shown items < collected
	Items     []any  `json:"items"`     // at most MaxBriefingItems
}

type BriefingEvent struct {
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day"`
	Calendar string `json:"calendar"`
}

type BriefingReminder struct {
	Title   string  `json:"title"`
	Due     *string `json:"due"`
	List    string  `json:"list"`
	Overdue bool    `json:"overdue"`
}

// Briefing constants.
const (
	BriefingSchema   = 1
	MaxBriefingItems = 10
	BriefingKind     = "briefing" // messages.template_kind

	BriefingOK          = "ok"
	BriefingPartial     = "partial"
	BriefingUnavailable = "unavailable"
)

// BriefingMetadata is the messages.metadata value for a briefing message.
func BriefingMetadata(b Briefing) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"briefing": b})
	return raw
}

var errCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

func strictDecode(raw json.RawMessage, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

// ParseBriefing validates stored metadata and returns the API object, or nil
// when it is absent or invalid (the Message then just shows its content).
func ParseBriefing(meta json.RawMessage) *Briefing {
	if len(meta) == 0 {
		return nil
	}
	var outer struct {
		Briefing *struct {
			Schema      int    `json:"schema"`
			Status      string `json:"status"`
			Date        string `json:"date"`
			TZ          string `json:"tz"`
			GeneratedAt string `json:"generated_at"`
			TextSource  string `json:"text_source"`
			Sections    []struct {
				Kind      string            `json:"kind"`
				Status    string            `json:"status"`
				ErrorCode string            `json:"error_code"`
				Count     int               `json:"count"`
				Truncated bool              `json:"truncated"`
				Items     []json.RawMessage `json:"items"`
			} `json:"sections"`
		} `json:"briefing"`
	}
	if json.Unmarshal(meta, &outer) != nil || outer.Briefing == nil {
		return nil
	}
	in := outer.Briefing
	if in.Schema != BriefingSchema || len(in.TZ) == 0 || len(in.TZ) > 64 {
		return nil
	}
	switch in.Status {
	case BriefingOK, BriefingPartial, BriefingUnavailable:
	default:
		return nil
	}
	switch in.TextSource {
	case "model", "template", "fixed":
	default:
		return nil
	}
	if _, err := time.Parse("2006-01-02", in.Date); err != nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, in.GeneratedAt); err != nil {
		return nil
	}
	out := &Briefing{Schema: in.Schema, Status: in.Status, Date: in.Date, TZ: in.TZ, GeneratedAt: in.GeneratedAt,
		TextSource: in.TextSource, Sections: []BriefingSection{}}
	want := []string{"events", "reminders"}
	if len(in.Sections) != 0 && len(in.Sections) != len(want) {
		return nil
	}
	for i, s := range in.Sections {
		if s.Kind != want[i] || (s.Status != "ok" && s.Status != "error") || s.Count < 0 || len(s.Items) > MaxBriefingItems {
			return nil
		}
		if (s.Status == "error") != (s.ErrorCode != "") || (s.ErrorCode != "" && !errCodeRe.MatchString(s.ErrorCode)) {
			return nil
		}
		sec := BriefingSection{Kind: s.Kind, Status: s.Status, ErrorCode: s.ErrorCode, Count: s.Count, Truncated: s.Truncated, Items: []any{}}
		for _, raw := range s.Items {
			if s.Kind == "events" {
				var e BriefingEvent
				if !strictDecode(raw, &e) {
					return nil
				}
				sec.Items = append(sec.Items, e)
			} else {
				var r BriefingReminder
				if !strictDecode(raw, &r) {
					return nil
				}
				sec.Items = append(sec.Items, r)
			}
		}
		out.Sections = append(out.Sections, sec)
	}
	return out
}

// MarshalJSON serves the Message with its top-level "briefing" object: the
// validated projection of metadata for template_kind "briefing" messages
// (their raw metadata is then not repeated), null for every other message.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	out := struct {
		plain
		Briefing *Briefing `json:"briefing"`
	}{plain: plain(m)}
	if m.TemplateKind != nil && *m.TemplateKind == BriefingKind {
		out.Briefing = ParseBriefing(m.Metadata)
		out.plain.Metadata = nil
	}
	return json.Marshal(out)
}
