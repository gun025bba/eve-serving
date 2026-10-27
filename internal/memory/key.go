package memory

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// ReadKeyFile reads the audit HMAC key: a regular, non-symlink file with no
// group/other permissions, owned by the gateway account, 32+ bytes.
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
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil || len(b) < 32 {
		return nil, errors.New("memory: audit key must be at least 32 bytes")
	}
	if err := weakKey(b); err != nil {
		return nil, err
	}
	return b, nil
}

// weakKey rejects obviously bad keys: a trailing newline/whitespace (an
// editor or `echo` artifact, and a sign the file is text, not random bytes)
// or too few distinct bytes (e.g. "kkkk…", "abab…").
func weakKey(b []byte) error {
	if n := len(b); n > 0 && (b[n-1] == '\n' || b[n-1] == '\r' || b[n-1] == ' ' || b[n-1] == '\t') {
		return errors.New("memory: audit key ends with whitespace/newline; write raw random bytes")
	}
	seen := map[byte]bool{}
	for _, c := range b {
		seen[c] = true
	}
	if len(seen) < 16 {
		return errors.New("memory: audit key is too repetitive (fewer than 16 distinct bytes)")
	}
	return nil
}
