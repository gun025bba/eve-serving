package search_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gun025bba/eve-serving/internal/search"
	_ "github.com/gun025bba/eve-serving/internal/search/brave"
	_ "github.com/gun025bba/eve-serving/internal/search/searxng"
)

type fake struct {
	name string
	err  error
	n    atomic.Int32
}

func (f *fake) Name() string { return f.name }
func (f *fake) Search(context.Context, search.Query) ([]search.Result, error) {
	f.n.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return []search.Result{{Title: f.name, URL: "https://example.com/" + f.name}}, nil
}

func TestRegistryFallbackAndCache(t *testing.T) {
	bad, good := &fake{name: "bad", err: errors.New("down")}, &fake{name: "good"}
	search.Register("t-bad", func(search.Config) (search.Provider, error) { return bad, nil })
	search.Register("t-good", func(search.Config) (search.Provider, error) { return good, nil })
	p, err := search.New(search.Config{Providers: []string{"t-bad", "t-good"}})
	if err != nil {
		t.Fatal(err)
	}
	var errs int
	p.(*search.Chain).OnError = func(string, error) { errs++ }
	for i := 0; i < 2; i++ {
		r, err := p.Search(context.Background(), search.Query{Q: "x"})
		if err != nil || r[0].Title != "good" {
			t.Fatal(r, err)
		}
	}
	if good.n.Load() != 1 || bad.n.Load() != 1 || errs != 1 {
		t.Fatalf("cache/fallback: good=%d bad=%d errs=%d", good.n.Load(), bad.n.Load(), errs)
	}
	if p.Name() != "good" {
		t.Fatal(p.Name())
	}
}

func TestUnknownAndBraveDisabled(t *testing.T) {
	if _, err := search.New(search.Config{Providers: []string{"nope"}}); err == nil {
		t.Fatal("unknown provider must fail")
	}
	if _, err := search.New(search.Config{Providers: []string{"brave"}}); err == nil {
		t.Fatal("brave alone (no key) must leave no provider")
	}
	if _, err := search.New(search.Config{Providers: []string{"brave", "searxng"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSearxngMustBeLoopback(t *testing.T) {
	for _, u := range []string{"http://10.0.0.2:8888", "http://searxng.example.com", "http://box.tailnet.ts.net:8888", "ftp://127.0.0.1"} {
		if _, err := search.New(search.Config{Providers: []string{"searxng"}, SearxngURL: u}); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestSearxngJSONFixture(t *testing.T) {
	fx, err := os.ReadFile("testdata/searxng.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.RawQuery
		if r.URL.Path != "/search" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fx)
	}))
	defer srv.Close()
	p, err := search.New(search.Config{Providers: []string{"searxng"}, SearxngURL: srv.URL, SnippetChars: 20})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Search(context.Background(), search.Query{Q: "서울 날씨", Lang: "ko", TimeRange: "day", Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQ, "format=json") || !strings.Contains(gotQ, "language=ko") || !strings.Contains(gotQ, "time_range=day") {
		t.Fatal(gotQ)
	}
	if len(res) != 2 { // javascript: and duplicate dropped
		t.Fatalf("%d results: %+v", len(res), res)
	}
	if res[0].URL != "https://ko.wikipedia.org/wiki/서울" || res[0].Published == nil || len([]rune(res[0].Snippet)) > 21 {
		t.Fatalf("%+v", res[0])
	}
}

func TestSearxngErrors(t *testing.T) {
	for code, kind := range map[int]string{429: "captcha", 500: "http"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		p, _ := search.New(search.Config{Providers: []string{"searxng"}, SearxngURL: srv.URL})
		_, err := p.Search(context.Background(), search.Query{Q: "x"})
		if err == nil || !strings.Contains(err.Error(), kind) {
			t.Errorf("%d: %v", code, err)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }))
	defer srv.Close()
	p, _ := search.New(search.Config{Providers: []string{"searxng"}, SearxngURL: srv.URL})
	if _, err := p.Search(context.Background(), search.Query{Q: "x"}); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatal(err)
	}
}
