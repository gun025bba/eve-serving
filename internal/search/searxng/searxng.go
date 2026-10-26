// Package searxng is the SearXNG JSON API provider (self-hosted on 127.0.0.1).
package searxng

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gun025bba/llm-serving/internal/search"
)

func init() { search.Register("searxng", New) }

type Provider struct {
	base    *url.URL
	client  *http.Client
	max     int
	snippet int
}

// New only accepts a loopback base URL: SearXNG must never be reached over
// the network, and the URL comes from config only (never from requests).
func New(cfg search.Config) (search.Provider, error) {
	raw := cfg.SearxngURL
	if raw == "" {
		raw = "http://127.0.0.1:8888"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("searxng url invalid")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("searxng url must be a loopback address (got host %q)", u.Hostname())
	}
	to := cfg.Timeout
	if to <= 0 {
		to = 6 * time.Second
	}
	max := cfg.MaxResults
	if max <= 0 {
		max = 5
	}
	sn := cfg.SnippetChars
	if sn <= 0 {
		sn = 300
	}
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: to}
	return &Provider{base: u, client: &http.Client{Transport: tr, Timeout: to, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, max: max, snippet: sn}, nil
}

func (p *Provider) Name() string { return "searxng" }

type apiResp struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		Engine        string `json:"engine"`
		PublishedDate string `json:"publishedDate"`
	} `json:"results"`
}

// Error kinds (llm_search_provider_errors_total{kind}).
type Error struct{ Kind, Msg string }

func (e *Error) Error() string { return e.Kind + ": " + e.Msg }

func (p *Provider) Search(ctx context.Context, q search.Query) ([]search.Result, error) {
	v := url.Values{"q": {q.Q}, "format": {"json"}}
	if q.Lang != "" && q.Lang != "auto" {
		v.Set("language", q.Lang)
	}
	switch q.TimeRange {
	case "day", "week", "month", "year":
		v.Set("time_range", q.TimeRange)
	}
	u := *p.base
	u.Path = strings.TrimRight(u.Path, "/") + "/search"
	u.RawQuery = v.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "Timeout") {
			return nil, &Error{"timeout", "request timed out"}
		}
		return nil, &Error{"http", "request failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return nil, &Error{"captcha", "rate limited by searxng limiter"}
	}
	if resp.StatusCode != 200 {
		return nil, &Error{"http", fmt.Sprintf("status %d", resp.StatusCode)}
	}
	var ar apiResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&ar); err != nil {
		return nil, &Error{"parse", "invalid json"}
	}
	max := q.Max
	if max <= 0 || max > 8 {
		max = p.max
	}
	seen := map[string]bool{}
	var out []search.Result
	for _, r := range ar.Results {
		pu, err := url.Parse(r.URL)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		res := search.Result{Title: clip(r.Title, 200), URL: r.URL, Snippet: clip(r.Content, p.snippet), Engine: r.Engine}
		if t, err := time.Parse(time.RFC3339, r.PublishedDate); err == nil {
			res.Published = &t
		} else if t, err := time.Parse("2006-01-02T15:04:05", r.PublishedDate); err == nil {
			res.Published = &t
		}
		out = append(out, res)
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
