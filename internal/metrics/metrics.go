// Package metrics holds Prometheus metrics (same names as the Python gateway),
// the recent-request window behind /stats, and memory sampling.
package metrics

import (
	"math"
	"sort"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var latBuckets = []float64{.05, .1, .25, .5, 1, 2, 5, 10, 20, 30, 60, 120, 300}

type Metrics struct {
	Reg      *prometheus.Registry
	Reqs     *prometheus.CounterVec
	Errs     *prometheus.CounterVec
	Auth     *prometheus.CounterVec
	Lat      *prometheus.HistogramVec
	QWait    prometheus.Histogram
	TTFT     prometheus.Histogram
	TPS      prometheus.Histogram
	PTok     prometheus.Counter
	CTok     prometheus.Counter
	Inflight prometheus.Gauge
	QDepth   prometheus.Gauge
	MemSys   *prometheus.GaugeVec
	MemMLX   prometheus.Gauge
	Recent   *Window

	// Web tools. Labels never carry domains, URLs or queries.
	ToolCalls     *prometheus.CounterVec   // tool, provider, status
	ToolLat       *prometheus.HistogramVec // tool, provider
	SearchResults *prometheus.HistogramVec // provider
	SearchErrs    *prometheus.CounterVec   // provider, kind
	FetchBytes    prometheus.Histogram
	FetchBlocked  *prometheus.CounterVec // reason
	ToolRounds    prometheus.Histogram
	ToolParseFail prometheus.Counter
	WebPurged     prometheus.Counter
}

func New(window int) *Metrics {
	m := &Metrics{
		Reg:      prometheus.NewRegistry(),
		Reqs:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_requests_total", Help: "Requests by endpoint/status"}, []string{"endpoint", "status"}),
		Errs:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_errors_total", Help: "Errors by code"}, []string{"code"}),
		Auth:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_auth_total", Help: "Auth decisions by provider/result"}, []string{"auth_method", "result"}),
		Lat:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "llm_request_latency_seconds", Help: "Total request latency", Buckets: latBuckets}, []string{"stream"}),
		QWait:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_queue_wait_seconds", Help: "Time waiting for a slot", Buckets: latBuckets}),
		TTFT:     prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_time_to_first_token_seconds", Help: "Streaming time to first token", Buckets: latBuckets}),
		TPS:      prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_tokens_per_second", Help: "Completion tokens/sec", Buckets: []float64{5, 10, 20, 30, 40, 50, 60, 70, 80, 100, 150}}),
		PTok:     prometheus.NewCounter(prometheus.CounterOpts{Name: "llm_prompt_tokens_total", Help: "Prompt tokens"}),
		CTok:     prometheus.NewCounter(prometheus.CounterOpts{Name: "llm_completion_tokens_total", Help: "Completion tokens"}),
		Inflight: prometheus.NewGauge(prometheus.GaugeOpts{Name: "llm_inflight_requests", Help: "Requests being generated"}),
		QDepth:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "llm_queue_depth", Help: "Requests waiting"}),
		MemSys:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llm_system_memory_bytes", Help: "System memory"}, []string{"kind"}),
		MemMLX:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "llm_mlx_server_rss_bytes", Help: "mlx_lm.server RSS"}),
		Recent:   NewWindow(window),

		ToolCalls:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_tool_calls_total", Help: "Web tool calls"}, []string{"tool", "provider", "status"}),
		ToolLat:       prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "llm_tool_latency_seconds", Help: "Web tool latency", Buckets: latBuckets}, []string{"tool", "provider"}),
		SearchResults: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "llm_search_results", Help: "Results per search", Buckets: []float64{0, 1, 2, 3, 5, 8}}, []string{"provider"}),
		SearchErrs:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_search_provider_errors_total", Help: "Search provider errors"}, []string{"provider", "kind"}),
		FetchBytes:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_fetch_bytes", Help: "Fetched body bytes (decompressed)", Buckets: prometheus.ExponentialBuckets(1024, 4, 8)}),
		FetchBlocked:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llm_fetch_blocked_total", Help: "Fetches blocked by policy"}, []string{"reason"}),
		ToolRounds:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llm_tool_rounds", Help: "Tool rounds per request", Buckets: []float64{0, 1, 2, 3, 4}}),
		ToolParseFail: prometheus.NewCounter(prometheus.CounterOpts{Name: "llm_tool_parse_failures_total", Help: "Unparsable tool calls"}),
		WebPurged:     prometheus.NewCounter(prometheus.CounterOpts{Name: "llm_web_history_purged_total", Help: "tool_calls/web_sources rows deleted by retention"}),
	}
	m.Reg.MustRegister(m.Reqs, m.Errs, m.Auth, m.Lat, m.QWait, m.TTFT, m.TPS, m.PTok, m.CTok, m.Inflight, m.QDepth, m.MemSys, m.MemMLX,
		m.ToolCalls, m.ToolLat, m.SearchResults, m.SearchErrs, m.FetchBytes, m.FetchBlocked, m.ToolRounds, m.ToolParseFail, m.WebPurged,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Sample is one finished request in the /stats window.
type Sample struct {
	OK   bool
	Code string
	Lat  *float64
	Q    *float64
	TPS  *float64
	TTFT *float64
}

type Window struct {
	mu  sync.Mutex
	buf []Sample
	max int
}

func NewWindow(n int) *Window {
	if n < 1 {
		n = 1
	}
	return &Window{max: n}
}

func (w *Window) Add(s Sample) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, s)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
}

func (w *Window) Snapshot() []Sample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Sample(nil), w.buf...)
}

// Pct matches the Python implementation: nearest-rank on sorted values, rounded to 3 decimals.
func Pct(xs []float64, p float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.RoundToEven(p / 100 * float64(len(s)-1)))
	if i > len(s)-1 {
		i = len(s) - 1
	}
	v := Round(s[i], 3)
	return &v
}

func Round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

// Stats builds the /stats latency/queue/ttft/tps/error summary.
func (w *Window) Stats() map[string]any {
	r := w.Snapshot()
	var lat, tps, ttft, qs []float64
	errs := map[string]int{}
	nerr := 0
	for _, x := range r {
		if x.Lat != nil && *x.Lat != 0 && x.OK {
			lat = append(lat, *x.Lat)
		}
		if x.TPS != nil && *x.TPS != 0 {
			tps = append(tps, *x.TPS)
		}
		if x.TTFT != nil {
			ttft = append(ttft, *x.TTFT)
		}
		if x.Q != nil {
			qs = append(qs, *x.Q)
		}
		if !x.OK {
			errs[x.Code]++
			nerr++
		}
	}
	out := map[string]any{"window_requests": len(r), "errors": errs,
		"latency_s":        map[string]any{"p50": Pct(lat, 50), "p95": Pct(lat, 95)},
		"ttft_s":           map[string]any{"p50": Pct(ttft, 50), "p95": Pct(ttft, 95)},
		"error_rate":       0.0,
		"tokens_per_s_avg": nil,
	}
	if len(r) > 0 {
		out["error_rate"] = Round(float64(nerr)/float64(len(r)), 3)
	}
	var qmax any
	if len(qs) > 0 {
		m := qs[0]
		for _, v := range qs {
			m = math.Max(m, v)
		}
		qmax = Round(m, 3)
	}
	out["queue_wait_s"] = map[string]any{"p50": Pct(qs, 50), "p95": Pct(qs, 95), "max": qmax}
	if len(tps) > 0 {
		s := 0.0
		for _, v := range tps {
			s += v
		}
		out["tokens_per_s_avg"] = Round(s/float64(len(tps)), 1)
	}
	return out
}
