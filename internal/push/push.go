// Package push validates Web Push subscriptions and builds the fixed push
// payload (docs/architecture/proactive-messaging.md §2.2–§2.4,
// docs/architecture/ui-api-contract.md §3.5.2–§3.5.3). Sending lands later.
package push

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const (
	// AppleHost is the only allowed endpoint host (exact match; adding one is MAJOR).
	AppleHost = "web.push.apple.com"
	// MaxEndpointLen caps the endpoint URL in bytes (code constant).
	MaxEndpointLen = 1024
	// MaxPayloadLen caps the plaintext payload.
	MaxPayloadLen = 256
)

var ErrInvalid = errors.New("invalid push subscription")

// ValidateEndpoint enforces https, port 443, exact host, and no userinfo,
// query or fragment.
func ValidateEndpoint(ep string) error {
	if ep == "" || len(ep) > MaxEndpointLen {
		return ErrInvalid
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(ep, "#") {
		return ErrInvalid
	}
	if u.Hostname() != AppleHost || u.Host != AppleHost && u.Host != AppleHost+":443" {
		return ErrInvalid
	}
	if p := u.Port(); p != "" && p != "443" {
		return ErrInvalid
	}
	return nil
}

func b64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// ValidateKeys checks p256dh is an uncompressed P-256 point on the curve
// (65 bytes, 0x04 prefix) and auth is 16 bytes.
func ValidateKeys(p256dh, auth string) error {
	pk, err := b64(p256dh)
	if err != nil || len(pk) != 65 || pk[0] != 0x04 {
		return ErrInvalid
	}
	if _, err := ecdh.P256().NewPublicKey(pk); err != nil {
		return ErrInvalid
	}
	a, err := b64(auth)
	if err != nil || len(a) != 16 {
		return ErrInvalid
	}
	return nil
}

var ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Payload returns the only push payload shape: {"v":1,"type":"proactive","nid":<message_id>}.
// No content, title or URL is ever included.
func Payload(messageID string) ([]byte, error) {
	if !ulidRe.MatchString(messageID) {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(struct {
		V    int    `json:"v"`
		Type string `json:"type"`
		NID  string `json:"nid"`
	}{1, "proactive", messageID})
	if err != nil || len(b) > MaxPayloadLen {
		return nil, ErrInvalid
	}
	return b, nil
}
