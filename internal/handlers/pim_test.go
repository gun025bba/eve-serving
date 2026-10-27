package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/store"
)

type fakePim struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakePim) ListEvents(context.Context, time.Time, time.Time) (pim.Events, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return pim.Events{Events: []pim.Event{{ID: "e1", Title: "치과 예약", Start: "s", End: "e", Calendar: "c"}}}, f.err
}

func (f *fakePim) ListReminders(context.Context, *time.Time, bool) (pim.Reminders, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return pim.Reminders{}, f.err
}

// pimEnv: web env whose upstream always calls calendar_list_events once (even
// when the tool is not offered), then answers.
func pimEnv(t *testing.T, enabled bool) (*webEnv, *fakePim) {
	e := newWebEnv(t, true)
	seen, mu := e.seen, e.mu
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		*seen = append(*seen, b)
		mu.Unlock()
		for _, m := range b["messages"].([]any) {
			if m.(map[string]any)["role"] == "tool" {
				fmt.Fprint(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
				return
			}
		}
		fmt.Fprint(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c0","type":"function","function":{"name":"calendar_list_events","arguments":"{\"from\":\"2026-10-28T00:00:00+09:00\",\"to\":\"2026-10-29T00:00:00+09:00\"}"}}]}}]}`)
	}))
	t.Cleanup(up.Close)
	e.srv.Cfg.Upstream = up.URL
	e.srv.Up = proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second})
	fp := &fakePim{}
	e.srv.Pim = fp
	e.srv.Cfg.Pim.Enabled = enabled
	res := e.st.(store.UserIDResolver)
	owner, err := res.UserID(t.Context(), "alice@example")
	if err != nil {
		t.Fatal(err)
	}
	res.UserID(t.Context(), "bob@example")
	e.srv.Cfg.Pim.OwnerUserID = owner
	return e, fp
}

func chatAutoAs(t *testing.T, e *webEnv, who string) {
	t.Helper()
	r, b := e.call(t, who, "POST", "/v1/chat/completions", `{"stream":true,"web_search":"auto","messages":[{"role":"user","content":"오늘 일정?"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
}

func offered(q map[string]any, name string) bool {
	b, _ := json.Marshal(q["tools"])
	return strings.Contains(string(b), `"`+name+`"`)
}

func TestPimTools_OwnerOnly(t *testing.T) {
	e, fp := pimEnv(t, true)
	chatAutoAs(t, e, "alice@example")
	q := e.requests()
	if !offered(q[0], pim.ToolListEvents) || !offered(q[0], pim.ToolListReminders) {
		t.Fatal("owner: pim tools not offered")
	}
	if res := lastToolResult(e); !strings.Contains(res, "치과 예약") || !strings.HasPrefix(res, "<untrusted_web_content") {
		t.Fatal(res)
	}
	if fp.calls != 1 {
		t.Fatal(fp.calls)
	}
	// bob (not the owner): tools hidden; a call that arrives anyway is denied
	// without reaching the helper.
	n := len(e.requests())
	chatAutoAs(t, e, "bob@example")
	q = e.requests()[n:]
	for _, x := range q {
		if offered(x, pim.ToolListEvents) || offered(x, pim.ToolListReminders) {
			t.Fatal("non-owner sees pim tools")
		}
	}
	if res := lastToolResult(e); res != pim.Denied() {
		t.Fatal(res)
	}
	if fp.calls != 1 {
		t.Fatal("helper called for non-owner")
	}
}

func TestPimTools_OffByDefault(t *testing.T) {
	e, fp := pimEnv(t, false)
	chatAutoAs(t, e, "alice@example")
	for _, x := range e.requests() {
		if offered(x, pim.ToolListEvents) {
			t.Fatal("offered while pim.enabled=false")
		}
	}
	if fp.calls != 0 || lastToolResult(e) != pim.Denied() {
		t.Fatal(fp.calls, lastToolResult(e))
	}
}

func TestPimTools_HelperDownCodeOnly(t *testing.T) {
	e, fp := pimEnv(t, true)
	var logs strings.Builder
	e.srv.Log = logging.New(&logs, logging.INFO)
	fp.err = &pim.Error{Code: pim.CodeUnavailable}
	chatAutoAs(t, e, "alice@example")
	if res := lastToolResult(e); res != `{"error_code":"pim_unavailable","status":"error"}` {
		t.Fatal(res)
	}
	if !strings.Contains(logs.String(), "pim_unavailable") || strings.Contains(logs.String(), "치과") {
		t.Fatal(logs.String())
	}
}
