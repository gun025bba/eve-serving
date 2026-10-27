package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// ProviderAPIKey authenticates "Authorization: Bearer <key>" (scripts, server-to-server).
const ProviderAPIKey = "api-key"

func init() {
	Register(ProviderAPIKey, func(o Options) (Authenticator, error) {
		if o.APIKey == "" {
			return nil, fmt.Errorf("empty api key")
		}
		return APIKey{Key: o.APIKey}, nil
	})
}

type APIKey struct{ Key string }

func (a APIKey) Authenticate(r *http.Request) (Identity, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Identity{}, ErrNoCredentials
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return Identity{}, ErrInvalidKey
	}
	tok = strings.TrimSpace(tok)
	// Compare fixed-length hashes so neither content nor length leaks via timing.
	got, want := sha256.Sum256([]byte(tok)), sha256.Sum256([]byte(a.Key))
	if a.Key == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return Identity{}, ErrInvalidKey
	}
	return Identity{User: "api-key", Provider: ProviderAPIKey}, nil
}

// Check reports whether r carries "Authorization: Bearer <key>".
func Check(r *http.Request, key string) bool {
	_, err := APIKey{Key: key}.Authenticate(r)
	return err == nil
}
