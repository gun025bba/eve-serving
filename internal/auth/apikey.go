package auth

import (
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
	vals, present := r.Header["Authorization"]
	if !present {
		return Identity{}, ErrNoCredentials
	}
	// Present but empty/whitespace, or sent more than once: reject, never
	// treat as "no credentials" (that would fall through the chain).
	if len(vals) != 1 || strings.TrimSpace(vals[0]) == "" {
		return Identity{}, ErrInvalidKey
	}
	h := vals[0]
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return Identity{}, ErrInvalidKey
	}
	tok = strings.TrimSpace(tok)
	if a.Key == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(a.Key)) != 1 {
		return Identity{}, ErrInvalidKey
	}
	return Identity{User: "api-key", Provider: ProviderAPIKey}, nil
}

// Check reports whether r carries "Authorization: Bearer <key>".
func Check(r *http.Request, key string) bool {
	_, err := APIKey{Key: key}.Authenticate(r)
	return err == nil
}
