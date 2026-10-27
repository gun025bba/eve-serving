package memory

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// ReadKeyFile reads the audit HMAC key: a regular, non-symlink file with no
// group/other permissions, owned by the gateway account; contents checked by ParseKey.
func ReadKeyFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("memory: audit key file unreadable")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("memory: audit key file must be a 0600 regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return nil, errors.New("memory: audit key file not owned by the gateway account")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, errors.New("memory: audit key file unreadable")
	}
	return ParseKey(raw)
}

const (
	// MinKeyBytes: minimum decoded key length (64 hex chars).
	MinKeyBytes = 32
	// MinDistinctBytes: minimum distinct byte values in the decoded key.
	MinDistinctBytes = 12
	// repeatBlock: a key whose first 8 bytes simply repeat is rejected.
	repeatBlock = 8
)

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// ParseKey validates and decodes the audit HMAC key file contents.
// Generate with: umask 077; openssl rand -hex 32 > key
//
//	(1) Hex only. At most ONE trailing "\n" is removed; any other whitespace
//	    (incl. CR, a second newline, inner spaces), non-hex character or raw
//	    binary is rejected.
//	(2) >= 64 hex chars (>= 32 bytes decoded); on the decoded bytes:
//	    >= 12 distinct values, not all zero, not its first 8 bytes repeated.
//
// Error messages give the reason only, never key material.
func ParseKey(raw []byte) ([]byte, error) {
	b := raw
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	for _, c := range b {
		switch {
		case c == '\n':
			return nil, errors.New("memory: audit key rejected: more than one trailing newline or an inner newline")
		case c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f':
			return nil, errors.New("memory: audit key rejected: contains whitespace")
		case !isHex(c):
			return nil, errors.New("memory: audit key rejected: not hex (raw/binary keys are not accepted; use `openssl rand -hex 32`)")
		}
	}
	if len(b) < 2*MinKeyBytes {
		return nil, fmt.Errorf("memory: audit key rejected: %d hex chars, need at least %d (%d bytes)", len(b), 2*MinKeyBytes, MinKeyBytes)
	}
	if len(b)%2 != 0 {
		return nil, errors.New("memory: audit key rejected: odd number of hex chars")
	}
	key, err := hex.DecodeString(string(b))
	if err != nil {
		return nil, errors.New("memory: audit key rejected: invalid hex")
	}
	allZero := true
	for _, c := range key {
		if c != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, errors.New("memory: audit key rejected: all zero bytes")
	}
	if isRepeat(key, repeatBlock) {
		return nil, errors.New("memory: audit key rejected: repeating pattern (first 8 bytes repeated)")
	}
	seen := map[byte]bool{}
	for _, c := range key {
		seen[c] = true
	}
	if len(seen) < MinDistinctBytes {
		return nil, fmt.Errorf("memory: audit key rejected: only %d distinct bytes, need at least %d", len(seen), MinDistinctBytes)
	}
	return key, nil
}

// isRepeat reports whether b is its first n bytes repeated (last copy may be partial).
func isRepeat(b []byte, n int) bool {
	if len(b) <= n {
		return false
	}
	for i := n; i < len(b); i++ {
		if b[i] != b[i%n] {
			return false
		}
	}
	return true
}
