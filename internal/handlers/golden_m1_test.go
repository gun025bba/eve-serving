package handlers

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden_m1 (off_* must stay byte-identical to the pre-6.0.0 baseline; review auto_* diffs)")

// Recorded conversations (off/auto x 5; the force cases were removed in 6.0.0). The upstream request bodies and
// the client-visible responses must be byte-identical to the pre-registry
// gateway (docs/tool-platform.md §8-4). Goldens were generated on v1.1.0 code.
var goldenCases = []struct{ name, user, body string }{
	{"off_1", "", `{"messages":[{"role":"user","content":"hi"}]}`},
	{"off_2", "", `{"stream":true,"messages":[{"role":"user","content":"hello"}]}`},
	{"off_3", "", `{"web_search":"off","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"x"}]}`},
	{"off_4", "", `{"web_search":false,"stream":true,"messages":[{"role":"user","content":"see https://go.example/rel"}]}`},
	{"off_5", "", `{"max_tokens":64,"temperature":0.2,"messages":[{"role":"user","content":"안녕"}]}`},
	{"auto_1", "", `{"web_search":"auto","messages":[{"role":"user","content":"go 1.27 나왔어?"}]}`},
	{"auto_2", "", `{"stream":true,"web_search":"auto","messages":[{"role":"user","content":"go 1.27 release"}]}`},
	{"auto_3", "", `{"web_search":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"news"}]}`},
	{"auto_4", "", `{"stream":true,"web_search":"auto","messages":[{"role":"user","content":"go 1.27 release: read https://two.example/ please"}]}`},
	{"auto_5", "", `{"web_search":"auto","messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"go 1.27 release q2"}]}`},
}

var volatileRe = regexp.MustCompile(`("(?:id|created|latency_ms|request_id|queue_ms|upstream_ms|duration_ms|elapsed_ms)":)("[^"]*"|-?[0-9.]+)`)

var dateRe = regexp.MustCompile(`Today is \d{4}-\d\d-\d\d \(\w+\)`)

func normalizeGolden(s string) string {
	return dateRe.ReplaceAllString(volatileRe.ReplaceAllString(s, `${1}"_"`), "Today is DATE")
}

func TestGolden_M1ByteIdentical(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			e := newWebEnv(t, true)
			// off_*: the operator rollback (web.auto_select_off) must give the
			// exact pre-6.0.0 pass-through. auto_*: auto selection (default);
			// the "web_search" field in those bodies is ignored.
			e.srv.Cfg.Web.AutoSelectOff = strings.HasPrefix(c.name, "off_")
			r, b := e.call(t, c.user, "POST", "/v1/chat/completions", c.body)
			var up []string
			for _, q := range e.requests() {
				j, _ := json.Marshal(q)
				up = append(up, string(j))
			}
			got := normalizeGolden("status: " + r.Status + "\n--- upstream\n" + strings.Join(up, "\n") + "\n--- response\n" + string(b) + "\n")
			p := filepath.Join("testdata", "golden_m1", c.name+".txt")
			if *updateGolden {
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("golden mismatch for %s\n--- got\n%s\n--- want\n%s", c.name, got, want)
			}
		})
	}
}
