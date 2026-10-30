package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/proxy"
)

// briefingReply is what the fake model answers (guarded: the server reads it).
type briefingReply struct {
	mu      sync.Mutex
	content string
	finish  string
}

func (b *briefingReply) set(content, finish string) {
	b.mu.Lock()
	b.content, b.finish = content, finish
	b.mu.Unlock()
}

func (b *briefingReply) json() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, _ := json.Marshal(b.content)
	f := ""
	if b.finish != "" {
		f = `,"finish_reason":"` + b.finish + `"`
	}
	return `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":` + string(c) + `}` + f + `}]}`
}

func briefingEnv(t *testing.T, on bool) (*webEnv, *fakePim, *[]map[string]any) {
	e, fp, seen, _ := briefingEnvReply(t, on)
	return e, fp, seen
}

func briefingEnvReply(t *testing.T, on bool) (*webEnv, *fakePim, *[]map[string]any, *briefingReply) {
	rep := &briefingReply{content: "오늘 2026-10-28(수) 브리핑\n일정 1건\n- 확인 불가 치과 예약\n리마인더 0건\n없음", finish: "stop"}
	e, fp := pimEnv(t, true)
	var mu sync.Mutex
	seen := &[]map[string]any{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		*seen = append(*seen, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rep.json())
	}))
	t.Cleanup(up.Close)
	e.srv.Cfg.Upstream = up.URL
	e.srv.Up = proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second})
	e.srv.Cfg.Briefing.Enabled = on
	return e, fp, seen, rep
}

func TestBriefingPreview_OffIs404(t *testing.T) {
	e, fp, seen := briefingEnv(t, false)
	r, _ := e.call(t, "alice@example", "POST", "/v1/briefing/preview", `{}`)
	if r.StatusCode != 404 || fp.calls != 0 || len(*seen) != 0 {
		t.Fatal(r.StatusCode)
	}
	// settings namespace is unknown while the server switch is off
	r, _ = e.call(t, "alice@example", "PATCH", "/v1/settings", `{"briefing":{"enabled":true}}`)
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestBriefingPreview_NoToolsNoSave(t *testing.T) {
	e, fp, seen := briefingEnv(t, true)
	r, b := e.call(t, "alice@example", "POST", "/v1/briefing/preview", `{}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"text":"오늘 2026-10-28(수) 브리핑\n일정 1건\n- 확인 불가 치과 예약`) {
		t.Fatal(r.StatusCode, string(b))
	}
	if fp.calls != 2 || len(*seen) != 1 {
		t.Fatal(fp.calls, len(*seen))
	}
	if _, ok := (*seen)[0]["tools"]; ok {
		t.Fatal("tools offered to briefing model")
	}
	// bounded, non-thinking call (model review must-fix #5)
	if mt, _ := (*seen)[0]["max_tokens"].(float64); mt != 2048 {
		t.Fatal("max_tokens", (*seen)[0]["max_tokens"])
	}
	if tp, _ := (*seen)[0]["temperature"].(float64); tp != 0.2 {
		t.Fatal("temperature", (*seen)[0]["temperature"])
	}
	ms := (*seen)[0]["messages"].([]any)
	if u := ms[len(ms)-1].(map[string]any)["content"].(string); !strings.HasSuffix(u, "/no_think") {
		t.Fatal("user message must end with /no_think:", u)
	}
	// non-owner: no pim data is read, but a preview still works
	before := fp.calls
	r, b = e.call(t, "bob@example", "POST", "/v1/briefing/preview", `{}`)
	if r.StatusCode != 200 || fp.calls != before || strings.Contains(string(b), "events") {
		t.Fatal(r.StatusCode, string(b))
	}
}

func TestBriefingSettings(t *testing.T) {
	e, _, _ := briefingEnv(t, true)
	r, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
	if r.StatusCode != 200 || !strings.Contains(string(b), `"briefing":{"enabled":false,"time":"07:30","tz":"","skip_if_empty":true,"delivery":"inapp","retention_days":30}`) {
		t.Fatal(string(b))
	}
	r, b = e.call(t, "alice@example", "PATCH", "/v1/settings", `{"briefing":{"enabled":true,"time":"06:45","tz":"Asia/Seoul"}}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"enabled":true,"time":"06:45","tz":"Asia/Seoul"`) {
		t.Fatal(r.StatusCode, string(b))
	}
	for _, bad := range []string{
		`{"briefing":{"delivery":"webpush"}}`,
		`{"briefing":{"delivery":"email"}}`,
		`{"briefing":{"time":"25:00"}}`,
		`{"briefing":{"tz":"Nowhere/X"}}`,
		`{"briefing":{"nope":1}}`,
		`{"briefing":{}}`,
		`{"proactive":{"enabled":true}}`,
		`{"pim":{}}`,
	} {
		if r, _ := e.call(t, "alice@example", "PATCH", "/v1/settings", bad); r.StatusCode != 400 {
			t.Fatal(bad, r.StatusCode)
		}
	}
	r, b = e.call(t, "alice@example", "PATCH", "/v1/settings", `{"pim":{"enabled":false}}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"pim":{"enabled":false}`) {
		t.Fatal(r.StatusCode, string(b))
	}
	// pim.enabled=false removes the tools for the owner too
	if n := e.srv.pimToolsFor(t.Context(), "alice@example"); len(n) != 0 {
		t.Fatal(n)
	}
}

// finish_reason=length must not be stored/returned as a briefing (must-fix #5).
func TestBriefingPreview_LengthIsFailure(t *testing.T) {
	e, _, _, rep := briefingEnvReply(t, true)
	rep.set("오늘 2026-10-28(수) 브리핑\n일정 1건\n- 치과", "length")
	r, b := e.call(t, "alice@example", "POST", "/v1/briefing/preview", `{}`)
	if r.StatusCode != 502 || strings.Contains(string(b), "치과") || !strings.Contains(string(b), `"status":"error"`) {
		t.Fatal(r.StatusCode, string(b))
	}
}

// A <think> block that leaks into content is removed; an unfinished one fails.
func TestBriefingPreview_ThinkStripped(t *testing.T) {
	e, _, _, rep := briefingEnvReply(t, true)
	rep.set("<think>일정을 정리하자</think>오늘 2026-10-28(수) 브리핑\n일정 1건\n- 확인 불가 치과 예약\n리마인더 0건\n없음", "stop")
	r, b := e.call(t, "alice@example", "POST", "/v1/briefing/preview", `{}`)
	if r.StatusCode != 200 || strings.Contains(string(b), "think") || !strings.Contains(string(b), "치과 예약") {
		t.Fatal(r.StatusCode, string(b))
	}
	rep.set("<think>일정을 정리하자", "stop")
	if r, b := e.call(t, "alice@example", "POST", "/v1/briefing/preview", `{}`); r.StatusCode != 502 || strings.Contains(string(b), "정리하자") {
		t.Fatal(r.StatusCode, string(b))
	}
}
