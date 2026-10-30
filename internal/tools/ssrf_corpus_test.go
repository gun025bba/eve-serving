//go:build qa_testhook

package tools_test

// QA harness for issue #140 (design auto-tool-selection §6.8), path 2: corpus -> tool-call layer.
//
// What a mock model's scripted web_fetch call exercises: a real Session (real domain policy, real allowlist,
// real webfetch.Fetcher). The URL is also placed in the user's message so the allowlist layer does not trivially
// refuse everything. Run in place (external package, exported API only):
//   go test -tags qa_testhook -count=1 -v -run TestQA_SSRF ./tests/e2e/issue140/go/tools/
// Add -tags qa_testhook once the backend ships webfetch.Options.SetDialIPForTest: every dial is then
// counted and must be 0 ("dial 0", §6.8).
//
// Network safety: the Fetcher's resolver is a stub that answers private IPs for dns-* stub names and errors for
// everything else, so no connection can be opened even without the hook.
//
// Error-code contract (architect decision 1): ANY block reason is accepted for blocking itself, but items whose
// corpus category is marked (ssrf) must expose error_code == "ssrf" in the tool result. Mode via
// EVE_QA_SSRF_ERRCODE: "ssrf-items" (default) | "any" (skip the contract check) | "off" (same as any).
// error_code is looked up (a) in the tool-result payload JSON and (b) in a Call.ErrorCode field if one exists.
// Today neither exists (SSE tool_result carries only status; see the #136 note) -> TestQA_SSRF_ErrorCodeContract
// fails by design until the backend ships it, independently of the blocking test.
//
// Corpus lookup: $EVE_QA_SSRF_DIR, ./testdata, ../webfetch/testdata, ../../ssrf, ../../tests/e2e/issue140/ssrf.
// Static entries only (rebind-*/redir.stub.example need an HTTP stub, covered by the webfetch harness).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	policydata "github.com/gun025bba/eve-serving/deploy/policy"
	"github.com/gun025bba/eve-serving/internal/policy/domains"
	"github.com/gun025bba/eve-serving/internal/tools"
	"github.com/gun025bba/eve-serving/internal/webfetch"
)

type qa2Resolver struct {
	mu  sync.Mutex
	log []string
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

type qa2Entry struct{ cat, class, raw, url string }

func qa2Load(t testing.TB, name string) (out []qa2Entry) {
	t.Helper()
	var dirs []string
	if d := os.Getenv("EVE_QA_SSRF_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, "testdata", filepath.Join("..", "webfetch", "testdata"), filepath.Join("..", "..", "ssrf"), filepath.Join("..", "..", "tests", "e2e", "issue140", "ssrf"))
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
	cat, class := "uncategorized", "any"
	re := regexp.MustCompile(`^#\s*\[([a-z0-9_]+)\]\s*(?:\((ssrf|any)\))?`)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(l, "#") {
			if m := re.FindStringSubmatch(l); m != nil {
				cat, class = m[1], "any"
				if m[2] == "ssrf" {
					class = "ssrf"
				}
			}
			continue
		}
		dec := strings.NewReplacer("<TAB>", "\t", "<LF>", "\n", "<CR>", "\r", "<NUL>", "\x00", "<SP>", " ").Replace(l)
		out = append(out, qa2Entry{cat, class, l, dec})
	}
	return
}

func qa2Layer(c tools.Call) string {
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

var qa2ErrCodeRe = regexp.MustCompile(`"error_code"\s*:\s*"([^"]*)"`)

// qa2ErrorCode returns the error_code the client can see: payload JSON first, then Call.ErrorCode (if the field exists).
func qa2ErrorCode(out string, c tools.Call) (string, bool) {
	if m := qa2ErrCodeRe.FindStringSubmatch(out); m != nil {
		return m[1], true
	}
	if f := reflect.ValueOf(c).FieldByName("ErrorCode"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
		return f.String(), true
	}
	return "", false
}

func qa2Run(t *testing.T) (ents []qa2Entry, outs []string, calls []tools.Call, lookups [][]string, skipped int) {
	pol, err := domains.Load(policydata.FS, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	res := &qa2Resolver{}
	o := webfetch.Options{Resolver: res, HostPolicy: tools.HostPolicy(pol), Timeout: 3 * time.Second}
	qaInstallDialCounter(&o)
	fetcher, err := webfetch.New(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range qa2Load(t, "ssrf_urls.txt") {
		if strings.Contains(en.raw, "rebind-") || strings.Contains(en.raw, "redir.stub") {
			skipped++
			continue
		}
		res.mu.Lock()
		res.log = nil
		res.mu.Unlock()
		s := tools.NewSession(tools.Deps{Domains: pol, Fetch: fetcher, Limits: tools.Limits{MaxCalls: 8}}, "qa", []string{"please read " + en.url + " now"})
		args, _ := json.Marshal(map[string]string{"url": en.url})
		out, c := s.Exec(context.Background(), 1, "qa", "web_fetch", string(args))
		ents, outs, calls = append(ents, en), append(outs, out), append(calls, c)
		res.mu.Lock()
		lookups = append(lookups, append([]string(nil), res.log...))
		res.mu.Unlock()
	}
	return
}

func qa2Hist(m map[string]int) string {
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

// Blocking: every static corpus URL must end as status=blocked with no page text, any reason; dial count 0 (hook).
func TestQA_SSRF_ToolsLayer(t *testing.T) {
	ents, outs, calls, lookups, skipped := qa2Run(t)
	type agg struct{ n, blocked, fail int }
	cats := map[string]*agg{}
	layers, reasons := map[string]int{}, map[string]int{}
	for i, en := range ents {
		a := cats[en.cat]
		if a == nil {
			a = &agg{}
			cats[en.cat] = a
		}
		a.n++
		c := calls[i]
		if c.Status == "blocked" {
			a.blocked++
			layers[qa2Layer(c)]++
			reasons[c.BlockReason]++
		}
		if c.Status != "blocked" || strings.Contains(outs[i], `"text"`) {
			a.fail++
			t.Errorf("  FAIL [%s] %s status=%s reason=%s lookups=%s", en.cat, strconv.Quote(en.raw), c.Status, c.BlockReason, strings.Join(lookups[i], ","))
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
	t.Logf("blocking layer: %s", qa2Hist(layers))
	t.Logf("block reason (Call.BlockReason, not exposed to clients today): %s", qa2Hist(reasons))
	t.Logf("skipped (needs an HTTP stub; covered by the webfetch harness): %d", skipped)
	qaAssertNoDials(t)
}

// Contract: (ssrf)-class items must expose error_code=="ssrf". Mode: EVE_QA_SSRF_ERRCODE (default ssrf-items).
func TestQA_SSRF_ErrorCodeContract(t *testing.T) {
	mode := os.Getenv("EVE_QA_SSRF_ERRCODE")
	if mode == "any" || mode == "off" {
		t.Skipf("EVE_QA_SSRF_ERRCODE=%s: error_code contract not enforced", mode)
	}
	ents, outs, calls, _, _ := qa2Run(t)
	n, absent, wrong := 0, 0, 0
	got := map[string]int{}
	var sample []string
	for i, en := range ents {
		if en.class != "ssrf" {
			continue
		}
		n++
		code, ok := qa2ErrorCode(outs[i], calls[i])
		switch {
		case !ok:
			absent++
		case code != "ssrf":
			wrong++
			got[code]++
		}
		if (!ok || code != "ssrf") && len(sample) < 5 {
			sample = append(sample, strconv.Quote(en.raw)+" -> reason="+calls[i].BlockReason+" error_code="+strconv.Quote(code))
		}
	}
	t.Logf("(ssrf)-class items=%d, error_code absent=%d, present-but-not-ssrf=%d %s", n, absent, wrong, qa2Hist(got))
	if absent+wrong > 0 {
		t.Errorf("error_code contract violated for %d/%d ssrf items; examples:\n  %s", absent+wrong, n, strings.Join(sample, "\n  "))
	}
}
