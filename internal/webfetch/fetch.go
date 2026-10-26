package webfetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// Resolver resolves names to IPs (net.DefaultResolver in production; a fake in tests).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configures a Fetcher. Zero values get the documented defaults.
type Options struct {
	Timeout        time.Duration // whole request incl. body (10s)
	ConnectTimeout time.Duration // 3s
	HeaderTimeout  time.Duration // 5s
	MaxBytes       int64         // decompressed body cap (2 MiB)
	MaxChars       int           // text returned to the model (6000)
	MaxRedirects   int           // 3
	AllowedPorts   []int         // 80,443
	DenyCIDRs      []string
	GlobalConc     int // 4
	PerHostConc    int // 2
	UserAgent      string
	Resolver       Resolver
	// HostPolicy is the gateway domain policy, checked for the requested URL
	// and again for every redirect hop before the SSRF checks.
	HostPolicy HostPolicy
	// dialIP, if set, replaces the raw TCP dial of an already validated
	// ip:port. Unexported on purpose: only this package's tests can set it
	// (to reach httptest servers); production always dials through
	// net.Dialer with Guard.Control.
	dialIP func(ctx context.Context, network, ipport string) (net.Conn, error)
}

// HostPolicy decides whether a URL's host may be reached. It returns a non-nil
// *BlockedError to block.
type HostPolicy func(u *url.URL, hop int) *BlockedError

type redirectPolicyKey struct{}

// WithRedirectPolicy attaches a per-request allow check for cross-host
// redirects (e.g. the tool session's fetch allowlist). Without a policy every
// cross-host redirect is refused.
func WithRedirectPolicy(ctx context.Context, allow func(*url.URL) bool) context.Context {
	return context.WithValue(ctx, redirectPolicyKey{}, allow)
}

func sameSite(a, b string) bool {
	a, b = strings.TrimPrefix(strings.ToLower(a), "www."), strings.TrimPrefix(strings.ToLower(b), "www.")
	return a == b
}

// Fetcher is the single web_fetch client. It owns one dedicated Transport.
type Fetcher struct {
	o      Options
	G      *Guard
	client *http.Client
	global chan struct{}
	mu     sync.Mutex
	hosts  map[string]int
	hostCV *sync.Cond
}

// Result is what the tool returns (text is untrusted).
type Result struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"-"`
}

var allowedTypes = map[string]bool{"text/html": true, "text/plain": true, "application/xhtml+xml": true, "application/json": true}

func New(o Options) (*Fetcher, error) {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 3 * time.Second
	}
	if o.HeaderTimeout <= 0 {
		o.HeaderTimeout = 5 * time.Second
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 2 << 20
	}
	if o.MaxChars <= 0 {
		o.MaxChars = 6000
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 3
	}
	if o.GlobalConc <= 0 {
		o.GlobalConc = 4
	}
	if o.PerHostConc <= 0 {
		o.PerHostConc = 2
	}
	if o.UserAgent == "" {
		o.UserAgent = "llm-gateway-webfetch/1.0"
	}
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	g, err := NewGuard(o.AllowedPorts, o.DenyCIDRs)
	if err != nil {
		return nil, err
	}
	f := &Fetcher{o: o, G: g, global: make(chan struct{}, o.GlobalConc), hosts: map[string]int{}}
	f.hostCV = sync.NewCond(&f.mu)
	dialer := &net.Dialer{Timeout: o.ConnectTimeout, Control: g.Control}
	tr := &http.Transport{
		Proxy:                 nil, // never honour HTTP(S)_PROXY
		DialContext:           f.dialContext(dialer),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: o.HeaderTimeout,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    false, // Go adds Accept-Encoding: gzip and decompresses transparently
		ForceAttemptHTTP2:     true,
	}
	f.client = &http.Client{
		Transport: tr,
		Timeout:   o.Timeout,
		Jar:       nil,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > o.MaxRedirects {
				return blocked(ReasonRedirect, "too many redirects")
			}
			prev := via[len(via)-1].URL
			if strings.EqualFold(prev.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
				return blocked(ReasonRedirect, "https->http downgrade")
			}
			if o.HostPolicy != nil {
				if be := o.HostPolicy(req.URL, len(via)); be != nil {
					return be
				}
			}
			if _, _, err := g.CheckURL(req.URL); err != nil {
				return err
			}
			if !sameSite(via[0].URL.Hostname(), req.URL.Hostname()) {
				allow, _ := req.Context().Value(redirectPolicyKey{}).(func(*url.URL) bool)
				if allow == nil || !allow(req.URL) {
					return blocked(ReasonAllowlist, "cross-domain redirect to a URL outside the allowlist")
				}
			}
			req.Header.Del("Referer")
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			return nil
		},
	}
	return f, nil
}

// Transport exposes the dedicated transport (for tests asserting Proxy==nil).
func (f *Fetcher) Transport() *http.Transport { return f.client.Transport.(*http.Transport) }

// dialContext resolves the name itself (fail-fast check on every answer),
// then dials the validated IP literal so Control sees exactly that IP. Host
// header and TLS SNI keep the original name because http.Transport derives
// them from the request URL, not from the dialed address.
func (f *Fetcher) dialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := splitHostPort(addr)
		if err != nil {
			return nil, blocked(ReasonSSRF, "bad addr")
		}
		if err := f.G.CheckPort(port); err != nil {
			return nil, err
		}
		var ips []netip.Addr
		if a, perr := netip.ParseAddr(host); perr == nil {
			ips = []netip.Addr{a}
		} else {
			ips, err = f.o.Resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("dns: %w", err)
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("dns: no addresses")
		}
		// Every answer must be public; one private answer fails the whole name
		// (defeats round-robin rebinding tricks).
		for _, ip := range ips {
			if err := f.G.CheckIP(ip); err != nil {
				return nil, err
			}
		}
		var last error
		for _, ip := range ips {
			ip = ip.Unmap()
			nw := "tcp4"
			if ip.Is6() {
				nw = "tcp6"
			}
			ipport := netip.AddrPortFrom(ip, uint16(port)).String()
			var c net.Conn
			if f.o.dialIP != nil {
				if err := f.G.CheckIP(ip); err != nil { // mirror Control in tests
					return nil, err
				}
				c, err = f.o.dialIP(ctx, nw, ipport)
			} else {
				c, err = d.DialContext(ctx, nw, ipport)
			}
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
}

func (f *Fetcher) acquire(ctx context.Context, host string) (func(), error) {
	select {
	case f.global <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f.mu.Lock()
	for f.hosts[host] >= f.o.PerHostConc {
		if ctx.Err() != nil {
			f.mu.Unlock()
			<-f.global
			return nil, ctx.Err()
		}
		f.hostCV.Wait()
	}
	f.hosts[host]++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.hosts[host]--
		if f.hosts[host] <= 0 {
			delete(f.hosts, host)
		}
		f.hostCV.Broadcast()
		f.mu.Unlock()
		<-f.global
	}, nil
}

// InFlight returns (global, per-host) usage; for tests.
func (f *Fetcher) InFlight(host string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.global), f.hosts[host]
}

// Fetch GETs rawURL under all policies. maxChars<=0 uses the default.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxChars int) (*Result, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, blocked(ReasonURL, "unparsable url")
	}
	u.Fragment = ""
	if f.o.HostPolicy != nil {
		if be := f.o.HostPolicy(u, 0); be != nil {
			return nil, be
		}
	}
	host, _, err := f.G.CheckURL(u)
	if err != nil {
		return nil, err
	}
	if maxChars <= 0 || maxChars > f.o.MaxChars*2 {
		maxChars = f.o.MaxChars
	}
	rel, err := f.acquire(ctx, host)
	if err != nil {
		return nil, err
	}
	defer rel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, blocked(ReasonURL, "bad request")
	}
	req.Header.Set("User-Agent", f.o.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.8")
	// Deliberately no Accept-Encoding: Go then decompresses gzip itself and
	// our LimitReader below caps the *decompressed* size.
	resp, err := f.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			var be *BlockedError
			if errors.As(ue.Err, &be) {
				return nil, be
			}
			if r, ok := IsBlocked(ue.Err); ok {
				return nil, &BlockedError{Reason: r, Detail: ue.Err.Error()}
			}
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	mt, params, perr := mime.ParseMediaType(ct)
	if ct == "" || perr != nil || !allowedTypes[strings.ToLower(mt)] {
		return nil, blocked(ReasonContentType, "content-type %q", trunc(ct, 60))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, f.o.MaxBytes+1))
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	truncated := int64(len(raw)) > f.o.MaxBytes
	if truncated {
		raw = raw[:f.o.MaxBytes]
	}
	sum := sha256.Sum256(raw)
	body := decode(raw, params["charset"], ct)
	var title, text string
	if strings.Contains(mt, "html") {
		title, text = ExtractText(body)
	} else {
		text = body
	}
	text = collapse(text)
	if utf8.RuneCountInString(text) > maxChars {
		text = string([]rune(text)[:maxChars])
		truncated = true
	}
	return &Result{URL: rawURL, FinalURL: resp.Request.URL.String(), Title: trunc(title, 300), Text: text, Truncated: truncated,
		Bytes: int64(len(raw)), ContentType: mt, SHA256: hex.EncodeToString(sum[:])}, nil
}

func decode(b []byte, cs, ct string) string {
	if cs == "" || strings.EqualFold(cs, "utf-8") {
		return strings.ToValidUTF8(string(b), "\uFFFD")
	}
	r, err := charset.NewReader(bytes.NewReader(b), ct)
	if err != nil {
		return strings.ToValidUTF8(string(b), "\uFFFD")
	}
	out, _ := io.ReadAll(r)
	return strings.ToValidUTF8(string(out), "\uFFFD")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func collapse(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
