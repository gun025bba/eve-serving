// Package auth implements Bearer API-key checking.
package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// LoadKey reads and trims the key file. The key is never logged.
func LoadKey(path string) (string, error) {
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

// Check reports whether r carries "Authorization: Bearer <key>".
func Check(r *http.Request, key string) bool {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return false
	}
	tok = strings.TrimSpace(tok)
	return subtle.ConstantTimeCompare([]byte(tok), []byte(key)) == 1
}
