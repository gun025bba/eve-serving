package memory

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
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
	// MinKeyBytes: minimum key material (decoded hex, or raw bytes).
	MinKeyBytes = 32
	// MinDistinctBytes: minimum distinct byte values in the decoded key.
	MinDistinctBytes = 12
	// repeatBlock: a key whose first 8 bytes simply repeat is rejected.
	repeatBlock = 8
)

var hexKeyRe = regexp.MustCompile(`^(?:[0-9a-fA-F]{2})+$`)

// ParseKey validates and decodes the audit HMAC key file contents.
//
//	(a) ONE trailing "\n" is stripped (`openssl rand -hex 32 > key` is fine);
//	    any other trailing whitespace, CR or a second newline is rejected.
//	(b) Whole content hex -> decoded, needs >= 32 bytes (64+ hex chars);
//	    otherwise the raw bytes need >= 32 (`openssl rand 32 > key`).
//	(c) >= 12 distinct byte values in the decoded key.
//	(d) not all zero, not its first 8 bytes repeated.
//
// Error messages give the reason only, never key material.
func ParseKey(raw []byte) ([]byte, error) {
	b := raw
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 {
		switch b[n-1] {
		case '\n':
			return nil, errors.New("memory: audit key rejected: more than one trailing newline")
		case '\r', ' ', '\t', '\v', '\f':
			return nil, errors.New("memory: audit key rejected: trailing whitespace")
		}
	}
	key := b
	if hexKeyRe.Match(b) {
		d, err := hex.DecodeString(string(b))
		if err != nil {
			return nil, errors.New("memory: audit key rejected: invalid hex")
		}
		key = d
	}
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("memory: audit key rejected: %d bytes (hex decoded or raw), need at least %d", len(key), MinKeyBytes)
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
