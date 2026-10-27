// Package memory is the per-user personalization memory
// (docs/architecture/memory.md): one JSON file per user, a secret filter on
// every write, and a bounded, nonce-delimited prompt block on read.
package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

const (
	SchemaVersion = 1
	MaxTextRunes  = 300
	MaxItems      = 200
	MaxFileBytes  = 64 << 10
	TombstoneTTL  = 8 * 24 * time.Hour // backup retention (7d) + 1 day
	TaskTTL       = 30 * 24 * time.Hour
)

var (
	ErrNotFound     = errors.New("memory: item not found")
	ErrConflict     = errors.New("memory: updated_at mismatch")
	ErrTooMany      = errors.New("memory: item limit reached")
	ErrTooLong      = errors.New("memory: text too long")
	ErrEmpty        = errors.New("memory: text empty")
	ErrFileTooLarge = errors.New("memory: file size limit reached")
	ErrInvalid      = errors.New("memory: invalid field")
	// ErrUnsafeFile: wrong owner/permissions, symlink or user mismatch. The
	// user's memory is then read-only until an operator restores it (§7 S5-R6).
	ErrUnsafeFile = errors.New("memory: unsafe or corrupt memory file")
)

var kinds = map[string]bool{"profile": true, "preference": true, "fact": true, "task": true}

type Source struct {
	ThreadID  string `json:"thread_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

type Item struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Text       string     `json:"text"`
	Pinned     bool       `json:"pinned"`
	Confidence string     `json:"confidence"` // user | confirmed
	Source     *Source    `json:"source,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

type Tombstone struct {
	ID        string    `json:"id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// Doc is the on-disk file. UserID is the Postgres users.id of the owner; the
// file name is that decimal id and nothing else (see fileKey).
type Doc struct {
	SchemaVersion int         `json:"schema_version"`
	UserID        int64       `json:"user_id"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Items         []Item      `json:"items"`
	Tombstones    []Tombstone `json:"tombstones"`
}

func (d *Doc) find(id string) int {
	return slices.IndexFunc(d.Items, func(it Item) bool { return it.ID == id })
}

// Store is the storage boundary (Load/Update); FileStore is the P1
// implementation, a pgStore can replace it later (§9).
type Store interface {
	Load(uid int64) (Doc, error)
	Update(uid int64, fn func(*Doc) error) (Doc, error)
	Purge(uid int64) error
}

type FileStore struct {
	dir   string
	uid   int
	Now   func() time.Time
	mu    sync.Mutex
	locks map[int64]*userLock
}

// Open prepares dir (0700, owned by the gateway account). An existing dir
// with wider permissions or another owner refuses startup (§7).
func Open(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("memory: data dir required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// No symlink anywhere on the path (the dir itself or any parent): the
	// resolved path must equal the cleaned absolute path.
	if real, err := filepath.EvalSymlinks(abs); err != nil || real != filepath.Clean(abs) {
		return nil, errors.New("memory: data dir path contains a symlink")
	}
	dir = abs
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a directory", filepath.Base(dir))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("memory: data dir permissions %o are wider than 0700", fi.Mode().Perm())
	}
	uid := os.Getuid()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
		return nil, errors.New("memory: data dir is not owned by the gateway account")
	}
	if err := checkEntries(dir, uid); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir, uid: uid, Now: time.Now, locks: map[int64]*userLock{}}, nil
}

// checkEntries is the startup permission audit: every entry must be a
// regular file (no symlink/dir) owned by the gateway account with no
// group/other bits, named <users.id>.json|.lock or a leftover .tmp-*.
// Anything else refuses startup rather than being silently "fixed".
// Leftover .tmp-* files (a crash between write and rename) are checked like
// every other entry first, and only then removed.
func checkEntries(dir string, uid int) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var tmps []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, ".tmp-") {
			base, ext := strings.TrimSuffix(n, filepath.Ext(n)), filepath.Ext(n)
			if (ext != ".json" && ext != ".lock") || !validName(base) {
				return fmt.Errorf("memory: unexpected entry in data dir (%d bytes name)", len(n))
			}
		}
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("memory: data file has mode %v, want regular 0600", fi.Mode())
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
			return errors.New("memory: data file is not owned by the gateway account")
		}
		if strings.HasPrefix(n, ".tmp-") {
			tmps = append(tmps, n)
		}
	}
	for _, n := range tmps {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("memory: cannot remove leftover temp file: %w", err)
		}
	}
	if len(tmps) > 0 {
		return syncDir(dir)
	}
	return nil
}

// ErrBadUserID: the id is not a positive users.id. Nothing touches disk.
var ErrBadUserID = errors.New("memory: invalid user id")

// fileKey is the only way a user maps to a file name: the canonical decimal
// form of a positive int64. No login, path separator, dot or sign can reach
// the file system, so traversal is impossible by construction; validName
// re-checks the result anyway (defense in depth).
func fileKey(uid int64) (string, error) {
	if uid <= 0 {
		return "", ErrBadUserID
	}
	k := strconv.FormatInt(uid, 10)
	// users.id is BIGSERIAL: check ^[0-9]+$ (canonical, no leading zero),
	// parse back and re-format, and require the round trip to be identical.
	if !validName(k) {
		return "", ErrBadUserID
	}
	if back, err := strconv.ParseInt(k, 10, 64); err != nil || back != uid || strconv.FormatInt(back, 10) != k {
		return "", ErrBadUserID
	}
	return k, nil
}

func validName(k string) bool {
	if k == "" || len(k) > 19 || k[0] == '0' {
		return false
	}
	for _, c := range k {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// file returns dir/<id><ext> and verifies it stays directly inside dir.
func (s *FileStore) file(uid int64, ext string) (string, error) {
	k, err := fileKey(uid)
	if err != nil {
		return "", err
	}
	p := filepath.Join(s.dir, k+ext)
	if filepath.Dir(p) != filepath.Clean(s.dir) {
		return "", ErrBadUserID
	}
	return p, nil
}

// Dir is the data directory (tests, diagnostics).
func (s *FileStore) Dir() string { return s.dir }

func (s *FileStore) path(uid int64) string { p, _ := s.file(uid, ".json"); return p }

// lockUser takes the per-user in-process mutex. Entries are reference
// counted and removed when the last holder/waiter releases, so the map only
// holds users with an operation in flight (it cannot grow without bound).
func (s *FileStore) lockUser(uid int64) (unlock func()) {
	s.mu.Lock()
	l := s.locks[uid]
	if l == nil {
		l = &userLock{}
		s.locks[uid] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, uid)
		}
		s.mu.Unlock()
	}
}

type userLock struct {
	mu   sync.Mutex
	refs int
}

// lockCount is the number of live per-user lock entries (tests).
func (s *FileStore) lockCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.locks)
}

func empty(uid int64) Doc {
	return Doc{SchemaVersion: SchemaVersion, UserID: uid, Items: []Item{}, Tombstones: []Tombstone{}}
}

// Load reads the user's file. Missing file = empty doc.
func (s *FileStore) Load(uid int64) (Doc, error) {
	p, err := s.file(uid, ".json")
	if err != nil {
		return Doc{}, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return empty(uid), nil
	}
	if err != nil {
		return Doc{}, ErrUnsafeFile // includes ELOOP (symlink)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || fi.Size() > MaxFileBytes {
		return Doc{}, ErrUnsafeFile
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != s.uid {
		return Doc{}, ErrUnsafeFile
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Doc{}, ErrUnsafeFile
	}
	var d Doc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil || d.UserID != uid || d.SchemaVersion != SchemaVersion {
		return Doc{}, ErrUnsafeFile
	}
	if d.Items == nil {
		d.Items = []Item{}
	}
	if d.Tombstones == nil {
		d.Tombstones = []Tombstone{}
	}
	return d, nil
}

// Update runs a read-modify-write under the per-user mutex and an flock, and
// writes atomically (temp file in the same dir, fsync, rename, dir fsync).
func (s *FileStore) Update(uid int64, fn func(*Doc) error) (Doc, error) {
	lp, err := s.file(uid, ".lock")
	if err != nil {
		return Doc{}, err
	}
	unlock := s.lockUser(uid)
	defer unlock()
	lf, err := os.OpenFile(lp, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return Doc{}, ErrUnsafeFile
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return Doc{}, err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	d, err := s.Load(uid)
	if err != nil {
		return Doc{}, err
	}
	if err := fn(&d); err != nil {
		return Doc{}, err
	}
	now := s.Now().UTC()
	d.Tombstones = slices.DeleteFunc(d.Tombstones, func(t Tombstone) bool { return now.Sub(t.DeletedAt) > TombstoneTTL })
	if len(d.Items) > MaxItems {
		return Doc{}, ErrTooMany
	}
	d.UpdatedAt = now
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return Doc{}, err
	}
	if len(b) > MaxFileBytes {
		return Doc{}, ErrFileTooLarge
	}
	if err := s.writeAtomic(s.path(uid), b); err != nil {
		return Doc{}, err
	}
	return d, nil
}

func (s *FileStore) writeAtomic(dst string, b []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Purge deletes the user's file (DELETE /v1/memory). Backups are not part
// of this PR, so there is no key to shred yet.
func (s *FileStore) Purge(uid int64) error {
	if _, err := fileKey(uid); err != nil {
		return err
	}
	lp, err := s.file(uid, ".lock")
	if err != nil {
		return err
	}
	unlock := s.lockUser(uid)
	defer unlock()
	// Same flock as Update, so a purge cannot interleave with another
	// process's read-modify-write. The .lock file itself is kept: removing
	// it while others may hold or wait on it would split the lock.
	lf, err := os.OpenFile(lp, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return ErrUnsafeFile
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	err = os.Remove(s.path(uid))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = syncDir(s.dir)
	}
	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Clean validates and normalizes new item text: normalization first, then
// length and the secret filter on the normalized form (§2-3).
func Clean(text string) (string, error) {
	t := Normalize(text)
	if t == "" {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(t) > MaxTextRunes {
		return "", ErrTooLong
	}
	if err := CheckSecret(t); err != nil {
		return "", err
	}
	if err := CheckSensitive(t); err != nil {
		return "", err
	}
	return t, nil
}

// NewItem is the input for Add.
type NewItem struct {
	Kind       string
	Text       string
	Pinned     *bool
	Confidence string
	Source     *Source
	ExpiresAt  *time.Time
}

// Add appends one item (already validated by Clean inside).
func Add(st Store, uid int64, in NewItem, now time.Time) (Item, error) {
	if !kinds[in.Kind] || (in.Confidence != "user" && in.Confidence != "confirmed") {
		return Item{}, ErrInvalid
	}
	t, err := Clean(in.Text)
	if err != nil {
		return Item{}, err
	}
	now = now.UTC()
	it := Item{ID: ulid.Make().String(), Kind: in.Kind, Text: t, Pinned: in.Kind == "profile", Confidence: in.Confidence,
		Source: in.Source, CreatedAt: now, UpdatedAt: now, ExpiresAt: in.ExpiresAt}
	if in.Pinned != nil {
		it.Pinned = *in.Pinned
	}
	if it.Kind == "task" && it.ExpiresAt == nil {
		e := now.Add(TaskTTL)
		it.ExpiresAt = &e
	}
	_, err = st.Update(uid, func(d *Doc) error {
		if len(d.Items) >= MaxItems {
			return ErrTooMany
		}
		d.Items = append(d.Items, it)
		return nil
	})
	return it, err
}

// Patch edits text/pinned/expires_at. ifUpdated (optional) is the
// updated_at the client last saw; a mismatch is ErrConflict (409).
type Patch struct {
	Text         *string
	Pinned       *bool
	ExpiresAt    *time.Time
	ClearExpires bool
	IfUpdatedAt  *time.Time
}

func Edit(st Store, uid int64, id string, p Patch, now time.Time) (Item, error) {
	var txt string
	if p.Text != nil {
		t, err := Clean(*p.Text)
		if err != nil {
			return Item{}, err
		}
		txt = t
	}
	var out Item
	_, err := st.Update(uid, func(d *Doc) error {
		i := d.find(id)
		if i < 0 {
			return ErrNotFound
		}
		it := &d.Items[i]
		if p.IfUpdatedAt != nil && !p.IfUpdatedAt.Equal(it.UpdatedAt) {
			return ErrConflict
		}
		if p.Text != nil {
			it.Text, it.Confidence = txt, "user"
		}
		if p.Pinned != nil {
			it.Pinned = *p.Pinned
		}
		if p.ClearExpires {
			it.ExpiresAt = nil
		} else if p.ExpiresAt != nil {
			e := p.ExpiresAt.UTC()
			it.ExpiresAt = &e
		}
		it.UpdatedAt = now.UTC()
		out = *it
		return nil
	})
	return out, err
}

// Delete removes ids (all must exist, otherwise nothing is removed and
// ErrNotFound is returned) and records tombstones (id + time, no text).
func Delete(st Store, uid int64, ids []string, now time.Time) error {
	_, err := st.Update(uid, func(d *Doc) error {
		for _, id := range ids {
			if d.find(id) < 0 {
				return ErrNotFound
			}
		}
		for _, id := range ids {
			d.Items = slices.Delete(d.Items, d.find(id), d.find(id)+1)
			d.Tombstones = append(d.Tombstones, Tombstone{ID: id, DeletedAt: now.UTC()})
		}
		return nil
	})
	return err
}

// Live returns non-expired items.
func Live(d Doc, now time.Time) []Item {
	out := make([]Item, 0, len(d.Items))
	for _, it := range d.Items {
		if it.ExpiresAt == nil || it.ExpiresAt.After(now) {
			out = append(out, it)
		}
	}
	return out
}

// SortedIDs returns ids sorted (payload_hash binding for delete cards).
func SortedIDs(ids []string) []string {
	c := slices.Clone(ids)
	sort.Strings(c)
	return slices.Compact(c)
}
