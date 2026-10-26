package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	policydata "github.com/gun025bba/eve-serving/deploy/policy"
	"github.com/gun025bba/eve-serving/internal/policy/domains"
	"github.com/gun025bba/eve-serving/internal/search"
	"github.com/gun025bba/eve-serving/internal/webfetch"
)

func testPolicy() *domains.Policy {
	p, err := domains.Load(policydata.FS, time.Now())
	if err != nil {
		panic(err)
	}
	return p
}

type listSearch []search.Result

func (listSearch) Name() string { return "fake" }
func (l listSearch) Search(context.Context, search.Query) ([]search.Result, error) {
	return l, nil
}

type blockEv struct {
	reason, rule, host string
	hop                int
}

// §13: blocked results never reach the model, only the count does.
func TestSearch_FiltersBlockedDomains_CountOnly(t *testing.T) {
	var evs []blockEv
	s := NewSession(Deps{Domains: testPolicy(), Search: listSearch{
		{Title: "ok", URL: "https://news.example.com/a"},
		{Title: "short", URL: "https://bit.ly/abc"},
		{Title: "ip", URL: "http://2130706433/"},
		{Title: "pub ip", URL: "https://8.8.8.8/"},
		{Title: "tunnel", URL: "https://x1.ngrok-free.app/"},
	}, OnDomainBlock: func(_ Call, r, rule, h string, hop int) { evs = append(evs, blockEv{r, rule, h, hop}) }}, "u", nil)
	out, c := s.Exec(context.Background(), 1, "1", "web_search", `{"query":"q"}`)
	if c.Status != "ok" || c.ResultCount != 1 || c.Blocked != 4 {
		t.Fatalf("%+v", c)
	}
	for _, leak := range []string{"bit.ly", "2130706433", "8.8.8.8", "ngrok"} {
		if strings.Contains(out, leak) {
			t.Errorf("model output leaks blocked host %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"blocked":4`) {
		t.Errorf("missing blocked count: %s", out)
	}
	if len(s.Sources) != 1 || s.Allowed("https://bit.ly/abc") {
		t.Error("blocked result must not become a source or fetchable")
	}
	if len(evs) != 4 {
		t.Fatalf("events %+v", evs)
	}
	for _, e := range evs {
		if e.reason == "" || e.hop != 0 {
			t.Errorf("event %+v", e)
		}
	}
}

func TestSearch_NoBlockedKeyWhenNothingBlocked(t *testing.T) {
	s := NewSession(Deps{Domains: testPolicy(), Search: listSearch{{Title: "ok", URL: "https://news.example.com/a"}}}, "u", nil)
	out, _ := s.Exec(context.Background(), 1, "1", "web_search", `{"query":"q"}`)
	if strings.Contains(out, "blocked") {
		t.Fatal(out)
	}
}

// fail closed when the policy is not loaded
func TestWebTools_PolicyUnavailableFailsClosed(t *testing.T) {
	fs, ff := &fakeSearch{}, &fakeFetch{}
	s := NewSession(Deps{Search: fs, Fetch: ff}, "u", []string{"https://user.example.com/"})
	for _, call := range [][2]string{{"web_search", `{"query":"q"}`}, {"web_fetch", `{"url":"https://user.example.com/"}`}} {
		_, c := s.Exec(context.Background(), 1, "1", call[0], call[1])
		if c.Status != "blocked" || c.BlockReason != domains.ReasonPolicyUnloaded {
			t.Errorf("%s: %+v", call[0], c)
		}
	}
	if fs.n != 0 || len(ff.urls) != 0 {
		t.Fatal("no upstream call without a policy")
	}
}

// a user-supplied shortener URL is blocked even though it is allowlisted
func TestFetch_DeniedEvenIfUserSupplied(t *testing.T) {
	ff := &fakeFetch{}
	var evs []blockEv
	s := NewSession(Deps{Domains: testPolicy(), Fetch: ff, OnDomainBlock: func(_ Call, r, rule, h string, hop int) { evs = append(evs, blockEv{r, rule, h, hop}) }},
		"u", []string{"see https://bit.ly/x and http://0x7f.1/"})
	for _, u := range []string{"https://bit.ly/x", "http://0x7f.1/"} {
		out, c := s.Exec(context.Background(), 1, "1", "web_fetch", `{"url":"`+u+`"}`)
		if c.Status != "blocked" || c.BlockReason == "" {
			t.Fatalf("%s: %+v", u, c)
		}
		if strings.Contains(out, "bit.ly") && !strings.Contains(c.Args["url"].(string), "bit.ly") {
			t.Fatal(out)
		}
	}
	if len(ff.urls) != 0 || len(evs) != 2 {
		t.Fatalf("fetched=%v events=%+v", ff.urls, evs)
	}
}

// redirect-hop blocks from webfetch become domain_blocked events with the hop
type hopFetch struct{}

func (hopFetch) Fetch(context.Context, string, int) (*webfetch.Result, error) {
	return nil, &url.Error{Op: "Get", URL: "x", Err: &webfetch.BlockedError{Reason: domains.ReasonDomainDenied, Rule: "shortener:1", Host: "bit.ly", Hop: 2}}
}

func TestFetch_RedirectHopBlockAudited(t *testing.T) {
	var evs []blockEv
	s := NewSession(Deps{Domains: testPolicy(), Fetch: hopFetch{}, OnDomainBlock: func(_ Call, r, rule, h string, hop int) { evs = append(evs, blockEv{r, rule, h, hop}) }},
		"u", []string{"https://news.example.com/"})
	out, c := s.Exec(context.Background(), 1, "1", "web_fetch", `{"url":"https://news.example.com/"}`)
	if c.BlockReason != domains.ReasonDomainDenied || len(evs) != 1 || evs[0].hop != 2 || evs[0].rule != "shortener:1" {
		t.Fatalf("%+v %+v", c, evs)
	}
	if strings.Contains(out, "bit.ly") {
		t.Fatal("redirect target leaked to model")
	}
}

func TestHostPolicy_Adapter(t *testing.T) {
	hp := HostPolicy(testPolicy())
	u, _ := url.Parse("https://is.gd/x")
	if be := hp(u, 1); be == nil || be.Hop != 1 || be.Rule == "" {
		t.Fatalf("%+v", be)
	}
	u, _ = url.Parse("https://news.example.com/")
	if be := hp(u, 0); be != nil {
		t.Fatal(be)
	}
	if be := HostPolicy(nil)(u, 0); be == nil {
		t.Fatal("nil policy must block")
	}
}

// every blocked path carries a constant, non-empty block reason
func TestBlockedAlwaysHasReason(t *testing.T) {
	s := NewSession(Deps{Domains: testPolicy(), Search: &fakeSearch{}, Fetch: &fakeFetch{},
		Rate:   NewRateLimiter(map[string]int{"search": 2, "fetch": 9}),
		Limits: Limits{MaxCalls: 9, MaxSearch: 3, MaxFetch: 1}}, "u", []string{"https://a.example.com/x https://user.example.com/blocked"})
	calls := [][2]string{
		{"web_search", `{"query":"sk-ant-abcdefghijklmnopqrstuvwxyz0123456789"}`},
		{"web_search", `{"query":"a"}`}, {"web_search", `{"query":"b"}`}, {"web_search", `{"query":"c"}`},
		{"web_fetch", `{"url":"https://nope.example.com/"}`},
		{"web_fetch", `{"url":"https://user.example.com/blocked"}`},
		{"web_fetch", `{"url":"https://a.example.com/x"}`},
		{"web_fetch", `{"url":"https://bit.ly/z"}`},
		{"web_search", `{"query":"d"}`}, {"web_search", `{"query":"e"}`},
	}
	nb := 0
	for _, cl := range calls {
		_, c := s.Exec(context.Background(), 1, "1", cl[0], cl[1])
		if c.Status == "blocked" {
			nb++
			if c.BlockReason == "" {
				t.Errorf("%s %s: blocked without reason: %+v", cl[0], cl[1], c)
			}
		}
	}
	if nb < 5 {
		t.Fatalf("expected several blocked paths, got %d", nb)
	}
}

// Low follow-up: the breaker is re-checked on every call, so a running loop stops.
func TestSession_DisabledRecheckedEveryCall(t *testing.T) {
	fs := &fakeSearch{}
	off := false
	s := NewSession(Deps{Domains: testPolicy(), Search: fs, Disabled: func() bool { return off }}, "u", nil)
	if _, c := s.Exec(context.Background(), 1, "1", "web_search", `{"query":"q"}`); c.Status == "blocked" {
		t.Fatalf("first call must run: %+v", c)
	}
	n := fs.n
	off = true // breaker opens mid-loop
	for i, call := range [][2]string{{"web_search", `{"query":"q2"}`}, {"web_fetch", `{"url":"https://news.example.com/a"}`}} {
		_, c := s.Exec(context.Background(), 2, fmt.Sprint(i+2), call[0], call[1])
		if c.Status != "blocked" || c.BlockReason != ReasonToolsDisabled {
			t.Errorf("%s: %+v", call[0], c)
		}
	}
	if fs.n != n {
		t.Fatal("no upstream call after the breaker opened")
	}
}
