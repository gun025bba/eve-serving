package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Domain policy runs on the requested URL and again on every redirect hop
// (docs/tool-platform.md §13), with the hop number recorded.
func TestHostPolicy_EveryRedirectHop(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "start.example.com":
			http.Redirect(w, r, "http://hop1.example.com/", 302)
		case "hop1.example.com":
			http.Redirect(w, r, "http://x.denied.example.org/", 302)
		default:
			reached.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "should not be reached")
		}
	}))
	defer srv.Close()
	res := &fakeResolver{m: map[string][]string{"start.example.com": {pubIP}, "hop1.example.com": {pubIP}, "x.denied.example.org": {pubIP}}}
	var hops []int
	pol := func(u *url.URL, hop int) *BlockedError {
		hops = append(hops, hop)
		if strings.HasSuffix(u.Hostname(), ".denied.example.org") {
			return &BlockedError{Reason: ReasonDomainDenied, Detail: "domain policy", Rule: "deny:0", Host: u.Hostname(), Hop: hop}
		}
		return nil
	}
	f := newTestFetcher(t, srv, res, Options{HostPolicy: pol})
	ctx := WithRedirectPolicy(context.Background(), func(*url.URL) bool { return true })
	_, err := f.Fetch(ctx, "http://start.example.com/", 0)
	mustBlocked(t, err, ReasonDomainDenied)
	var be *BlockedError
	if !errors.As(err, &be) || be.Hop != 2 || be.Rule != "deny:0" {
		t.Fatalf("want hop 2 rule deny:0, got %+v", be)
	}
	if reached.Load() != 0 {
		t.Fatal("denied host was contacted")
	}
	if fmt.Sprint(hops) != "[0 1 2]" {
		t.Fatalf("policy must run on every hop, got %v", hops)
	}
	// Requested URL itself denied: no network at all.
	_, err = f.Fetch(context.Background(), "http://a.denied.example.org/", 0)
	mustBlocked(t, err, ReasonDomainDenied)
}

// Resolved-address deny list includes IPv4-mapped IPv6, unspecified and
// multicast, checked on the address actually dialed.
func TestDNSAnswerDenyList_MappedUnspecifiedMulticast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("reached %s", r.Host)
	}))
	defer srv.Close()
	for _, ip := range []string{"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "0.0.0.0", "::", "224.0.0.1", "239.1.2.3", "ff02::1", "ff05::2"} {
		res := &fakeResolver{m: map[string][]string{"evil.example.com": {ip}}}
		f := newTestFetcher(t, srv, res, Options{})
		_, err := f.Fetch(context.Background(), "http://evil.example.com/", 0)
		mustBlocked(t, err, ReasonSSRF)
	}
}

// Low (PR #13 review): Teredo and 6to4 answers are refused at connect time.
func TestDNSAnswerDenyList_TeredoAnd6to4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("reached %s", r.Host)
	}))
	defer srv.Close()
	for _, ip := range []string{"2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1", "2002:c0a8:0101::1"} {
		res := &fakeResolver{m: map[string][]string{"evil.example.com": {ip}}}
		f := newTestFetcher(t, srv, res, Options{})
		_, err := f.Fetch(context.Background(), "http://evil.example.com/", 0)
		mustBlocked(t, err, ReasonSSRF)
	}
}
