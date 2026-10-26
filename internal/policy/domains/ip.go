package domains

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// errNotIP means the host is a name, not an IP literal.
var errNotIP = errors.New("not an ip literal")

// errBadNumeric means the host looks numeric (WHATWG "ends in a number") but
// is not a valid IPv4 address. Such hosts are rejected.
var errBadNumeric = errors.New("invalid numeric host")

// ParseIPLiteral converts every host spelling that a browser (WHATWG URL host
// parser) would treat as an IP address into its netip form: dotted decimal,
// decimal integer (2130706433), octal (0177.0.0.1), hex (0x7f.1, 0x7f000001),
// short forms (127.1, 10.1), mixed forms, bracketed or bare IPv6 incl. zone
// IDs (%25 / %) and IPv4-mapped/compatible forms. It returns errNotIP for
// ordinary names and errBadNumeric for numeric-looking hosts that are invalid.
func ParseIPLiteral(host string) (netip.Addr, error) {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if strings.Contains(h, ":") {
		if i := strings.Index(h, "%25"); i >= 0 {
			h = h[:i]
		} else if i := strings.IndexByte(h, '%'); i >= 0 {
			h = h[:i]
		}
		a, err := netip.ParseAddr(h)
		if err != nil {
			return netip.Addr{}, errBadNumeric
		}
		return a, nil
	}
	h = strings.TrimSuffix(h, ".")
	parts := strings.Split(h, ".")
	if !isNumberLabel(parts[len(parts)-1]) {
		return netip.Addr{}, errNotIP
	}
	if len(parts) > 4 {
		return netip.Addr{}, errBadNumeric
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseIPv4Number(p)
		if !ok {
			return netip.Addr{}, errBadNumeric
		}
		nums[i] = n
	}
	for _, n := range nums[:len(nums)-1] {
		if n > 255 {
			return netip.Addr{}, errBadNumeric
		}
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return netip.Addr{}, errBadNumeric
	}
	v := last
	for i, n := range nums[:len(nums)-1] {
		v += n << (8 * (3 - i))
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), nil
}

// isNumberLabel is the WHATWG "ends in a number" check for the last label.
func isNumberLabel(l string) bool {
	if l == "" {
		return false
	}
	if allDigits(l) {
		return true
	}
	if strings.HasPrefix(l, "0x") {
		for _, c := range l[2:] {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	}
	return false
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// parseIPv4Number implements the WHATWG IPv4 number parser.
func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	switch {
	case strings.HasPrefix(s, "0x"):
		s, base = s[2:], 16
		if s == "" {
			return 0, true
		}
	case len(s) > 1 && s[0] == '0':
		s, base = s[1:], 8
	}
	n, err := strconv.ParseUint(s, base, 64)
	if err != nil || n > 0xffffffff {
		return 0, false
	}
	return n, true
}

var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "fd7a:115c:a1e0::/48",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// embeddedV4 returns the IPv4 address carried by mapped, compatible and NAT64
// IPv6 forms.
func embeddedV4(a netip.Addr) (netip.Addr, bool) {
	if a.Is4In6() {
		return a.Unmap(), true
	}
	if a.Is6() {
		b := a.As16()
		if nat64.Contains(a) || (b == [16]byte{12: b[12], 13: b[13], 14: b[14], 15: b[15]} && (b[12]|b[13]) != 0) {
			return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
		}
	}
	return a, false
}

// IsNonPublic reports loopback, RFC 1918, link-local, CGNAT/tailnet, ULA,
// unspecified, multicast and broadcast addresses, looking through mapped,
// compatible and NAT64 IPv6 forms.
func IsNonPublic(a netip.Addr) bool {
	cands := []netip.Addr{a}
	if v4, ok := embeddedV4(a); ok {
		cands = append(cands, v4)
	}
	for _, x := range cands {
		if x.IsLoopback() || x.IsPrivate() || x.IsLinkLocalUnicast() || x.IsLinkLocalMulticast() || x.IsMulticast() || x.IsUnspecified() || x.IsInterfaceLocalMulticast() {
			return true
		}
		for _, p := range nonPublic {
			if p.Contains(x) {
				return true
			}
		}
	}
	return false
}
