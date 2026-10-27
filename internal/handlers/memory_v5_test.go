package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

// 5.0.0 (MAJOR): memory default on, intro gate, api-key fixed off.

func TestV5_ServerDefaultOn(t *testing.T) {
	c := config.Default()
	if !c.Memory.Enabled || c.Memory.AuditKeyFile == "" {
		t.Fatal("5.0.0: memory.enabled defaults to true with an audit key path")
	}
}

func TestV5_UserMissingKeyReadsOn_ExplicitFalseStaysOff(t *testing.T) {
	st := memstore.New()
	srv := &Server{Store: st}
	u, err := srv.loadUserSettings(httptestReq("carol@example"))
	if err != nil || !u.memoryEnabled() || u.UI.MemoryIntroSeen {
		t.Fatal("missing key: memory on, intro unseen", err)
	}
	st.MergeUserSettings(t.Context(), "carol@example", "memory", []byte(`{"enabled":false}`))
	if u, _ := srv.loadUserSettings(httptestReq("carol@example")); u.memoryEnabled() {
		t.Fatal("explicit false must stay off")
	}
}

func TestV5_IntroSeenPatch(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	e.st.(store.UserSettingsStore).MergeUserSettings(t.Context(), "alice@example", "ui", []byte(`{"memory_intro_seen":false}`))
	r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"ui":{"memory_intro_seen":true}}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"ui":{"memory_intro_seen":true}`) {
		t.Fatal(r.StatusCode, string(b))
	}
	for _, body := range []string{`{"ui":{"memory_intro_seen":null}}`, `{"ui":{"memory_intro_seen":"yes"}}`,
		`{"ui":{}}`, `{"ui":null}`, `{"ui":{"memory_intro_seen":true,"x":1}}`, `{"ui":{"theme":"dark"}}`} {
		if r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", body); r.StatusCode != 400 {
			t.Fatal(body, r.StatusCode, string(b))
		}
	}
	// combined patch in one request
	if r, b := e.call(t, "alice@example", "PATCH", "/v1/settings", `{"memory":{"enabled":false},"ui":{"memory_intro_seen":false}}`); r.StatusCode != 200 ||
		!strings.Contains(string(b), `"memory":{"enabled":false}`) || !strings.Contains(string(b), `"memory_intro_seen":false`) {
		t.Fatal(r.StatusCode, string(b))
	}
	// Bearer cannot patch settings
	if r, _ := do(t, "PATCH", e.ts.URL+"/v1/settings", `{"ui":{"memory_intro_seen":true}}`, true); r.StatusCode != 403 {
		t.Fatal("bearer patch", r.StatusCode)
	}
}

func TestV5_InjectionHeldUntilIntroSeen(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	memory.Add(fs, uidOf(t, e, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	e.st.(store.UserSettingsStore).MergeUserSettings(t.Context(), "alice@example", "ui", []byte(`{"memory_intro_seen":false}`))
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if up := fmt.Sprint(e.lastUpstream()); strings.Contains(up, "user_memory") || strings.Contains(up, memory.ToolName) {
		t.Fatal("injected or tool offered before intro seen")
	}
	e.call(t, "alice@example", "PATCH", "/v1/settings", `{"ui":{"memory_intro_seen":true}}`)
	e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(fmt.Sprint(e.lastUpstream()), "user_memory") {
		t.Fatal("not injected after intro seen")
	}
}

func TestV5_ProposalHeldUntilIntroSeen(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	e.st.(store.UserSettingsStore).MergeUserSettings(t.Context(), "alice@example", "ui", []byte(`{"memory_intro_seen":false}`))
	chatAuto(t, e)
	if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 {
		t.Fatalf("card created before intro seen: %d", len(acts))
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Fatal("stored")
	}
}

func TestV5_BearerMemoryRoutes403(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	for _, c := range [][2]string{{"GET", "/v1/memory"}, {"GET", "/v1/memory/export"}, {"DELETE", "/v1/memory"},
		{"POST", "/v1/memory/items"}, {"PATCH", "/v1/memory/items/x"}, {"DELETE", "/v1/memory/items/x"}} {
		r, m := do(t, c[0], e.ts.URL+c[1], `{"kind":"fact","text":"x"}`, true)
		if r.StatusCode != 403 || m["error_code"] != "scope_denied" {
			t.Fatal(c, r.StatusCode, m)
		}
	}
	if d, _ := fs.Load(uidOf(t, e, "api-key")); len(d.Items) != 0 {
		t.Fatal("api key stored memory")
	}
}

func TestV5_BearerChatNoInjection(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	do(t, "POST", e.ts.URL+"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, true)
	if up := fmt.Sprint(e.lastUpstream()); strings.Contains(up, "user_memory") || strings.Contains(up, memory.ToolName) {
		t.Fatal("api-key virtual user got memory")
	}
}
