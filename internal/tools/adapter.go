package tools

import (
	"context"
	"sync"
)

// Adapter executes one tool. Args are already JSON-decoded. The builtin web
// adapters delegate to the existing Session logic unchanged (§8).
type Adapter interface {
	Name() string
	Call(ctx context.Context, s *Session, c *Call, args map[string]any) string
}

type adapterFunc struct {
	name string
	fn   func(s *Session, ctx context.Context, c *Call, a map[string]any) string
}

func (a adapterFunc) Name() string { return a.name }
func (a adapterFunc) Call(ctx context.Context, s *Session, c *Call, args map[string]any) string {
	return a.fn(s, ctx, c, args)
}

var builtinAdapters = map[string]Adapter{
	"web_search": adapterFunc{"web_search", (*Session).search},
	"web_fetch":  adapterFunc{"web_fetch", (*Session).fetch},
}

// Platform bundles the registry, adapters and kill switch.
type Platform struct {
	Reg      *Registry
	Adapters map[string]Adapter
	Kill     *Killswitch
	Redact   *Redactor
}

var (
	defaultOnce sync.Once
	defaultP    *Platform
)

// Default returns the builtin platform. It panics if the embedded manifests are
// invalid, which is caught by tests at build time.
func Default() *Platform {
	defaultOnce.Do(func() {
		reg, err := LoadBuiltin()
		if err != nil {
			panic("tools: builtin manifests invalid: " + err.Error())
		}
		for _, n := range reg.Names() {
			if _, ok := builtinAdapters[n]; !ok {
				panic("tools: no adapter for " + n)
			}
		}
		defaultP = &Platform{Reg: reg, Adapters: builtinAdapters, Redact: &Redactor{}}
	})
	return defaultP
}

// legacyOrder is the tool order of the pre-registry toolJSON (kept for byte
// identity of the upstream request body).
var legacyOrder = []string{"web_search", "web_fetch"}

// Exposed returns the tool names offered to the model for this request: all
// registered tools minus those switched off, in legacy order.
func (p *Platform) Exposed() []string {
	var out []string
	for _, n := range legacyOrder {
		if _, ok := p.Reg.Get(n); ok && !p.Kill.Disabled(n) {
			out = append(out, n)
		}
	}
	return out
}

// IsExposed reports whether name is in the exposed set.
func (p *Platform) IsExposed(name string) bool {
	for _, n := range p.Exposed() {
		if n == name {
			return true
		}
	}
	return false
}

// SetKillswitch installs the kill switch on the default platform (startup only).
func SetKillswitch(k *Killswitch) { Default().Kill = k }

// exec is the M1 pipeline: call budget -> registry -> kill switch -> adapter.
// Scope/taint/audit stages arrive in later milestones (M2/M3).
func (p *Platform) exec(ctx context.Context, s *Session, c *Call, name string, args map[string]any) string {
	if !s.CallsLeft() {
		c.Status, c.Error, c.BlockReason = "blocked", "tool call limit reached", ReasonLimit
		return ""
	}
	a, ok := p.Adapters[name]
	if _, reg := p.Reg.Get(name); !ok || !reg {
		c.Status, c.Error = "error", "unknown tool"
		return ""
	}
	if p.Kill.Disabled(name) {
		c.Status, c.Error, c.BlockReason = "blocked", "tool disabled", ReasonDisabled
		return ""
	}
	if s.d.OnStart != nil {
		s.d.OnStart(*c)
	}
	return a.Call(ctx, s, c, args)
}

// Constant block reasons (metric label eve_tool_denied_total{reason}); the
// domain policy reasons live in internal/policy/domains.
const (
	ReasonLimit    = "limit"
	ReasonRate     = "rate_limited"
	ReasonQuery    = "query_rejected"
	ReasonDisabled = "tool_disabled"
)
