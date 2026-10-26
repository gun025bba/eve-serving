package metrics

import "testing"

func f(v float64) *float64 { return &v }

func TestPct(t *testing.T) {
	if Pct(nil, 50) != nil {
		t.Fatal("nil")
	}
	xs := []float64{5, 1, 3, 2, 4}
	if *Pct(xs, 50) != 3 || *Pct(xs, 95) != 5 {
		t.Fatal(*Pct(xs, 50), *Pct(xs, 95))
	}
}

func TestWindowStats(t *testing.T) {
	w := NewWindow(3)
	w.Add(Sample{OK: true, Lat: f(1), Q: f(0), TPS: f(60)})
	w.Add(Sample{OK: false, Code: "queue_full"})
	w.Add(Sample{OK: true, Lat: f(3), Q: f(2), TPS: f(40), TTFT: f(0.3)})
	w.Add(Sample{OK: true, Lat: f(2), Q: f(1), TPS: f(50)})
	s := w.Stats()
	if s["window_requests"] != 3 || s["error_rate"] != 0.333 || s["tokens_per_s_avg"] != 45.0 {
		t.Fatal(s)
	}
	if s["errors"].(map[string]int)["queue_full"] != 1 {
		t.Fatal(s["errors"])
	}
	if *s["latency_s"].(map[string]any)["p95"].(*float64) != 3 {
		t.Fatal("p95")
	}
}

func TestRegistry(t *testing.T) {
	m := New(10)
	m.Reqs.WithLabelValues("/v1/chat/completions", "200").Inc()
	mfs, err := m.Reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "llm_requests_total" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing llm_requests_total")
	}
}
