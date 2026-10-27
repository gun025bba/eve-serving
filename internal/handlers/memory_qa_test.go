package handlers

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

// uidFailStore: users.id lookup fails (DB down, etc.).
type uidFailStore struct{ *memstore.Store }

func (uidFailStore) UserID(context.Context, string) (int64, error) { return 0, errors.New("db down") }

// QA gap (a): a users.id lookup failure blocks every memory access
// (fail-closed 500), creates no file, injects nothing, and logs
// memory_user_id_failed.
func TestMemoryQA_UserIDFailureBlocksAccess(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	var logs bytes.Buffer
	srvOf(t, e).Log = logging.New(&logs, logging.INFO)
	srvOf(t, e).Store = uidFailStore{e.st.(*memstore.Store)}
	for _, c := range []struct{ m, p, b string }{
		{"GET", "/v1/memory", ""}, {"GET", "/v1/memory/export", ""},
		{"POST", "/v1/memory/items", `{"kind":"fact","text":"고양이"}`},
		{"PATCH", "/v1/memory/items/01J00000000000000000000000", `{"pinned":true}`},
		{"DELETE", "/v1/memory/items/01J00000000000000000000000", ""},
		{"DELETE", "/v1/memory", ""},
	} {
		r, b := e.call(t, "alice@example", c.m, c.p, c.b)
		if r.StatusCode < 400 || r.StatusCode == 404 && c.m == "GET" {
			t.Fatalf("%s %s: %d %s", c.m, c.p, r.StatusCode, b)
		}
		if strings.Contains(string(b), "고양이") {
			t.Fatal("echoed text")
		}
	}
	if ents, _ := os.ReadDir(fs.Dir()); len(ents) != 0 {
		t.Fatalf("files created: %d", len(ents))
	}
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"hi"}]}`)
	if c := e.lastUpstream()["messages"].([]any)[0].(map[string]any)["content"]; c != "S" {
		t.Fatalf("injected despite uid failure: %v", c)
	}
	if !strings.Contains(logs.String(), "memory_user_id_failed") {
		t.Fatal("memory_user_id_failed not logged")
	}
}

// QA gap (b): edit, delete and delete-all audit rows carry no raw text.
func TestMemoryQA_AuditEditDeleteNoPlaintext(t *testing.T) {
	led := &recLedger{}
	e, _ := newMemEnv(t, true, led)
	_, b := e.call(t, "alice@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"고양이를 키운다"}`)
	id := js(t, b)["id"].(string)
	if r, b := e.call(t, "alice@example", "PATCH", "/v1/memory/items/"+id, `{"text":"강아지를 키운다"}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := e.call(t, "alice@example", "DELETE", "/v1/memory/items/"+id, ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	e.call(t, "alice@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"커피는 디카페인"}`)
	if r, _ := e.call(t, "alice@example", "DELETE", "/v1/memory", ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	seen := map[string]bool{}
	for _, ev := range led.ev {
		seen[ev.Event] = true
		c := string(audit.Canonical(ev))
		for _, w := range []string{"고양이", "강아지", "디카페인", "키운다"} {
			if strings.Contains(c, w) {
				t.Fatalf("%s: plaintext %q in audit", ev.Event, w)
			}
		}
		if ev.Event == "memory_edit" && !strings.HasPrefix(ev.PayloadSHA256, "hmac-sha256:") {
			t.Fatal("edit without hmac")
		}
	}
	for _, want := range []string{"memory_edit", "memory_delete", "memory_delete_all"} {
		if !seen[want] {
			t.Fatalf("missing audit event %s (got %v)", want, seen)
		}
	}
}

// QA gap (c): web auto mode with several tool_call rounds (search, then
// fetch, then answer) keeps the memory block last in every upstream round.
func TestMemoryQA_WebMultiRoundBlockLast(t *testing.T) {
	e := newWebEnv(t, true)
	fs, err := memory.Open(t.TempDir() + "/memory")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Memory, e.srv.MemoryAuditKey = fs, []byte(strings.Repeat("k", 32))
	e.srv.Cfg.Memory.Enabled = true
	memory.Add(fs, uidOf(t, &e.env, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions",
		`{"web_search":"auto","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"go 1.27 나왔어?"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	reqs := e.requests()
	if len(reqs) < 3 {
		t.Fatalf("want >=3 upstream rounds (2 tool rounds + answer), got %d", len(reqs))
	}
	toolMsgs := 0
	for i, req := range reqs {
		assertBlockLast(t, req, "web-multi/round"+string(rune('0'+i)))
		for _, m := range req["messages"].([]any) {
			if m.(map[string]any)["role"] == "tool" {
				toolMsgs++
			}
		}
	}
	if toolMsgs == 0 {
		t.Fatal("no tool results reached upstream; rounds not exercised")
	}
}

// QA gap (d): export and delete-all only ever touch the caller's file.
func TestMemoryQA_ExportDeleteAllPerUser(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	e.call(t, "alice@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"앨리스 고양이"}`)
	e.call(t, "bob@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"밥 강아지"}`)
	_, b := e.call(t, "alice@example", "GET", "/v1/memory/export", "")
	if !strings.Contains(string(b), "앨리스") || strings.Contains(string(b), "밥 강아지") {
		t.Fatalf("alice export: %s", b)
	}
	_, b = e.call(t, "bob@example", "GET", "/v1/memory/export", "")
	if strings.Contains(string(b), "앨리스") || !strings.Contains(string(b), "밥 강아지") {
		t.Fatalf("bob export: %s", b)
	}
	if r, _ := e.call(t, "alice@example", "DELETE", "/v1/memory", ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	d, err := fs.Load(uidOf(t, e, "bob@example"))
	if err != nil || len(d.Items) != 1 || d.Items[0].Text != "밥 강아지" {
		t.Fatalf("bob affected by alice delete-all: %v %+v", err, d.Items)
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/memory", "")
	if strings.Contains(string(b), "앨리스") {
		t.Fatal("alice not deleted")
	}
}
