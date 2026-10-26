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
	h := r.Header.Get("Authorization")
	if h == "" {
		return Identity{}, ErrNoCredentials
	}
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
