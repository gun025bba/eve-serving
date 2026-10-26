// Package search is the web search provider registry. Like auth providers,
// each provider lives in its own package and registers itself in init();
// Config.Providers sets the order, and New returns a provider that tries
// them in order, falling back on error.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Query struct {
	Q         string
	Lang      string // "ko", "en", "" = auto
	TimeRange string // "", "day", "week", "month", "year"
	Max       int
}

type Result struct {
	Title     string     `json:"title"`
	URL       string     `json:"url"`
	Snippet   string     `json:"snippet"`
	Engine    string     `json:"engine,omitempty"`
	Published *time.Time `json:"published,omitempty"`
}

type Provider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Config is the provider-facing slice of gateway config.
type Config struct {
	Providers    []string
	SearxngURL   string
	BraveKeyFile string
	Timeout      time.Duration
	MaxResults   int
	SnippetChars int
	CacheTTL     time.Duration
}

// ErrDisabled means a provider is registered but not configured (e.g. Brave without key).
var ErrDisabled = errors.New("search provider disabled")

type Factory func(cfg Config) (Provider, error)

var (
	mu  sync.RWMutex
	reg = map[string]Factory{}
)

func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	reg[name] = f
}

func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for k := range reg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New builds the chain. Unknown names are an error; disabled providers are skipped.
func New(cfg Config) (Provider, error) {
	if len(cfg.Providers) == 0 {
		cfg.Providers = []string{"searxng"}
	}
	var ps []Provider
	for _, n := range cfg.Providers {
		mu.RLock()
		f, ok := reg[n]
		mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown search provider %q (registered: %s)", n, strings.Join(Registered(), ","))
		}
		p, err := f(cfg)
		if errors.Is(err, ErrDisabled) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("search provider %s: %w", n, err)
		}
		ps = append(ps, p)
	}
	if len(ps) == 0 {
		return nil, errors.New("no search provider enabled")
	}
	c := &Chain{ps: ps, ttl: cfg.CacheTTL, cache: map[string]cacheEntry{}}
	if c.ttl == 0 {
		c.ttl = 15 * time.Minute
	}
	return c, nil
}

// Chain tries providers in order and caches results per query.
type Chain struct {
	ps    []Provider
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]cacheEntry
	// OnError is called for each failed provider attempt (metrics hook).
	OnError func(provider string, err error)
	last    string
}

type cacheEntry struct {
	at  time.Time
	res []Result
	by  string
}

func (c *Chain) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != "" {
		return c.last
	}
	return c.ps[0].Name()
}

// Search tries providers in order (cached 15 min per normalized query).
func (c *Chain) Search(ctx context.Context, q Query) ([]Result, error) {
	k := strings.ToLower(strings.TrimSpace(q.Q)) + "\x00" + q.Lang + "\x00" + q.TimeRange + fmt.Sprint("\x00", q.Max)
	c.mu.Lock()
	if e, ok := c.cache[k]; ok && time.Since(e.at) < c.ttl {
		c.last = e.by
		c.mu.Unlock()
		return e.res, nil
	}
	c.mu.Unlock()
	var errs []error
	for _, p := range c.ps {
		res, err := p.Search(ctx, q)
		if err == nil {
			c.mu.Lock()
			if len(c.cache) > 512 {
				c.cache = map[string]cacheEntry{}
			}
			c.cache[k] = cacheEntry{at: time.Now(), res: res, by: p.Name()}
			c.last = p.Name()
			c.mu.Unlock()
			return res, nil
		}
		if c.OnError != nil {
			c.OnError(p.Name(), err)
		}
		errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
