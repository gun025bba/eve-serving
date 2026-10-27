package handlers

// QA (PR #43): PATCH /v1/settings is browser-session only; Bearer (API key) -> 403
// scope_denied and nothing changes; GET stays available to Bearer; browser session PATCH works.
import (
	"fmt"
	"strings"
	"testing"
)

func TestQA_SettingsPatchBearer403(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	if r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false}}`); r.StatusCode != 200 {
		t.Fatalf("browser session PATCH: %d %s", r.StatusCode, b)
	}
	for _, body := range []string{`{"memory":{"enabled":true}}`, `{"memory":{"enabled":false}}`, `{"system":{"memory_available":false}}`} {
		r, m := do(t, "PATCH", e.ts.URL+"/v1/settings", body, true)
		if r.StatusCode != 403 || !strings.Contains(fmt.Sprint(m), "scope_denied") {
			t.Errorf("Bearer PATCH %s: %d %v (want 403 scope_denied)", body, r.StatusCode, m)
		}
	}
	r, m := do(t, "GET", e.ts.URL+"/v1/settings", "", true)
	if r.StatusCode != 200 {
		t.Errorf("Bearer GET: %d %v", r.StatusCode, m)
	}
	_, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
	if !strings.Contains(string(b), `"enabled":false`) {
		t.Errorf("setting changed by Bearer attempt: %s", b)
	}
}
