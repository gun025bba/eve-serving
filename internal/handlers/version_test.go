package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/queue"
)

func TestVersionExposed(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"1.1.0", "1.1.0"}, {"", "dev"}} {
		s := &Server{Cfg: testDefaultCfg(), Key: key, Log: logging.New(io.Discard, logging.INFO), Start: time.Now(), Version: tc.in,
			M: metrics.New(50), Mem: &metrics.MemSampler{Needle: "no-such-proc-xyz"}, Q: queue.New(1, 1, time.Second)}
		ts := httptest.NewServer(s.Handler())
		get := func(path string, authz bool) map[string]any {
			req, _ := http.NewRequest("GET", ts.URL+path, nil)
			if authz {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.StatusCode != 200 || r.Header.Get("X-Eve-Version") != tc.want {
				t.Fatalf("%s: status %d X-Eve-Version %q want %q", path, r.StatusCode, r.Header.Get("X-Eve-Version"), tc.want)
			}
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			return m
		}
		if m := get("/health", false); m["version"] != tc.want || m["status"] != "ok" {
			t.Fatalf("/health: %v", m)
		}
		if m := get("/v1/me", true); m["version"] != tc.want {
			t.Fatalf("/v1/me: %v", m)
		}
		// Header is set on error responses too.
		r, _ := http.Get(ts.URL + "/nope")
		if r.StatusCode != 404 || !strings.EqualFold(r.Header.Get("X-Eve-Version"), tc.want) {
			t.Fatalf("404 header %q", r.Header.Get("X-Eve-Version"))
		}
		r.Body.Close()
		ts.Close()
	}
}
