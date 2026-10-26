package domains

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
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

// PublicKey decodes the pinned key file (one base64 line, comments allowed).
func PublicKey(raw []byte) (ed25519.PublicKey, error) {
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, errors.New("bad ed25519 public key")
		}
		return ed25519.PublicKey(b), nil
	}
	return nil, errors.New("empty public key file")
}

// VerifySnapshot checks body against a base64 signature file.
func VerifySnapshot(pub ed25519.PublicKey, body, sigFile []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed signature")
	}
	if !ed25519.Verify(pub, body, sig) {
		return errors.New("signature mismatch")
	}
	return nil
}

// SnapshotGenerated reads the "# generated: RFC3339" header line.
func SnapshotGenerated(body []byte) (time.Time, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(l, "#") {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(l, "#")), "generated:"); ok {
			return time.Parse(time.RFC3339, strings.TrimSpace(v))
		}
	}
	return time.Time{}, errors.New("missing '# generated:' header")
}

func verifyFile(fsys fs.FS, pub ed25519.PublicKey, name string) ([]byte, time.Time, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, time.Time{}, err
	}
	sig, err := fs.ReadFile(fsys, name+signatureSuffix)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := VerifySnapshot(pub, body, sig); err != nil {
		return nil, time.Time{}, err
	}
	gen, err := SnapshotGenerated(body)
	if err != nil {
		return nil, time.Time{}, err
	}
	return body, gen, nil
}

func loadMalwareSnapshot(fsys fs.FS, now time.Time) (*snapshot, error) {
	kraw, err := fs.ReadFile(fsys, malwareKeyFile)
	if err != nil {
		return nil, fmt.Errorf("domain policy: malware key: %w", err)
	}
	pub, err := PublicKey(kraw)
	if err != nil {
		return nil, fmt.Errorf("domain policy: malware key: %w", err)
	}
	s := &snapshot{}
	body, gen, cerr := verifyFile(fsys, pub, malwareCurrent)
	if cerr != nil {
		pbody, pgen, perr := verifyFile(fsys, pub, malwarePrevious)
		if perr != nil {
			return nil, fmt.Errorf("domain policy: malware snapshot unverifiable (current: %v; previous: %v)", cerr, perr)
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

// CheckSnapshotFiles is the CI check: every present snapshot must verify and
// the current one must be younger than MaxSnapshotAge.
func CheckSnapshotFiles(fsys fs.FS, now time.Time) error {
	kraw, err := fs.ReadFile(fsys, malwareKeyFile)
	if err != nil {
		return err
	}
	pub, err := PublicKey(kraw)
	if err != nil {
		return err
	}
	_, gen, err := verifyFile(fsys, pub, malwareCurrent)
	if err != nil {
		return fmt.Errorf("%s: %w", malwareCurrent, err)
	}
	if _, statErr := fs.Stat(fsys, malwarePrevious); statErr == nil {
		if _, _, err := verifyFile(fsys, pub, malwarePrevious); err != nil {
			return fmt.Errorf("%s: %w", malwarePrevious, err)
		}
	}
	if age := now.Sub(gen); age > MaxSnapshotAge {
		return fmt.Errorf("%s: snapshot is %s old (max %s)", malwareCurrent, age.Round(time.Hour), MaxSnapshotAge)
	}
	return nil
}
