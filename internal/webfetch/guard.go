// Package webfetch fetches public web pages for the web_fetch tool with SSRF
// protections. Security model (docs/web-search.md §2.8):
//
//   - Pre-checks on the URL (scheme, userinfo, port, host name, numeric hosts,
//     path/query length) and on DNS answers are fail-fast only.
//   - The authoritative check is net.Dialer.Control on the dedicated Transport:
//     it sees the exact IP being connected to (every Happy Eyeballs attempt,
//     every redirect hop), so DNS rebinding between lookup and connect (TOCTOU)
//     cannot reach an internal address.
//   - The Transport is dedicated to web_fetch (separate from the mlx client in
//     internal/proxy), has Proxy=nil, no cookie jar, and no auth headers.
package webfetch

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"

	"code.dny.dev/ssrf"
)

// Block reasons (also used as llm_fetch_blocked_total{reason} label values).
const (
	ReasonSSRF        = "ssrf"
	ReasonScheme      = "scheme"
	ReasonPort        = "port"
	ReasonHost        = "host"
	ReasonURL         = "url"
	ReasonContentType = "content_type"
	ReasonRedirect    = "redirect"
	ReasonAllowlist   = "allowlist"
	// Domain policy reasons (internal/policy/domains, docs/tool-platform.md §13).
	ReasonDomainDenied = "domain_denied"
)

// BlockedError is returned for any policy rejection.
type BlockedError struct {
	Reason string
	Detail string
	Rule   string // domain policy rule id (audit only, never shown to the model)
	Host   string // normalized host (audit hashes it)
	Hop    int    // 0 = requested URL, n = n-th redirect
}

func (e *BlockedError) Error() string { return "blocked (" + e.Reason + "): " + e.Detail }

func blocked(reason, f string, a ...any) error {
	return &BlockedError{Reason: reason, Detail: fmt.Sprintf(f, a...)}
}

// IsBlocked reports whether err is a policy rejection and its reason.
func IsBlocked(err error) (string, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b.Reason, true
	}
	// Control errors surface wrapped inside net.OpError / url.Error.
	if err != nil && (errors.Is(err, ssrf.ErrProhibitedIP) || errors.Is(err, ssrf.ErrProhibitedNetwork) || errors.Is(err, ssrf.ErrProhibitedPort) || errors.Is(err, ssrf.ErrInvalidHostPort)) {
		return ReasonSSRF, true
	}
	return "", false
}

// DeniedPorts are internal service ports that are never fetchable, regardless
// of fetch.allowed_ports (Postgres, Valkey, gateway, mlx, SearXNG).
var DeniedPorts = map[int]bool{5432: true, 6379: true, 8000: true, 8080: true, 8888: true}

// extraDenied adds Tailscale ranges and a few special-purpose ranges on top of
// the code.dny.dev/ssrf defaults (belt and braces; the library already covers
// most of these).
var extraDenied = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16",
	"fc00::/7", "fd7a:115c:a1e0::/48", "fe80::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Guard holds the IP/port policy.
type Guard struct {
	ports map[int]bool
	deny  []netip.Prefix
	lib   *ssrf.Guardian
}

// NewGuard builds a guard allowing the given ports (default 80,443) minus
// DeniedPorts, and denying extraCIDRs in addition to the built-in lists.
func NewGuard(allowedPorts []int, extraCIDRs []string) (*Guard, error) {
	if len(allowedPorts) == 0 {
		allowedPorts = []int{80, 443}
	}
	g := &Guard{ports: map[int]bool{}, deny: append([]netip.Prefix{}, extraDenied...)}
	var lp []uint16
	for _, p := range allowedPorts {
		if p < 1 || p > 65535 || DeniedPorts[p] {
			continue
		}
		g.ports[p] = true
		lp = append(lp, uint16(p))
	}
	for _, c := range extraCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("deny cidr %q: %w", c, err)
		}
		g.deny = append(g.deny, p.Masked())
	}
	var v4, v6 []netip.Prefix
	for _, p := range g.deny {
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	g.lib = ssrf.New(ssrf.WithPorts(lp...), ssrf.WithDeniedV4Prefixes(v4...), ssrf.WithDeniedV6Prefixes(v6...))
	return g, nil
}

// CheckIP reports whether a (normalized) IP is allowed.
func (g *Guard) CheckIP(a netip.Addr) error {
	if !a.IsValid() {
		return blocked(ReasonSSRF, "invalid ip")
	}
	raw := a
	a = a.Unmap()
	for _, x := range []netip.Addr{raw, a} {
		for _, p := range g.deny {
			if p.Contains(x) {
				return blocked(ReasonSSRF, "denied range %s", p)
			}
		}
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return blocked(ReasonSSRF, "non-public ip")
	}
	return nil
}

// CheckPort enforces allowed ports and the permanent internal-port denylist.
func (g *Guard) CheckPort(p int) error {
	if DeniedPorts[p] || !g.ports[p] {
		return blocked(ReasonPort, "port %d not allowed", p)
	}
	return nil
}

// Control is the authoritative check, run by net.Dialer for every connect
// attempt with the literal IP:port being dialed.
func (g *Guard) Control(network, address string, c syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" {
		return blocked(ReasonSSRF, "network %s not allowed", network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return blocked(ReasonSSRF, "unparsable address")
	}
	if err := g.CheckIP(ap.Addr()); err != nil {
		return err
	}
	if err := g.CheckPort(int(ap.Port())); err != nil {
		return err
	}
	// Second opinion from code.dny.dev/ssrf (default deny lists + ours).
	if err := g.lib.Safe(network, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String(), c); err != nil {
		return blocked(ReasonSSRF, "%v", err)
	}
	return nil
}

var deniedSuffixes = []string{".ts.net", ".local", ".localhost", ".internal", ".home.arpa", ".localdomain", ".lan", ".intranet", ".corp"}

const (
	MaxQueryLen = 256
	MaxPathLen  = 1024
	MaxURLLen   = 2048
)

// CheckURL validates a URL before any network activity and returns the
// normalized host and port. It rejects literal IPs that are not public,
// numeric/hex/octal host spellings, single-label names and internal suffixes.
func (g *Guard) CheckURL(u *url.URL) (host string, port int, err error) {
	if len(u.String()) > MaxURLLen {
		return "", 0, blocked(ReasonURL, "url too long")
	}
	s := strings.ToLower(u.Scheme)
	if s != "http" && s != "https" {
		return "", 0, blocked(ReasonScheme, "scheme %q", u.Scheme)
	}
	if u.User != nil {
		return "", 0, blocked(ReasonURL, "userinfo not allowed")
	}
	if u.Opaque != "" {
		return "", 0, blocked(ReasonURL, "opaque url")
	}
	if len(u.RawQuery) > MaxQueryLen {
		return "", 0, blocked(ReasonURL, "query string > %d", MaxQueryLen)
	}
	if len(u.EscapedPath()) > MaxPathLen {
		return "", 0, blocked(ReasonURL, "path > %d", MaxPathLen)
	}
	port = 80
	if s == "https" {
		port = 443
	}
	if ps := u.Port(); ps != "" {
		p, perr := strconv.Atoi(ps)
		if perr != nil {
			return "", 0, blocked(ReasonPort, "bad port")
		}
		port = p
	}
	if err := g.CheckPort(port); err != nil {
		return "", 0, err
	}
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", 0, blocked(ReasonHost, "empty host")
	}
	if strings.ContainsAny(host, "%\\@ ") {
		return "", 0, blocked(ReasonHost, "bad host characters")
	}
	if a, perr := netip.ParseAddr(host); perr == nil {
		return host, port, g.CheckIP(a)
	}
	if strings.Contains(host, ":") || strings.HasPrefix(u.Host, "[") {
		return "", 0, blocked(ReasonHost, "malformed ip literal")
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || strings.HasSuffix(name, ".") {
		return "", 0, blocked(ReasonHost, "empty label")
	}
	if name == "localhost" {
		return "", 0, blocked(ReasonHost, "localhost")
	}
	for _, suf := range deniedSuffixes {
		if strings.HasSuffix(name, suf) || name == suf[1:] {
			return "", 0, blocked(ReasonHost, "internal suffix %s", suf)
		}
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", 0, blocked(ReasonHost, "single-label host")
	}
	if numericHost(labels) {
		return "", 0, blocked(ReasonHost, "numeric host")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", 0, blocked(ReasonHost, "bad label")
		}
	}
	return name, port, nil
}

// numericHost catches inet_aton-style spellings (2130706433, 0x7f000001,
// 0177.0.0.1, 127.1) and any name whose final label is all digits/hex-prefixed,
// which no real TLD is.
func numericHost(labels []string) bool {
	last := labels[len(labels)-1]
	if isNumLabel(last) {
		return true
	}
	all := true
	for _, l := range labels {
		if !isNumLabel(l) {
			all = false
		}
	}
	return all
}

func isNumLabel(l string) bool {
	if l == "" {
		return false
	}
	if strings.HasPrefix(l, "0x") {
		l = l[2:]
		if l == "" {
			return true
		}
		for _, c := range l {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	}
	for _, c := range l {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// splitHostPort helper for dialing.
func splitHostPort(addr string) (string, int, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(p)
	return h, n, err
}
