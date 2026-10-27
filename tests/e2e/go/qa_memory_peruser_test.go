package handlers

// QA (PR #40): per-user memory.enabled isolation; memory_available tracks global flag.
import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/memory"
)

func TestQA_PerUserMemoryIsolation(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	memory.Add(fs, uidOf(t, e, "alice@example"), memory.NewItem{Kind: "fact", Text: "앨리스는 고양이를 키운다", Confidence: "user"}, time.Now())
	memory.Add(fs, uidOf(t, e, "bob@example"), memory.NewItem{Kind: "fact", Text: "밥은 강아지를 키운다", Confidence: "user"}, time.Now())
	if r, b := e.call(t, "bob@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false}}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	// B off: no injection
	e.call(t, "bob@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if up := fmt.Sprint(e.lastUpstream()); strings.Contains(up, "user_memory") || strings.Contains(up, "강아지") {
		t.Error("B (off) got memory injected")
	}
	// A on: own memory only
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	up := fmt.Sprint(e.lastUpstream())
	if !strings.Contains(up, "고양이") || strings.Contains(up, "강아지") {
		t.Error("A (on) injection wrong: missing own or contains B's memory")
	}
	// settings views
	_, ba := e.call(t, "alice@example", "GET", "/v1/settings", "")
	_, bb := e.call(t, "bob@example", "GET", "/v1/settings", "")
	if !strings.Contains(string(ba), `"enabled":true`) || !strings.Contains(string(bb), `"enabled":false`) ||
		!strings.Contains(string(ba), `"memory_available":true`) || !strings.Contains(string(bb), `"memory_available":true`) {
		t.Errorf("settings A=%s B=%s", ba, bb)
	}
	// B endpoints while off (ui-api-contract §3.9: view/edit/delete/export keep working
	// so the user can erase; injection and propose do not happen).
	for _, c := range []struct {
		m, p, body string
		want       int
	}{
		{"GET", "/v1/memory", "", 200}, {"GET", "/v1/memory/export", "", 200},
		{"POST", "/v1/memory/items", `{"kind":"fact","text":"새 항목"}`, 0}, {"DELETE", "/v1/memory", "", 204},
	} {
		r, _ := e.call(t, "bob@example", c.m, c.p, c.body)
		t.Logf("B off %s %s -> %d", c.m, c.p, r.StatusCode)
		if c.want != 0 && r.StatusCode != c.want {
			t.Errorf("B off %s %s: %d want %d", c.m, c.p, r.StatusCode, c.want)
		}
	}
	// A unaffected by B's toggle
	if r, _ := e.call(t, "alice@example", "GET", "/v1/memory", ""); r.StatusCode != 200 {
		t.Errorf("A GET /v1/memory %d", r.StatusCode)
	}
}

func TestQA_MemoryAvailableTracksGlobal(t *testing.T) {
	for _, on := range []bool{true, false} {
		e, _ := newMemEnv(t, on, &failLedger{})
		_, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
		if !strings.Contains(string(b), fmt.Sprintf(`"memory_available":%v`, on)) {
			t.Errorf("global=%v settings=%s", on, b)
		}
		if !on && strings.Contains(string(b), `"memory":`) {
			t.Errorf("global off leaks per-user memory key: %s", b)
		}
	}
}
