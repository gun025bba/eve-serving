package handlers

// QA (#46 5f7f2bd): a propose call with sensitive text maps to sensitive_category via
// rejectCode: tool result {"status":"denied","error_code":"sensitive_category"}, no "error"
// key, no card, nothing stored. Needs memToolEnv/setUserMemory (#38 + qa killswitch file).
import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQA_ProposeSensitiveCategory(t *testing.T) {
	for _, text := range []string{"동생은 우울증 약을 먹어", "지난 선거에 2번 찍었어"} {
		e, fs := memToolEnv(t, true, `{"op":"add","text":"`+text+`"}`, false)
		setUserMemory(t, e, "alice@example", true)
		chatAuto(t, e)
		res := lastToolResult(e)
		var tr map[string]any
		if i, j := strings.Index(res, "{"), strings.LastIndex(res, "}"); i >= 0 && j > i {
			_ = json.Unmarshal([]byte(res[i:j+1]), &tr)
		}
		if tr["status"] != "denied" || tr["error_code"] != "sensitive_category" {
			t.Errorf("%q: tool result %q, want denied/sensitive_category", text, res)
		}
		if _, ok := tr["error"]; ok {
			t.Errorf("%q: legacy 'error' key: %v", text, tr)
		}
		if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 {
			t.Errorf("%q: card created", text)
		}
		if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
			t.Errorf("%q: stored", text)
		}
	}
}
