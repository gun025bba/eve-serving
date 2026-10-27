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
		s := &Server{Cfg: testDefault(), Key: key, Log: logging.New(io.Discard, logging.INFO), Start: time.Now(), Version: tc.in,
			M: metrics.New(50), Mem: &metrics.MemSampler{Needle: "no-such-proc-xyz"}, Q: queue.New(1, 1, time.Second)}
		ts := httptest.NewServer(s.Handler())
		// /health is unauthenticated: exactly {"status":"ok"}, no version anywhere.
		r, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || strings.TrimSpace(string(b)) != `{"status":"ok"}` || r.Header.Get("X-Eve-Version") != "" {
			t.Fatalf("/health: %d %s %q", r.StatusCode, b, r.Header.Get("X-Eve-Version"))
		}
		// Unauthenticated errors don't reveal the version either.
		r, _ = http.Get(ts.URL + "/v1/me")
		r.Body.Close()
		if r.StatusCode != 401 || r.Header.Get("X-Eve-Version") != "" {
			t.Fatalf("401 header %q", r.Header.Get("X-Eve-Version"))
		}
		req, _ := http.NewRequest("GET", ts.URL+"/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		r, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		r.Body.Close()
		if r.StatusCode != 200 || r.Header.Get("X-Eve-Version") != tc.want || m["version"] != tc.want {
			t.Fatalf("/v1/me: %d %q %v", r.StatusCode, r.Header.Get("X-Eve-Version"), m)
		}
		ts.Close()
	}
}
