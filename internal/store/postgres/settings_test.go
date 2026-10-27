package postgres

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUserSettings_DefaultEmptyMergePreservesUnknown(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	b, err := s.UserSettings(ctx, "alice")
	if err != nil || string(b) != "{}" {
		t.Fatal(string(b), err)
	}
	// a future/unknown namespace and an unknown key inside memory survive
	if _, err := s.pool.Exec(ctx, `UPDATE users SET settings = '{"future":{"x":1},"memory":{"other":"k"}}' WHERE login = 'alice'`); err != nil {
		t.Fatal(err)
	}
	b, err = s.MergeUserSettings(ctx, "alice", "memory", json.RawMessage(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	json.Unmarshal(b, &m)
	if m["future"]["x"] != float64(1) || m["memory"]["other"] != "k" || m["memory"]["enabled"] != true {
		t.Fatal(string(b))
	}
	// other users untouched
	if b, _ := s.UserSettings(ctx, "bob"); string(b) != "{}" {
		t.Fatal(string(b))
	}
}
