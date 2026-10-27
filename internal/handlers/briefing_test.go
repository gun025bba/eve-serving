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

func briefingEnv(t *testing.T, on bool) (*webEnv, *fakePim, *[]map[string]any) {
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
		fmt.Fprint(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"오늘 일정 1건"}}]}`)
	}))
	t.Cleanup(up.Close)
	e.srv.Cfg.Upstream = up.URL
	e.srv.Up = proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second})
	e.srv.Cfg.Briefing.Enabled = on
	return e, fp, seen
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
	if r.StatusCode != 200 || !strings.Contains(string(b), `"text":"오늘 일정 1건"`) {
		t.Fatal(r.StatusCode, string(b))
	}
	if fp.calls != 2 || len(*seen) != 1 {
		t.Fatal(fp.calls, len(*seen))
	}
	if _, ok := (*seen)[0]["tools"]; ok {
		t.Fatal("tools offered to briefing model")
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
