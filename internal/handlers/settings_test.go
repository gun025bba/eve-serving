package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

func TestSettings_ServerOff(t *testing.T) {
	e, _ := newMemEnv(t, false, &failLedger{})
	r, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
	if r.StatusCode != 200 || strings.TrimSpace(string(b)) != `{"system":{"memory_available":false},"ui":{"memory_intro_seen":true}}` || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":true}}`); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestSettings_DefaultOffAndToggleGatesInjection(t *testing.T) {
	// 5.0.0: a user with no settings key reads as on (opt-out)
	if st := memstore.New(); true {
		srv := &Server{Store: st}
		u, err := srv.loadUserSettings(httptestReq("alice@example"))
		if err != nil || !u.memoryEnabled() || u.UI.MemoryIntroSeen {
			t.Fatal("missing key must read as memory on, intro unseen", err)
		}
	}
	e, fs := newMemEnv(t, true, &failLedger{})
	memory.Add(fs, uidOf(t, e, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false}}`)
	if r.StatusCode != 200 || strings.TrimSpace(string(b)) != `{"memory":{"enabled":false},"system":{"memory_available":true},"ui":{"memory_intro_seen":true}}` {
		t.Fatal(r.StatusCode, string(b))
	}
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(fmt.Sprint(e.lastUpstream()), "user_memory") {
		t.Fatal("injected while user setting is off")
	}
	e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":true}}`)
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(fmt.Sprint(e.lastUpstream()), "user_memory") {
		t.Fatal("not injected after enabling")
	}
}

func TestSettings_PatchStrict(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	for _, body := range []string{
		`{"system":{"memory_available":false}}`, `{"memory":{"enabled":true},"system":{}}`,
		`{"memory":{"enabled":null}}`, `{"memory":null}`, `{}`, `{"memory":{"enabled":"yes"}}`,
		`{"memory":{"enabled":true,"x":1}}`, `{"proactive":{"enabled":true}}`,
	} {
		if r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", body); r.StatusCode != 400 || strings.TrimSpace(string(b)) != `{"error_code":"invalid"}` {
			t.Fatal(body, r.StatusCode, string(b))
		}
	}
}

func TestSettings_UnknownKeysPreserved(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	st := srvOf(t, e).Store.(store.UserSettingsStore)
	st.MergeUserSettings(context.Background(), "alice@example", "future", []byte(`{"x":1}`))
	if r, _ := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false}}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	b, _ := st.UserSettings(context.Background(), "alice@example")
	if !strings.Contains(string(b), `"future":{"x":1}`) {
		t.Fatal(string(b))
	}
}

func httptestReq(login string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	return r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{User: login}))
}

func TestSettings_PatchBearer403(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	r, b := e.call(t, "", "PATCH", "/v1/settings", `{"memory":{"enabled":true}}`)
	if r.StatusCode != 403 || strings.TrimSpace(string(b)) != `{"error_code":"scope_denied"}` {
		t.Fatal(r.StatusCode, string(b))
	}
	// GET stays available to Bearer
	if r, _ := e.call(t, "", "GET", "/v1/settings", ""); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
}

// Security Low: memory is fixed off for a principal without the interactive
// scope, so GET /v1/settings must not report memory.enabled=true to an API key.
func TestSettings_GetBearerShowsMemoryOff(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	r, b := e.call(t, "alice@example", "GET", "/v1/settings", "")
	if r.StatusCode != 200 || !strings.Contains(string(b), `"memory":{"enabled":true}`) {
		t.Fatal("interactive view must show the stored value", r.StatusCode, string(b))
	}
	r, b = e.call(t, "", "GET", "/v1/settings", "")
	if r.StatusCode != 200 || !strings.Contains(string(b), `"memory":{"enabled":false}`) || strings.Contains(string(b), `"enabled":true`) {
		t.Fatal("API key must read memory.enabled=false", r.StatusCode, string(b))
	}
}
