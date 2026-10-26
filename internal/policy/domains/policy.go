// Package domains is the single gateway-side web egress policy layer
// (docs/tool-platform.md §13). It decides, for search result URLs, fetch URLs
// and every redirect hop, whether a host may be reached. The SSRF dialer in
// internal/webfetch stays underneath as the dial-time check.
//
// Fail-closed: if the policy cannot be loaded (bad file, unknown field,
// unverifiable malware snapshot with no verified fallback) callers get an
// error and must disable web search and fetch.
package domains

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"gopkg.in/yaml.v3"
)

// Block reasons (stable values; also metric labels and tool_calls.error text).
const (
	ReasonDomainDenied    = "domain_denied"
	ReasonIPLiteral       = "ip_literal"
	ReasonIPLiteralPriv   = "ip_literal_private"
	ReasonInvalidHost     = "invalid_host"
	ReasonNotAllowlisted  = "not_allowlisted"
	ReasonPolicyUnloaded  = "policy_unavailable"
	ModeDenylist          = "denylist"
	ModeAllowlistOnly     = "allowlist_only"
	MaxSnapshotAge        = 30 * 24 * time.Hour
	categoryMalware       = "malware"
	malwareKeyFile        = "keys/malware-ed25519.pub"
	malwareCurrent        = "categories/malware.txt"
	malwarePrevious       = "categories/malware.prev.txt"
	signatureSuffix       = ".sig"
	defaultPolicyFileName = "web-domains.yaml"
)

// KnownCategories are the category files under categories/.
var KnownCategories = []string{"shortener", "tunnel", "paste", "wildcard_dns", categoryMalware}

// File is the schema of web-domains.yaml. Unknown fields are rejected.
type File struct {
	Version        int      `yaml:"version"`
	Mode           string   `yaml:"mode"`
	DenyCategories []string `yaml:"deny_categories"`
	Deny           []string `yaml:"deny"`
	Allow          []string `yaml:"allow"`
}

// Decision is the result of a check. RuleID identifies the matching rule for
// the audit log only; it is never sent to the model.
type Decision struct {
	Blocked bool
	Reason  string
	RuleID  string
	Host    string // normalized host (audit hashes it; never sent to the model)
}

type ruleSet struct {
	exact    map[string]string // host -> rule id
	suffix   map[string]string // "x" for pattern "*.x" -> rule id
	prefixes []prefixRule      // "paste.*"
}

type prefixRule struct{ label, id string }

func newRuleSet() *ruleSet {
	return &ruleSet{exact: map[string]string{}, suffix: map[string]string{}}
}

func (r *ruleSet) add(pattern, id string) error {
	p := strings.ToLower(strings.TrimSpace(pattern))
	switch {
	case strings.HasPrefix(p, "*."):
		h, err := NormalizeHost(p[2:])
		if err != nil {
			return fmt.Errorf("%s: %q: %w", id, pattern, err)
		}
		r.suffix[h] = id
	case strings.HasSuffix(p, ".*"):
		l := strings.TrimSuffix(p, ".*")
		if l == "" || strings.ContainsAny(l, ".*") {
			return fmt.Errorf("%s: bad prefix pattern %q", id, pattern)
		}
		r.prefixes = append(r.prefixes, prefixRule{l, id})
	default:
		if strings.Contains(p, "*") {
			return fmt.Errorf("%s: wildcard only allowed as leading '*.' or trailing '.*': %q", id, pattern)
		}
		h, err := NormalizeHost(p)
		if err != nil {
			return fmt.Errorf("%s: %q: %w", id, pattern, err)
		}
		r.exact[h] = id
	}
	return nil
}

// match: exact host, or any proper parent domain listed as "*.parent"
// (subdomains only; "*.x" does not match "x" itself), or "label.*".
func (r *ruleSet) match(host string) (string, bool) {
	if id, ok := r.exact[host]; ok {
		return id, true
	}
	h := host
	for {
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
		if id, ok := r.suffix[h]; ok {
			return id, true
		}
	}
	first, _, _ := strings.Cut(host, ".")
	if strings.Contains(host, ".") {
		for _, p := range r.prefixes {
			if p.label == first {
				return p.id, true
			}
		}
	}
	return "", false
}

// Policy is immutable after Load.
type Policy struct {
	mode  string
	deny  *ruleSet
	allow *ruleSet
	// SnapshotGenerated is the malware snapshot's generation time (zero if the
	// malware category is not enabled).
	SnapshotGenerated time.Time
	// SnapshotFallback is true when the current snapshot failed verification
	// and the previous verified one is in use.
	SnapshotFallback bool
	// Warnings are non-fatal load notes (fallback, stale snapshot).
	Warnings []string
}

var profile = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false), idna.StrictDomainName(false))

// NormalizeHost lowercases, IDNA-encodes (Lookup profile), strips a trailing
// dot and a port. IP literals are returned unchanged (callers classify them
// with ParseIPLiteral first). IDNA failures are errors (the host is rejected).
func NormalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", errors.New("empty host")
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i > 0 {
			return strings.ToLower(h[1:i]), nil
		}
		return "", errors.New("bad bracketed host")
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	a, err := profile.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("idna: %w", err)
	}
	a = strings.ToLower(a)
	if a == "" || strings.HasPrefix(a, ".") || strings.Contains(a, "..") {
		return "", errors.New("empty label")
	}
	for _, l := range strings.Split(a, ".") {
		if len(l) > 63 {
			return "", errors.New("label too long")
		}
	}
	return a, nil
}

// Load reads web-domains.yaml and the category files from fsys. now is used
// for the snapshot age warning (never to stop enforcing).
func Load(fsys fs.FS, now time.Time) (*Policy, error) {
	raw, err := fs.ReadFile(fsys, defaultPolicyFileName)
	if err != nil {
		return nil, fmt.Errorf("domain policy: %w", err)
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("domain policy: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("domain policy: unsupported version %d", f.Version)
	}
	if f.Mode != ModeDenylist && f.Mode != ModeAllowlistOnly {
		return nil, fmt.Errorf("domain policy: invalid mode %q", f.Mode)
	}
	p := &Policy{mode: f.Mode, deny: newRuleSet(), allow: newRuleSet()}
	seen := map[string]bool{}
	for _, c := range f.DenyCategories {
		if !contains(KnownCategories, c) {
			return nil, fmt.Errorf("domain policy: unknown category %q", c)
		}
		if seen[c] {
			return nil, fmt.Errorf("domain policy: duplicate category %q", c)
		}
		seen[c] = true
		var body []byte
		if c == categoryMalware {
			s, err := loadMalwareSnapshot(fsys, now)
			if err != nil {
				return nil, err
			}
			body, p.SnapshotGenerated, p.SnapshotFallback = s.body, s.generated, s.fallback
			p.Warnings = append(p.Warnings, s.warnings...)
		} else {
			body, err = fs.ReadFile(fsys, "categories/"+c+".txt")
			if err != nil {
				return nil, fmt.Errorf("domain policy: category %s: %w", c, err)
			}
		}
		if err := addLines(p.deny, c, body); err != nil {
			return nil, fmt.Errorf("domain policy: %w", err)
		}
	}
	for i, d := range f.Deny {
		if err := p.deny.add(d, fmt.Sprintf("deny:%d", i)); err != nil {
			return nil, fmt.Errorf("domain policy: %w", err)
		}
	}
	for i, a := range f.Allow {
		if err := p.allow.add(a, fmt.Sprintf("allow:%d", i)); err != nil {
			return nil, fmt.Errorf("domain policy: %w", err)
		}
	}
	return p, nil
}

func addLines(r *ruleSet, cat string, body []byte) error {
	for n, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := r.add(line, fmt.Sprintf("%s:%d", cat, n+1)); err != nil {
			return err
		}
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// CheckHost decides for a bare host (no scheme).
func (p *Policy) CheckHost(host string) Decision {
	if p == nil {
		return Decision{Blocked: true, Reason: ReasonPolicyUnloaded}
	}
	if a, err := ParseIPLiteral(host); err == nil {
		if IsNonPublic(a) {
			return Decision{Blocked: true, Reason: ReasonIPLiteralPriv, RuleID: "ip_literal_private", Host: a.String()}
		}
		return Decision{Blocked: true, Reason: ReasonIPLiteral, RuleID: "ip_literal", Host: a.String()}
	} else if errors.Is(err, errBadNumeric) {
		return Decision{Blocked: true, Reason: ReasonInvalidHost, RuleID: "numeric_invalid", Host: strings.ToLower(host)}
	}
	h, err := NormalizeHost(host)
	if err != nil {
		return Decision{Blocked: true, Reason: ReasonInvalidHost, RuleID: "idna_invalid", Host: strings.ToLower(host)}
	}
	// Names that normalize into an IP spelling (e.g. full-width digits).
	if a, err := ParseIPLiteral(h); err == nil {
		r := ReasonIPLiteral
		if IsNonPublic(a) {
			r = ReasonIPLiteralPriv
		}
		return Decision{Blocked: true, Reason: r, RuleID: r, Host: a.String()}
	} else if errors.Is(err, errBadNumeric) {
		return Decision{Blocked: true, Reason: ReasonInvalidHost, RuleID: "numeric_invalid", Host: h}
	}
	if id, ok := p.deny.match(h); ok { // deny wins over allow
		return Decision{Blocked: true, Reason: ReasonDomainDenied, RuleID: id, Host: h}
	}
	if p.mode == ModeAllowlistOnly {
		if _, ok := p.allow.match(h); !ok {
			return Decision{Blocked: true, Reason: ReasonNotAllowlisted, RuleID: "allowlist_only", Host: h}
		}
	}
	return Decision{Host: h}
}

// CheckURL decides for an absolute http(s) URL.
func (p *Policy) CheckURL(raw string) Decision {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return Decision{Blocked: true, Reason: ReasonInvalidHost, RuleID: "unparsable_url"}
	}
	return p.CheckParsed(u)
}

// CheckParsed decides for a parsed URL (used for redirect hops).
func (p *Policy) CheckParsed(u *url.URL) Decision {
	h := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		h = "[" + h + "]"
	}
	if h == "" {
		return Decision{Blocked: true, Reason: ReasonInvalidHost, RuleID: "empty_host"}
	}
	return p.CheckHost(h)
}

// RuleCount returns (deny, allow) rule counts (for the startup log).
func (p *Policy) RuleCount() (int, int) {
	c := func(r *ruleSet) int { return len(r.exact) + len(r.suffix) + len(r.prefixes) }
	return c(p.deny), c(p.allow)
}

// Categories returns the enabled categories in sorted order (for logs only).
func Categories(f File) []string {
	out := append([]string(nil), f.DenyCategories...)
	sort.Strings(out)
	return out
}
