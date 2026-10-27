package handlers

// QA (#38 + #40 together): user memory off -> propose and approve are 403 kill_switch.
// Needs memToolEnv/proposeAndToken (PR #38) and settings (PR #40); compile on a tree with both.
import (
	"strings"
	"testing"
)

func setUserMemory(t *testing.T, e *webEnv, login string, on bool) {
	t.Helper()
	body := `{"memory":{"enabled":false}}`
	if on {
		body = `{"memory":{"enabled":true}}`
	}
	if r, b := e.call(t, login, "PATCH", "/v1/settings", body); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
}

func TestQA_KillSwitch_ProposeDenied(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	setUserMemory(t, e, "alice@example", false)
	chatAuto(t, e)
	acts, _ := e.st.ListPendingActions(t.Context(), "alice@example")
	res := lastToolResult(e)
	if len(acts) != 0 || !strings.Contains(res, "kill_switch") {
		t.Errorf("propose while off: res=%q cards=%d (want kill_switch, no card)", res, len(acts))
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Error("written while off")
	}
}

func TestQA_KillSwitch_ApproveDenied(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	setUserMemory(t, e, "alice@example", true)
	id, tok, ph := proposeAndToken(t, e, "")
	setUserMemory(t, e, "alice@example", false)
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`)
	if r.StatusCode != 403 || !strings.Contains(string(b), "kill_switch") {
		t.Errorf("approve while off: %d %s (want 403 kill_switch)", r.StatusCode, b)
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Error("written while off")
	}
}
