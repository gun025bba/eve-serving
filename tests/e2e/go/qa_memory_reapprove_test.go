package handlers

// QA (#53 902297a): re-approving an already executed card is refused: 409 conflict,
// body {"error_code":"conflict"}, nothing written twice. Also after a reissue attempt.
import (
	"strings"
	"testing"
)

func TestQA_ReapproveExecuted409(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	setUserMemory(t, e, "alice@example", true)
	id, tok, ph := proposeAndToken(t, e, "")
	body := `{"confirm_token":"` + tok + `","payload_hash":"` + ph + `"}`
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", body); r.StatusCode != 200 {
		t.Fatal("first approve", r.StatusCode, string(b))
	}
	for i := 0; i < 2; i++ {
		r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", body)
		if r.StatusCode != 409 || strings.TrimSpace(string(b)) != `{"error_code":"conflict"}` {
			t.Errorf("re-approve %d: %d %s (want 409 {\"error_code\":\"conflict\"})", i, r.StatusCode, b)
		}
	}
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/reissue", ""); r.StatusCode == 200 {
		t.Errorf("reissue of executed card succeeded: %s", b)
	} else {
		t.Logf("reissue executed: %d %s", r.StatusCode, b)
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 1 {
		t.Errorf("items=%d, want 1 (no double write)", len(d.Items))
	}
}
