//go:build qaharness

package webfetch_test

// QA harness for issue #140 (design auto-tool-selection §6.8), path 1: corpus -> Fetcher.
//
// Written against the exported test hook Options.SetDialIPForTest (architect decision: hook compiled only
// with -tags qa_testhook; proposal in ../testhook/webfetch_testhook.go.txt). Until the backend ships it, run
// the FALLBACK: copy this file and export_qa_test.go into internal/webfetch/ (export_qa_test.go defines the
// hook inside the package's own test build):
//   cp tests/e2e/issue140/go/webfetch/*_test.go internal/webfetch/
//   go test -tags qaharness -count=1 -v -run TestQA_SSRF ./internal/webfetch/
// With the hook shipped, no copy is needed:
//   go test -tags 'qaharness qa_testhook' -count=1 -v ./tests/e2e/issue140/go/webfetch/
// Nothing here touches the gateway, any real network or any real DNS: names resolve through a stub
// resolver and every "dial" is intercepted. A dial that reaches the hook with a non-public IP (or a
// forbidden port) is a violation; a public stub hop is routed to an in-process httptest server.
//
// Corpus lookup order: $EVE_QA_SSRF_DIR, ./testdata, ../../ssrf, ../../tests/e2e/issue140/ssrf.
// Optional private holdout: $EVE_QA_SSRF_HOLDOUT (file in the same format; skipped when unset).

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/webfetch"
)

var qaPub = netip.MustParseAddr("93.184.216.34")

type qaEntry struct{ cat, raw, url string }

func qaDecode(s string) string {
	return strings.NewReplacer("<TAB>", "\t", "<LF>", "\n", "<CR>", "\r", "<NUL>", "\x00", "<SP>", " ").Replace(s)
}

func qaFindFile(t testing.TB, name string, env string) string {
	t.Helper()
	var dirs []string
	if d := os.Getenv("EVE_QA_SSRF_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, "testdata", filepath.Join("..", "..", "ssrf"), filepath.Join("..", "..", "tests", "e2e", "issue140", "ssrf"))
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("corpus file %s not found in %v", name, dirs)
	return ""
}

func qaLoad(t testing.TB, path string) []qaEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []qaEntry
	cat := "uncategorized"
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	re := regexp.MustCompile(`^#\s*\[([a-z0-9_]+)\]`)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(l, "#") {
			if m := re.FindStringSubmatch(l); m != nil {
				cat = m[1]
			}
			continue
		}
		out = append(out, qaEntry{cat: cat, raw: l, url: qaDecode(l)})
	}
	return out
}

// qaIsPublic is an INDEPENDENT oracle (does not call the production Guard).
var qaDenyNets = func() []netip.Prefix {
	var p []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/96", "64:ff9b::/96", "100::/64", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		p = append(p, netip.MustParsePrefix(s))
	}
	return p
}()

func qaIsPublic(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range qaDenyNets {
		if p.Contains(a) {
			return false
		}
	}
	return a.IsValid() && a.IsGlobalUnicast()
}

type qaResolver struct {
	mu      sync.Mutex
	lookups map[string]int
	log     []string
}

var (
	qaDNS4 = regexp.MustCompile(`^dns-(\d+)-(\d+)-(\d+)-(\d+)\.stub\.example$`)
	qaNip  = regexp.MustCompile(`^(\d+\.\d+\.\d+\.\d+)\.nip\.io$`)
	qaReb  = regexp.MustCompile(`^rebind-[0-9a-z]+\.stub\.example$`)
)

func (r *qaResolver) reset() { r.mu.Lock(); r.lookups = map[string]int{}; r.log = nil; r.mu.Unlock() }

func (r *qaResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.Lock()
	r.lookups[h]++
	n := r.lookups[h]
	r.log = append(r.log, h)
	r.mu.Unlock()
	switch {
	case h == "redir.stub.example":
		return []netip.Addr{qaPub}, nil
	case qaReb.MatchString(h):
		if n == 1 {
			return []netip.Addr{qaPub}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	case h == "mixed.stub.example":
		return []netip.Addr{qaPub, netip.MustParseAddr("10.0.0.9")}, nil
	case h == "dns6-loopback.stub.example":
		return []netip.Addr{netip.MustParseAddr("::1")}, nil
	case h == "dns6-mapped.stub.example":
		return []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}, nil
	case h == "dns6-ula.stub.example":
		return []netip.Addr{netip.MustParseAddr("fd00::1")}, nil
	}
	if m := qaDNS4.FindStringSubmatch(h); m != nil {
		a, err := netip.ParseAddr(fmt.Sprintf("%s.%s.%s.%s", m[1], m[2], m[3], m[4]))
		if err == nil {
			return []netip.Addr{a}, nil
		}
	}
	if m := qaNip.FindStringSubmatch(h); m != nil {
		if a, err := netip.ParseAddr(m[1]); err == nil {
			return []netip.Addr{a}, nil
		}
	}
	// Any other name looks public: a URL that policy fails to refuse then shows up as SUCCESS/ERROR (= FAIL).
	return []netip.Addr{qaPub}, nil
}

type qaEnv struct {
	f        *webfetch.Fetcher
	res      *qaResolver
	srv      *httptest.Server
	tlsSrv   *httptest.Server
	allowed  map[int]bool
	mu       sync.Mutex
	dials    int
	badDials []string
}

func newQAEnv(t testing.TB, ports []int) *qaEnv {
	t.Helper()
	e := &qaEnv{res: &qaResolver{lookups: map[string]int{}}, allowed: map[int]bool{}}
	for _, p := range ports {
		e.allowed[p] = true
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to := r.URL.Query().Get("to"); to != "" && strings.HasPrefix(r.Host, "redir.stub.example") {
			w.Header().Set("Location", to)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "stub ok")
	})
	e.srv = httptest.NewServer(h)
	e.tlsSrv = httptest.NewTLSServer(h)
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.tlsSrv.Close)
	o := webfetch.Options{Resolver: e.res, AllowedPorts: ports, Timeout: 4 * time.Second, ConnectTimeout: time.Second, HeaderTimeout: 2 * time.Second}
	o.SetDialIPForTest(func(ctx context.Context, network, ipport string) (net.Conn, error) {
		ap, perr := netip.ParseAddrPort(ipport)
		// Mirror the authoritative net.Dialer.Control (production runs it at connect time). Only dials
		// that survive Control are counted; the independent oracle below then audits what survived.
		if cerr := e.f.G.Control(network, ipport, nil); cerr != nil {
			return nil, cerr
		}
		e.mu.Lock()
		e.dials++
		bad := perr != nil || !qaIsPublic(ap.Addr()) || !e.allowed[int(ap.Port())] || webfetch.DeniedPorts[int(ap.Port())]
		if bad {
			e.badDials = append(e.badDials, ipport)
		}
		e.mu.Unlock()
		if bad {
			return nil, errors.New("QA: private/forbidden dial intercepted")
		}
		var d net.Dialer
		if ap.Port() == 443 {
			return d.DialContext(ctx, "tcp", e.tlsSrv.Listener.Addr().String())
		}
		return d.DialContext(ctx, "tcp", e.srv.Listener.Addr().String())
	})
	f, err := webfetch.New(o)
	if err != nil {
		t.Fatal(err)
	}
	f.Transport().TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test-only: stub TLS server
	e.f = f
	return e
}

type qaOutcome struct {
	class, reason string
	bad           []string
	dials         int
	lookups       []string
}

func (e *qaEnv) run(raw string) qaOutcome {
	e.res.reset()
	e.mu.Lock()
	e.dials, e.badDials = 0, nil
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.f.Fetch(ctx, raw, 0)
	o := qaOutcome{}
	e.mu.Lock()
	o.bad, o.dials = append([]string(nil), e.badDials...), e.dials
	e.mu.Unlock()
	e.res.mu.Lock()
	o.lookups = append([]string(nil), e.res.log...)
	e.res.mu.Unlock()
	switch {
	case err == nil:
		o.class = "SUCCESS"
	default:
		if r, ok := webfetch.IsBlocked(err); ok {
			o.reason = r
			if r == webfetch.ReasonSSRF {
				o.class = "BLOCKED_SSRF"
			} else {
				o.class = "BLOCKED_OTHER"
			}
		} else {
			o.class = "ERROR"
			o.reason = qaTrunc(err.Error(), 80)
		}
	}
	return o
}

// rebindLenient: a rebinding stub may legitimately be fetched from its (validated) public first answer;
// the invariant is that no private IP is ever dialed.
func qaRebindLenient(raw string) bool {
	u, err := url.Parse(qaDecode(raw))
	return err == nil && qaReb.MatchString(strings.ToLower(u.Hostname())) && !strings.Contains(raw, "redir.stub")
}

func qaRunCorpus(t *testing.T, ents []qaEntry, ports []int, only func(qaEntry) bool, label string) {
	e := newQAEnv(t, ports)
	type agg struct{ n, strict, other, errc, fail int }
	cats := map[string]*agg{}
	reasons := map[string]int{}
	var fails, errs []string
	n := 0
	for _, en := range ents {
		if only != nil && !only(en) {
			continue
		}
		n++
		a := cats[en.cat]
		if a == nil {
			a = &agg{}
			cats[en.cat] = a
		}
		a.n++
		o := e.run(en.url)
		bad := len(o.bad) > 0
		switch o.class {
		case "BLOCKED_SSRF":
			a.strict++
			reasons["ssrf"]++
		case "BLOCKED_OTHER":
			a.other++
			reasons[o.reason]++
		case "ERROR":
			a.errc++
			reasons["error"]++
			errs = append(errs, fmt.Sprintf("  [%s] %q -> %s lookups=%v", en.cat, en.raw, o.reason, o.lookups))
		case "SUCCESS":
			reasons["SUCCESS"]++
		}
		okFetch := o.class != "SUCCESS" || qaRebindLenient(en.raw)
		if bad || !okFetch {
			a.fail++
			fails = append(fails, fmt.Sprintf("  FAIL [%s] %q class=%s reason=%q privateDials=%v", en.cat, en.raw, o.class, o.reason, o.bad))
		}
	}
	var names []string
	for k := range cats {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("[%s] ports=%v entries=%d", label, ports, n)
	totF := 0
	for _, k := range names {
		a := cats[k]
		totF += a.fail
		t.Logf("  %-20s n=%-3d ssrf=%-3d other_block=%-3d error=%-3d FAIL=%d", k, a.n, a.strict, a.other, a.errc, a.fail)
	}
	var rk []string
	for k := range reasons {
		rk = append(rk, k)
	}
	sort.Strings(rk)
	var rs []string
	for _, k := range rk {
		rs = append(rs, k+"="+strconv.Itoa(reasons[k]))
	}
	t.Logf("  reason histogram: %s", strings.Join(rs, " "))
	if os.Getenv("EVE_QA_VERBOSE") != "" {
		for _, s := range errs {
			t.Log(s)
		}
	} else {
		t.Logf("  non-block errors (fail-closed by DNS/transport, set EVE_QA_VERBOSE=1 to list): %d", len(errs))
	}
	for _, s := range fails {
		t.Error(s)
	}
}

func TestQA_SSRF_CorpusDirect(t *testing.T) {
	ents := qaLoad(t, qaFindFile(t, "ssrf_urls.txt", ""))
	if len(ents) < 100 {
		t.Fatalf("corpus suspiciously small: %d", len(ents))
	}
	qaRunCorpus(t, ents, []int{80, 443}, nil, "default ports")
}

// With deployment configs that open extra ports, DeniedPorts (5432/6379/8000/8080/8888) must still refuse.
func TestQA_SSRF_DeniedPortsWithWideConfig(t *testing.T) {
	ents := qaLoad(t, qaFindFile(t, "ssrf_urls.txt", ""))
	only := func(en qaEntry) bool {
		u, err := url.Parse(en.url)
		if err != nil {
			return false
		}
		p, err := strconv.Atoi(u.Port())
		return err == nil && webfetch.DeniedPorts[p]
	}
	qaRunCorpus(t, ents, []int{80, 443, 5432, 6379, 8000, 8080, 8888}, only, "wide ports config")
}

// Over-blocking control: public URLs must pass the URL pre-check (nothing is fetched).
func TestQA_SSRF_AllowControl(t *testing.T) {
	g, err := webfetch.NewGuard(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ents := qaLoad(t, qaFindFile(t, "ssrf_allow_urls.txt", ""))
	for _, en := range ents {
		u, perr := url.Parse(en.url)
		if perr != nil {
			t.Errorf("OVERBLOCK parse %q: %v", en.raw, perr)
			continue
		}
		u.Fragment = ""
		if _, _, cerr := g.CheckURL(u); cerr != nil {
			t.Errorf("OVERBLOCK %q: %v", en.raw, cerr)
		}
	}
	t.Logf("allow-control entries: %d", len(ents))
}

// Private holdout (novel spellings kept out of the repo). Skipped unless EVE_QA_SSRF_HOLDOUT is set.
func TestQA_SSRF_PrivateHoldout(t *testing.T) {
	p := os.Getenv("EVE_QA_SSRF_HOLDOUT")
	if p == "" {
		t.Skip("EVE_QA_SSRF_HOLDOUT not set")
	}
	qaRunCorpus(t, qaLoad(t, p), []int{80, 443}, nil, "private holdout")
}

func qaTrunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
