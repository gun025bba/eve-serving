package handlers

import (
	"errors"
	"testing"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/config"
)

func webCap(t *testing.T, e *webEnv) any {
	_, b := e.call(t, "alice@example", "GET", "/v1/me", "")
	return js(t, b)["capabilities"].(map[string]any)["web_search"]
}

// Med 2: 3 audit write failures within 5 minutes disable the web tools.
func TestAuditBreaker_WriteFailuresDisableWebTools(t *testing.T) {
	e := newWebEnv(t, true)
	e.srv.AuditBreaker = audit.NewBreaker()
	e.audit.Fail = errors.New("db down")
	// one web chat writes call_started + call_done (+ more) audit rows -> >= 3 failures
	r, b := e.call(t, "", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"latest go"}],"web_search":"force"}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if !e.srv.AuditBreaker.Open() {
		t.Fatal("breaker must be open after repeated audit write failures")
	}
	if webCap(t, e) != false {
		t.Fatal("web tools must be reported disabled")
	}
	r, b = e.call(t, "", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"web_search":"force"}`)
	if r.StatusCode != 400 || errCode(js(t, b)) != "web_search_disabled" {
		t.Fatal(r.StatusCode, string(b))
	}
}

// Med 2: verify failures feed the same breaker.
func TestAuditBreaker_VerifyFailuresDisableWebTools(t *testing.T) {
	e := newWebEnv(t, true)
	e.srv.AuditBreaker = audit.NewBreaker()
	for i := 0; i < 2; i++ {
		e.srv.AuditBreaker.Fail()
	}
	if webCap(t, e) != true {
		t.Fatal("2 failures must keep web tools on")
	}
	e.srv.AuditBreaker.Fail()
	if webCap(t, e) != false {
		t.Fatal("3rd failure must disable web tools")
	}
}

// Med 2: in-memory audit ledger outside dev disables the web tools.
func TestMemoryLedger_DisablesWebToolsInProd(t *testing.T) {
	for _, tc := range []struct {
		env    string
		memory bool
		want   bool
	}{{"", true, true}, {"prod", true, true}, {"dev", true, false}, {"DEV", true, false}, {"", false, false}} {
		if got := MemoryLedgerBlocksWeb(config.Config{Env: tc.env}, tc.memory); got != tc.want {
			t.Errorf("env=%q memory=%v: got %v", tc.env, tc.memory, got)
		}
	}
	e := newWebEnv(t, true)
	e.srv.WebAuditBlocked = true
	if webCap(t, e) != false {
		t.Fatal("memory ledger in prod must disable web tools")
	}
}
