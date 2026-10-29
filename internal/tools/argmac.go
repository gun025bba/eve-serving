package tools

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ArgMAC computes the argument hash that goes into logs, audit events and
// tool_calls: HMAC-SHA256 under a gateway-only key (design §4.2-3, §6.4),
// truncated to 128 bits. A plain SHA-256 of a short query can be reversed by
// guessing, so there is no unkeyed fallback: without a key the automatic
// selection is off (fail closed).
type ArgMAC struct {
	key []byte
	kid string
}

// MinArgMACKeyBytes is the shortest accepted key.
const MinArgMACKeyBytes = 32

// NewArgMAC builds the hasher. A short key is refused.
func NewArgMAC(key []byte) (*ArgMAC, error) {
	if len(key) < MinArgMACKeyBytes {
		return nil, errors.New("tools: argument hash key too short")
	}
	k := append([]byte(nil), key...)
	// the key id lets a rotated key be told apart without revealing it
	kid := hmac.New(sha256.New, k)
	kid.Write([]byte("eve.argmac.kid.v1"))
	return &ArgMAC{key: k, kid: hex.EncodeToString(kid.Sum(nil))[:8]}, nil
}

// KID is a short, non-secret identifier of the key.
func (m *ArgMAC) KID() string {
	if m == nil {
		return ""
	}
	return m.kid
}

// Sum returns the 32-hex-digit MAC of one argument. The tool name and a
// domain-separation label are part of the input, so one string hashes
// differently for a query and for a URL. A nil receiver (no key) returns "":
// there is never an unkeyed hash.
func (m *ArgMAC) Sum(tool, arg string) string {
	if m == nil {
		return ""
	}
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte("eve.argmac.v1\x00"))
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write([]byte(arg))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
