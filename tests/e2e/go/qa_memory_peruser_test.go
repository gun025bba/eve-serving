package handlers

// QA (PR #40) per architect decision (memory contract):
//  1. users.settings memory.enabled defaults to off;
//  2. settings exposes system.memory_available only; read-only is signaled solely by
//     503 memory_read_only, so no memory_read_only field may appear in settings;
//  3. user off: POST /v1/memory/items, propose and approve -> 403 kill_switch;
//     GET / PATCH item / DELETE / export keep working.
import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

func TestQA_MemoryDefaultOff(t *testing.T) {
	srv := &Server{Store: memstore.New()}
	u, err := srv.loadUserSettings(httptestReq("carol@example"))
	if err != nil || u.Memory.Enabled {
		t.Fatalf("missing settings must read as off: enabled=%v err=%v", u.Memory.Enabled, err)
	}
}

func TestQA_SettingsFieldsContract(t *testing.T) {
	for _, on := range []bool{true, false} {
		e, _ := newMemEnv(t, on, &failLedger{})
		_, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
		s := string(b)
		if !strings.Contains(s, fmt.Sprintf(`"memory_available":%v`, on)) {
			t.Errorf("global=%v: memory_available missing/wrong: %s", on, s)
		}
		if strings.Contains(s, "memory_read_only") {
			t.Errorf("global=%v: settings must not carry memory_read_only: %s", on, s)
		}
		if !on && strings.Contains(s, `"memory":`) {
			t.Errorf("global off leaks per-user memory key: %s", s)
		}
		if r, _ := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"system":{"memory_read_only":true}}`); r.StatusCode != 400 {
			t.Errorf("global=%v: PATCH system.* must be 400, got %d", on, r.StatusCode)
		}
	}
}

func TestQA_PerUserMemoryIsolation(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	memory.Add(fs, uidOf(t, e, "alice@example"), memory.NewItem{Kind: "fact", Text: "앨리스는 고양이를 키운다", Confidence: "user"}, time.Now())
	bi, _ := memory.Add(fs, uidOf(t, e, "bob@example"), memory.NewItem{Kind: "fact", Text: "밥은 강아지를 키운다", Confidence: "user"}, time.Now())
	if r, b := e.call(t, "bob@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false}}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	e.call(t, "bob@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if up := fmt.Sprint(e.lastUpstream()); strings.Contains(up, "user_memory") || strings.Contains(up, "강아지") {
		t.Error("B (off) got memory injected")
	}
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if up := fmt.Sprint(e.lastUpstream()); !strings.Contains(up, "고양이") || strings.Contains(up, "강아지") {
		t.Error("A (on) injection wrong: missing own or contains B's memory")
	}
	_, ba := e.call(t, "alice@example", "GET", "/v1/settings", "")
	_, bb := e.call(t, "bob@example", "GET", "/v1/settings", "")
	if !strings.Contains(string(ba), `"enabled":true`) || !strings.Contains(string(bb), `"enabled":false`) {
		t.Errorf("settings A=%s B=%s", ba, bb)
	}
	// B off: management works
	for _, c := range []struct {
		name, m, p, body string
		want             int
	}{
		{"list", "GET", "/v1/memory", "", 200},
		{"export", "GET", "/v1/memory/export", "", 200},
		{"edit", "PATCH", "/v1/memory/items/" + bi.ID, `{"pinned":true}`, 200},
		{"delete item", "DELETE", "/v1/memory/items/" + bi.ID, "", 204},
		{"delete all", "DELETE", "/v1/memory", "", 204},
	} {
		if r, b := e.call(t, "bob@example", c.m, c.p, c.body); r.StatusCode != c.want {
			t.Errorf("B off %s: %d want %d %s", c.name, r.StatusCode, c.want, b)
		}
	}
	// B off: new writes refused with 403 kill_switch, nothing stored
	r, b := e.call(t, "bob@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"새 항목"}`)
	if r.StatusCode != 403 || !strings.Contains(string(b), "kill_switch") {
		t.Errorf("B off POST /v1/memory/items: %d %s (want 403 kill_switch)", r.StatusCode, b)
	}
	if d, _ := fs.Load(uidOf(t, e, "bob@example")); len(d.Items) != 0 {
		t.Errorf("B off: item stored (%d)", len(d.Items))
	}
	if r, _ := e.call(t, "alice@example", "GET", "/v1/memory", ""); r.StatusCode != 200 {
		t.Errorf("A GET /v1/memory %d", r.StatusCode)
	}
}
