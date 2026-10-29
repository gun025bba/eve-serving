package tools

// QA harness for issue #140 (design auto-tool-selection §6.8), path 2: corpus -> tool-call layer.
//
// This is what the model-driven path exercises: a scripted web_fetch call (as a mock model would emit)
// executed by a real Session (real domain policy, real allowlist, real webfetch.Fetcher). The URL is also
// placed in the user's message so the allowlist layer does not trivially refuse everything.
// Copy into internal/tools/ and run:
//   go test -count=1 -v -run 'TestQA_SSRF' ./internal/tools/
// Network safety: the Fetcher's resolver is a stub that answers private IPs for dns-* stub names and
// errors for everything else, so no connection can ever be opened (verified by the lookup log).
//
// Corpus lookup: $EVE_QA_SSRF_DIR, ./testdata, ../webfetch/testdata, ../../tests/e2e/issue140/ssrf.
// Static entries only (rebind-*/redir.stub.example need an HTTP stub, covered by the webfetch harness).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
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

type qa2Resolver struct {
	mu  sync.Mutex
	log []string
	pub int
}

var qa2DNS4 = regexp.MustCompile(`^dns-(\d+)-(\d+)-(\d+)-(\d+)\.stub\.example$`)

func (r *qa2Resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.Lock()
	r.log = append(r.log, h)
	r.mu.Unlock()
	switch h {
	case "mixed.stub.example":
		return []netip.Addr{netip.MustParseAddr("10.0.0.9"), netip.MustParseAddr("93.184.216.34")}, nil
	case "dns6-loopback.stub.example":
		return []netip.Addr{netip.MustParseAddr("::1")}, nil
	case "dns6-mapped.stub.example":
		return []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}, nil
	case "dns6-ula.stub.example":
		return []netip.Addr{netip.MustParseAddr("fd00::1")}, nil
	}
	if m := qa2DNS4.FindStringSubmatch(h); m != nil {
		if a, err := netip.ParseAddr(m[1] + "." + m[2] + "." + m[3] + "." + m[4]); err == nil {
			return []netip.Addr{a}, nil
		}
	}
	return nil, errors.New("no such host (QA stub: never resolves publicly)")
}

func qa2Load(t testing.TB, name string) (out [][3]string) {
	t.Helper()
	var dirs []string
	if d := os.Getenv("EVE_QA_SSRF_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, "testdata", filepath.Join("..", "webfetch", "testdata"), filepath.Join("..", "..", "tests", "e2e", "issue140", "ssrf"))
	var f *os.File
	for _, d := range dirs {
		if x, err := os.Open(filepath.Join(d, name)); err == nil {
			f = x
			break
		}
	}
	if f == nil {
		t.Fatalf("%s not found in %v", name, dirs)
	}
	defer f.Close()
	cat := "uncategorized"
	re := regexp.MustCompile(`^#\s*\[([a-z0-9_]+)\]`)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
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
		dec := strings.NewReplacer("<TAB>", "\t", "<LF>", "\n", "<CR>", "\r", "<NUL>", "\x00", "<SP>", " ").Replace(l)
		out = append(out, [3]string{cat, l, dec})
	}
	return
}

func qa2Layer(c Call) string {
	switch c.BlockReason {
	case "":
		return "none"
	case webfetch.ReasonAllowlist:
		return "allowlist"
	case "ip_literal", "ip_literal_private", "invalid_host", "domain_denied", "not_allowlisted":
		return "domain_policy"
	default:
		return "fetcher"
	}
}

func TestQA_SSRF_ToolsLayer(t *testing.T) {
	ents := qa2Load(t, "ssrf_urls.txt")
	res := &qa2Resolver{}
	fetcher, err := webfetch.New(webfetch.Options{Resolver: res, HostPolicy: HostPolicy(testPolicy()), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	type agg struct{ n, blocked, fail int }
	cats := map[string]*agg{}
	layers := map[string]int{}
	reasons := map[string]int{}
	skipped := 0
	var fails []string
	for _, en := range ents {
		cat, raw, url := en[0], en[1], en[2]
		if strings.Contains(raw, "rebind-") || strings.Contains(raw, "redir.stub") {
			skipped++
			continue
		}
		a := cats[cat]
		if a == nil {
			a = &agg{}
			cats[cat] = a
		}
		a.n++
		res.mu.Lock()
		res.log = nil
		res.mu.Unlock()
		s := NewSession(Deps{Domains: testPolicy(), Fetch: fetcher, Limits: Limits{MaxCalls: 8}}, "qa", []string{"please read " + url + " now"})
		args, _ := json.Marshal(map[string]string{"url": url})
		out, c := s.Exec(context.Background(), 1, "qa", "web_fetch", string(args))
		if c.Status == "blocked" {
			a.blocked++
			layers[qa2Layer(c)]++
			reasons[c.BlockReason]++
		}
		if c.Status != "blocked" || strings.Contains(out, "untrusted_web_content") && strings.Contains(out, "\"text\"") {
			a.fail++
			fails = append(fails, "  FAIL ["+cat+"] "+strconv.Quote(raw)+" status="+c.Status+" reason="+c.BlockReason+" lookups="+strings.Join(res.log, ","))
		}
	}
	var names []string
	for k := range cats {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		a := cats[k]
		t.Logf("  %-20s n=%-3d blocked=%-3d FAIL=%d", k, a.n, a.blocked, a.fail)
	}
	hist := func(m map[string]int) string {
		var ks, o []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			o = append(o, k+"="+strconv.Itoa(m[k]))
		}
		return strings.Join(o, " ")
	}
	t.Logf("blocking layer: %s", hist(layers))
	t.Logf("block reason (Call.BlockReason, NOT in SSE/JSON today): %s", hist(reasons))
	t.Logf("skipped (needs HTTP stub; see webfetch harness): %d; error_code==\"ssrf\" would hold for only %d of %d blocked", skipped, reasons["ssrf"], sum(layers))
	for _, f := range fails {
		t.Error(f)
	}
}

func sum(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return
}
