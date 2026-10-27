package memory

import (
	"encoding/json"
	"strings"
	"time"
)

// ToolName is the model-facing function name of memory.propose (function
// names cannot contain '.'). The tool never writes: it only creates a
// confirmation card (docs/architecture/memory.md §2, ui-api-contract §3.9.4).
const ToolName = "memory_propose"

// ToolDef is the OpenAI-style tool definition exposed only while memory is on.
func ToolDef() map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{
		"name": ToolName,
		"description": "Propose saving or forgetting a fact about the user. Nothing is stored until the user approves a card. " +
			"Use op=add with text+kind for things the user explicitly asked you to remember; use op=forget with query when the user asks to forget something (\"잊어줘\").",
		"parameters": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"op"},
			"properties": map[string]any{
				"op":    map[string]any{"type": "string", "enum": []string{"add", "forget"}},
				"text":  map[string]any{"type": "string", "description": "op=add: the short fact to remember"},
				"kind":  map[string]any{"type": "string", "enum": []string{"profile", "preference", "fact", "task"}},
				"query": map[string]any{"type": "string", "description": "op=forget: words identifying the item(s) to forget"},
			}},
	}}
}

// ToolArgs is the parsed model call.
type ToolArgs struct {
	Op    string `json:"op"`
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Query string `json:"query"`
}

func ParseToolArgs(raw string) (ToolArgs, error) {
	var a ToolArgs
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil || (a.Op != "add" && a.Op != "forget") {
		return ToolArgs{}, ErrInvalid
	}
	if a.Op == "add" && a.Kind == "" {
		a.Kind = "fact"
	}
	return a, nil
}

// MatchForget returns ids of live items matching query (normalized,
// case-insensitive substring either way), at most MaxForgetMatches.
// Empty result = no card (the model answers, Q16).
const MaxForgetMatches = 10

func MatchForget(items []Item, query string, now time.Time) []string {
	q := strings.ToLower(Normalize(query))
	if q == "" {
		return nil
	}
	var ids []string
	for _, it := range Live(Doc{Items: items}, now) {
		t := strings.ToLower(it.Text)
		if strings.Contains(t, q) || strings.Contains(q, t) {
			ids = append(ids, it.ID)
			if len(ids) == MaxForgetMatches {
				break
			}
		}
	}
	return ids
}
