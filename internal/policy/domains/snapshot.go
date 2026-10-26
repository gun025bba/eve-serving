package domains

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Malware snapshot rules (docs/tool-platform.md §13):
//   - categories/malware.txt is an offline snapshot of a public feed, committed
//     with an ed25519 signature (malware.txt.sig) by the pinned key in
//     keys/malware-ed25519.pub. No runtime download.
//   - The signature is verified in CI (cmd/eve-policy verify) and at gateway
//     startup (here).
//   - If the current snapshot fails verification, the previous verified
//     snapshot (malware.prev.txt) is used with a warning. If neither verifies,
//     the policy fails to load (web search and fetch are disabled).
//   - Snapshots older than 30 days produce a warning and a metric here, and
//     fail CI; enforcement continues with the stale list.

type snapshot struct {
	body      []byte
	generated time.Time
	fallback  bool
	warnings  []string
}

// Key is one pinned snapshot-signing key. Rotation (security review of
// PR #13): add the new key as keys/malware-ed25519-<id>.pub, give the old key
// a "# not-after:" date, re-sign with the new key, and delete the old key file
// once the overlap window has passed. Both keys verify during the overlap.
type Key struct {
	ID       string
	Pub      ed25519.PublicKey
	NotAfter time.Time // zero = no expiry
}

// PublicKey decodes a pinned key file: one base64 line, comments allowed,
// optional "# key-id: ID" and "# not-after: RFC3339" headers.
func PublicKey(raw []byte) (Key, error) {
	var k Key
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if c, ok := strings.CutPrefix(l, "#"); ok {
			c = strings.TrimSpace(c)
			if v, ok := strings.CutPrefix(c, "key-id:"); ok {
				k.ID = strings.TrimSpace(v)
			} else if v, ok := strings.CutPrefix(c, "not-after:"); ok {
				t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
				if err != nil {
					return Key{}, fmt.Errorf("bad not-after: %w", err)
				}
				k.NotAfter = t
			}
			continue
		}
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return Key{}, errors.New("bad ed25519 public key")
		}
		k.Pub = ed25519.PublicKey(b)
		return k, nil
	}
	return Key{}, errors.New("empty public key file")
}

// LoadKeys reads every keys/malware-ed25519*.pub. A key without a key-id
// header is identified by its file name stem.
func LoadKeys(fsys fs.FS) ([]Key, error) {
	names, err := fs.Glob(fsys, malwareKeyGlob)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no key matching %s", malwareKeyGlob)
	}
	var keys []Key
	seen := map[string]bool{}
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		k, err := PublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if k.ID == "" {
			k.ID = strings.TrimSuffix(path.Base(n), ".pub")
		}
		if seen[k.ID] {
			return nil, fmt.Errorf("duplicate key id %q", k.ID)
		}
		seen[k.ID] = true
		keys = append(keys, k)
	}
	return keys, nil
}

// VerifySnapshot checks body against a signature file "KEYID:BASE64SIG". A
// signature without a key id is rejected. Keys past their
// not-after date are rejected. It returns the id of the verifying key.
func VerifySnapshot(keys []Key, body, sigFile []byte, now time.Time) (string, error) {
	line := strings.TrimSpace(string(sigFile))
	id, line, ok := strings.Cut(line, ":")
	if !ok || id == "" {
		return "", errors.New("signature without key id")
	}
	sig, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", errors.New("malformed signature")
	}
	matched := false
	for _, k := range keys {
		if k.ID != id {
			continue
		}
		matched = true
		if !k.NotAfter.IsZero() && now.After(k.NotAfter) {
			continue
		}
		if ed25519.Verify(k.Pub, body, sig) {
			return k.ID, nil
		}
	}
	if !matched {
		return "", fmt.Errorf("unknown key id %q", id)
	}
	return "", errors.New("signature mismatch (or signing key expired)")
}

// SnapshotGenerated reads the "# generated: RFC3339" header line.
func SnapshotGenerated(body []byte) (time.Time, error) {
	v, ok := SnapshotHeader(body, "generated")
	if !ok {
		return time.Time{}, errors.New("missing '# generated:' header")
	}
	return time.Parse(time.RFC3339, v)
}

// SnapshotHeader returns the value of a leading "# name: value" line.
func SnapshotHeader(body []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(l, "#") {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(l, "#")), name+":"); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func verifyFile(fsys fs.FS, keys []Key, name string, now time.Time) ([]byte, time.Time, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, time.Time{}, err
	}
	sig, err := fs.ReadFile(fsys, name+signatureSuffix)
	if err != nil {
		return nil, time.Time{}, err
	}
	if _, err := VerifySnapshot(keys, body, sig, now); err != nil {
		return nil, time.Time{}, err
	}
	gen, err := SnapshotGenerated(body)
	if err != nil {
		return nil, time.Time{}, err
	}
	return body, gen, nil
}

func loadMalwareSnapshot(fsys fs.FS, now time.Time) (*snapshot, error) {
	pub, err := LoadKeys(fsys)
	if err != nil {
		return nil, fmt.Errorf("domain policy: malware key: %w", err)
	}
	s := &snapshot{}
	body, gen, cerr := verifyFile(fsys, pub, malwareCurrent, now)
	if cerr != nil {
		pbody, pgen, perr := verifyFile(fsys, pub, malwarePrevious, now)
		if perr != nil {
			return nil, fmt.Errorf("domain policy: malware snapshot unverifiable (current: %v; previous: %v)", cerr, perr)
		}
		if age := now.Sub(pgen); age > MaxPrevSnapshotAge {
			return nil, fmt.Errorf("domain policy: malware snapshot unverifiable (current: %v) and previous is %s old (max %s)", cerr, age.Round(time.Hour), MaxPrevSnapshotAge)
		}
		body, gen, s.fallback = pbody, pgen, true
		s.warnings = append(s.warnings, "malware snapshot failed verification, using previous: "+cerr.Error())
	}
	s.body, s.generated = body, gen
	if now.Sub(gen) > MaxSnapshotAge {
		s.warnings = append(s.warnings, fmt.Sprintf("malware snapshot older than %s (generated %s); still enforced", MaxSnapshotAge, gen.UTC().Format(time.RFC3339)))
	}
	return s, nil
}

// CheckSnapshotFiles is the CI check: the current snapshot and the fallback
// (malware.prev.txt) must both exist and verify, the current one must carry
// source and upstream-commit headers and be younger than MaxSnapshotAge.
func CheckSnapshotFiles(fsys fs.FS, now time.Time) error {
	pub, err := LoadKeys(fsys)
	if err != nil {
		return err
	}
	_, gen, err := verifyFile(fsys, pub, malwareCurrent, now)
	if err != nil {
		return fmt.Errorf("%s: %w", malwareCurrent, err)
	}
	if _, statErr := fs.Stat(fsys, malwarePrevious); statErr == nil {
		_, pgen, err := verifyFile(fsys, pub, malwarePrevious, now)
		if err != nil {
			return fmt.Errorf("%s: %w", malwarePrevious, err)
		}
		if age := now.Sub(pgen); age > MaxPrevSnapshotAge {
			return fmt.Errorf("%s: fallback snapshot is %s old (max %s)", malwarePrevious, age.Round(time.Hour), MaxPrevSnapshotAge)
		}
	}
	cur, _ := fs.ReadFile(fsys, malwareCurrent)
	for _, h := range []string{"source", "upstream-commit"} {
		if v, ok := SnapshotHeader(cur, h); !ok || v == "" {
			return fmt.Errorf("%s: missing '# %s:' header", malwareCurrent, h)
		}
	}
	if _, err := fs.Stat(fsys, malwarePrevious); err != nil {
		return fmt.Errorf("%s: fallback snapshot missing: %w", malwarePrevious, err)
	}
	if age := now.Sub(gen); age > MaxSnapshotAge {
		return fmt.Errorf("%s: snapshot is %s old (max %s)", malwareCurrent, age.Round(time.Hour), MaxSnapshotAge)
	}
	return nil
}
