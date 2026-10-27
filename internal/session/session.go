// Package session implements 4.0.0 browser identity: one-time pairing codes
// issued on the host (`eve-gw pair --user <login>`) and redeemed at
// POST /v1/pair for a 256-bit session cookie (docs/architecture/identity-transport.md).
//
// Only HMAC-SHA256(pepper, value) of codes and tokens is ever stored. The
// pepper lives in a 0600 file outside the repository; rotating it invalidates
// every session and outstanding code.
package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	CookieName  = "eve_session"
	CodeTTL     = 10 * time.Minute
	IdleTTL     = 30 * 24 * time.Hour
	AbsoluteTTL = 180 * 24 * time.Hour
	MaxFailures = 5
	LockFor     = 10 * time.Minute
	// touchEvery bounds last_seen writes (idle expiry is in days).
	touchEvery = 5 * time.Minute
)

var (
	ErrNotFound = errors.New("session: not found")
	// ErrPairFailed is the only failure POST /v1/pair reports (wrong,
	// expired, used, or locked out are indistinguishable to the client).
	ErrPairFailed = errors.New("pairing failed")
)

// Session is a stored browser session (no token, only its hash).
type Session struct {
	ID        int64
	UserID    int64
	Login     string
	CreatedAt time.Time
	LastSeen  time.Time
	RevokedAt *time.Time
}

// Store persists codes and sessions. Implemented by postgres.Store and Memory.
type Store interface {
	// UserIDByLogin returns the existing users.id for login, creating the row
	// if absent (pairing an allowlisted login that never logged in).
	UserIDByLogin(ctx context.Context, login string) (int64, error)
	PutPairCode(ctx context.Context, userID int64, codeHash string, expires time.Time) error
	// TakePairCode deletes the code (one-time) and returns its user if it
	// existed and had not expired at now.
	TakePairCode(ctx context.Context, codeHash string, now time.Time) (userID int64, ok bool, err error)
	DeleteAllPairCodes(ctx context.Context) error
	CreateSession(ctx context.Context, userID int64, tokenHash string, now time.Time) (int64, error)
	SessionByHash(ctx context.Context, tokenHash string) (Session, error)
	TouchSession(ctx context.Context, id int64, now time.Time) error
	RevokeSession(ctx context.Context, id int64, now time.Time) error
	ListSessions(ctx context.Context) ([]Session, error)
}

// Manager hashes, issues and validates codes and sessions.
type Manager struct {
	Store  Store
	pepper []byte
	Now    func() time.Time

	mu       sync.Mutex
	failures int
	lockedTo time.Time
}

// New builds a Manager. pepper must be at least 32 bytes.
func New(st Store, pepper []byte) (*Manager, error) {
	if len(pepper) < 32 {
		return nil, fmt.Errorf("session pepper must be at least 32 bytes")
	}
	return &Manager{Store: st, pepper: pepper, Now: time.Now}, nil
}

func (m *Manager) hash(kind, v string) string {
	h := hmac.New(sha256.New, m.pepper)
	h.Write([]byte(kind + ":" + v))
	return hex.EncodeToString(h.Sum(nil))
}

// Crockford base32 alphabet (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewCode returns a CSPRNG 8-char (40-bit) Crockford base32 code as XXXX-XXXX.
func NewCode() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = crockford[v&31]
		v >>= 5
	}
	return string(out[:4]) + "-" + string(out[4:]), nil
}

// NormalizeCode upper-cases, drops separators and maps Crockford aliases
// (O->0, I/L->1). ok=false if it cannot be a valid code.
func NormalizeCode(s string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(crockford, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != 8 {
		return "", false
	}
	return b.String(), true
}

// IssueCode creates a pairing code for an existing (or new) users row.
func (m *Manager) IssueCode(ctx context.Context, login string) (string, error) {
	uid, err := m.Store.UserIDByLogin(ctx, login)
	if err != nil {
		return "", err
	}
	code, err := NewCode()
	if err != nil {
		return "", err
	}
	n, _ := NormalizeCode(code)
	if err := m.Store.PutPairCode(ctx, uid, m.hash("code", n), m.Now().Add(CodeTTL)); err != nil {
		return "", err
	}
	return code, nil
}

// Pair redeems a code and returns a new session token. Every failure is
// ErrPairFailed. After MaxFailures failures pairing is locked for LockFor and
// all outstanding codes are invalidated. The counter is per process.
func (m *Manager) Pair(ctx context.Context, code string) (token string, s Session, err error) {
	now := m.Now()
	m.mu.Lock()
	if now.Before(m.lockedTo) {
		m.mu.Unlock()
		return "", Session{}, ErrPairFailed
	}
	m.mu.Unlock()
	n, ok := NormalizeCode(code)
	var uid int64
	if ok {
		uid, ok, err = m.Store.TakePairCode(ctx, m.hash("code", n), now)
		if err != nil {
			return "", Session{}, err
		}
	}
	if !ok {
		m.fail(ctx, now)
		return "", Session{}, ErrPairFailed
	}
	m.mu.Lock()
	m.failures = 0
	m.mu.Unlock()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	id, err := m.Store.CreateSession(ctx, uid, m.hash("session", token), now)
	if err != nil {
		return "", Session{}, err
	}
	return token, Session{ID: id, UserID: uid, CreatedAt: now, LastSeen: now}, nil
}

func (m *Manager) fail(ctx context.Context, now time.Time) {
	m.mu.Lock()
	m.failures++
	lock := m.failures >= MaxFailures
	if lock {
		m.failures = 0
		m.lockedTo = now.Add(LockFor)
	}
	m.mu.Unlock()
	if lock {
		_ = m.Store.DeleteAllPairCodes(ctx)
	}
}

// Locked reports whether pairing is currently locked out.
func (m *Manager) Locked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Now().Before(m.lockedTo)
}

// Validate resolves a cookie token to a live session (idle 30d, absolute 180d).
func (m *Manager) Validate(ctx context.Context, token string) (Session, error) {
	if token == "" || len(token) > 128 {
		return Session{}, ErrNotFound
	}
	s, err := m.Store.SessionByHash(ctx, m.hash("session", token))
	if err != nil {
		return Session{}, err
	}
	now := m.Now()
	if s.RevokedAt != nil || now.Sub(s.LastSeen) > IdleTTL || now.Sub(s.CreatedAt) > AbsoluteTTL {
		return Session{}, ErrNotFound
	}
	if now.Sub(s.LastSeen) > touchEvery {
		_ = m.Store.TouchSession(ctx, s.ID, now)
	}
	return s, nil
}

// Revoke revokes the session behind token (logout).
func (m *Manager) Revoke(ctx context.Context, token string) error {
	s, err := m.Store.SessionByHash(ctx, m.hash("session", token))
	if err != nil {
		return err
	}
	return m.Store.RevokeSession(ctx, s.ID, m.Now())
}

// LoadSecret reads a secret file, refusing (fail-closed) a missing or empty
// file or one whose mode grants any group/other permission (must be 0600 or
// stricter).
func LoadSecret(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: mode %#o is wider than 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return nil, fmt.Errorf("%s: empty", path)
	}
	return b, nil
}

// Mask returns the first 4 characters followed by "…" for logs.
func Mask(s string) string {
	if len(s) <= 4 {
		return "…"
	}
	return s[:4] + "…"
}
