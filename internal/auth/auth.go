// Package auth implements pluggable request authentication.
//
// Each provider lives in its own file, implements Authenticator, and registers
// a constructor by name in init(). config auth.providers picks which providers
// run and in what order. Handlers only read the resulting Identity via
// FromContext and never know which provider authenticated the request.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// Identity is an authenticated caller. It never contains secrets.
type Identity struct {
	User     string `json:"user"`     // users.login of a paired session; "api-key" for key callers
	Provider string `json:"provider"` // registered provider name
	// SessionID is the sessions.id for ProviderSession (0 otherwise).
	SessionID int64 `json:"-"`
}

var (
	// ErrNoCredentials: nothing this provider handles; the chain tries the next one.
	ErrNoCredentials = errors.New("no credentials")
	// ErrInvalidKey: a Bearer token was presented but is wrong.
	ErrInvalidKey = errors.New("invalid api key")
	// ErrForbiddenIdentity: an identity was presented but is not allowed.
	ErrForbiddenIdentity = errors.New("identity not allowed")
	// ErrInvalidSession: a session cookie was presented but is unknown, expired or revoked.
	ErrInvalidSession = errors.New("invalid session")
)

// Authenticator authenticates a request.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// Options carries everything constructors may need.
type Options struct {
	APIKey       string
	AllowedUsers []string
	TestUsers    []string         // e2e identity injection allowlist (-tags e2e only)
	Sessions     SessionValidator // paired browser sessions; nil = none accepted
}

// Constructor builds a provider from Options.
type Constructor func(Options) (Authenticator, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Constructor{}
)

// Register adds a provider constructor. Call from init().
func Register(name string, c Constructor) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("auth: duplicate provider " + name)
	}
	registry[name] = c
}

// Providers lists registered provider names.
func Providers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build constructs a Chain from provider names in order.
func Build(names []string, o Options) (Chain, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("auth: no providers configured")
	}
	regMu.RLock()
	defer regMu.RUnlock()
	var c Chain
	for _, n := range names {
		ctor, ok := registry[n]
		if !ok {
			return nil, fmt.Errorf("auth: unknown provider %q (known: %s)", n, strings.Join(keys(), ", "))
		}
		a, err := ctor(o)
		if err != nil {
			return nil, fmt.Errorf("auth: %s: %w", n, err)
		}
		c = append(c, a)
	}
	return c, nil
}

func keys() []string {
	out := []string{}
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Chain tries each authenticator in order; the first success wins.
// ErrNoCredentials means "not mine" and moves on to the next provider.
// ErrForbiddenIdentity and ErrInvalidKey are definitive rejections: the chain
// stops immediately and never falls through, so e.g. a refused identity
// header cannot be downgraded to the api-key user by a valid Bearer key.
// Any other provider error also stops the chain (fail closed).
type Chain []Authenticator

func (c Chain) Authenticate(r *http.Request) (Identity, error) {
	for _, a := range c {
		id, err := a.Authenticate(r)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrNoCredentials) {
			return Identity{}, err
		}
	}
	return Identity{}, ErrNoCredentials
}

// RejectReason maps a chain error to a short, secret-free log reason.
func RejectReason(err error) string {
	switch {
	case errors.Is(err, ErrForbiddenIdentity):
		return "forbidden_identity"
	case errors.Is(err, ErrInvalidKey):
		return "invalid_key"
	case errors.Is(err, ErrNoCredentials):
		return "no_credentials"
	default:
		return "provider_error"
	}
}

type ctxKey struct{}

// WithIdentity stores id in ctx.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the request's Identity (ok=false if unauthenticated).
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// LoadKey reads and trims the key file. The key is never logged. Startup is
// refused (fail-closed) unless the file is a regular file with mode 0600 or
// stricter.
func LoadKey(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("api key file %s must be a regular file (not a symlink) with mode 0600 (got %#o)", path, st.Mode().Perm())
	}
	if !ownedBySelf(st) {
		return "", fmt.Errorf("api key file %s must be owned by the gateway's own uid", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("api key file %s is empty", path)
	}
	return k, nil
}
