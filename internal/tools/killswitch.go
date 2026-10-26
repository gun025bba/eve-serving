package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Killswitch can only turn tools OFF (docs/tool-platform.md §1.3). It is backed
// by one 0600 YAML file owned by the service account and reloaded on SIGHUP.
//
//   - no file at startup  -> nothing disabled (current behaviour)
//   - bad perms / owner / symlink / parse error / unknown key -> everything off
//   - file removed on reload -> previous state kept (never re-enables by deletion)
type Killswitch struct {
	path   string
	uid    int
	mu     sync.RWMutex
	allOff bool
	off    map[string]bool
	// proactiveOff turns off proactive messaging and push (off-only).
	proactiveOff bool
	loaded       bool
	OnLoad       func(result string, disabled []string, allOff bool) // audit/log hook
}

type killFile struct {
	AllOff       bool     `yaml:"all_off"`
	Disabled     []string `yaml:"disabled"`
	ProactiveOff bool     `yaml:"proactive_off"`
}

// NewKillswitch creates a switch for path; uid is the expected file owner.
// A nil *Killswitch disables nothing.
func NewKillswitch(path string, uid int) *Killswitch {
	return &Killswitch{path: path, uid: uid, off: map[string]bool{}}
}

// Reload re-reads the file and returns the outcome string.
func (k *Killswitch) Reload() string {
	allOff, off, pOff, res := k.read()
	k.mu.Lock()
	switch res {
	case "missing":
		if !k.loaded { // startup without file: nothing disabled
			k.allOff, k.off, k.proactiveOff = false, map[string]bool{}, false
		} // on reload: keep previous state
	case "ok":
		k.allOff, k.off, k.proactiveOff = allOff, off, pOff
	default: // any error: fail closed
		k.allOff, k.off, k.proactiveOff = true, map[string]bool{}, true
	}
	k.loaded = true
	a, names := k.allOff, keys(k.off)
	k.mu.Unlock()
	if k.OnLoad != nil {
		k.OnLoad(res, names, a)
	}
	return res
}

func (k *Killswitch) read() (bool, map[string]bool, bool, string) {
	f, err := os.OpenFile(k.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, false, "missing"
	}
	if err != nil {
		return false, nil, false, "open_error"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false, nil, false, "not_regular"
	}
	if st.Mode().Perm() != 0o600 {
		return false, nil, false, "bad_perm"
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != k.uid {
		return false, nil, false, "bad_owner"
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false, nil, false, "read_error"
	}
	var kf killFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&kf); err != nil && !errors.Is(err, io.EOF) {
		return false, nil, false, "parse_error"
	}
	off := map[string]bool{}
	for _, n := range kf.Disabled {
		if !nameRe.MatchString(n) {
			return false, nil, false, "parse_error"
		}
		off[n] = true
	}
	return kf.AllOff, off, kf.ProactiveOff, "ok"
}

// Disabled reports whether tool name is switched off.
func (k *Killswitch) Disabled(name string) bool {
	if k == nil {
		return false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.allOff || k.off[name]
}

// ProactiveOff reports whether proactive messaging (and push) is switched
// off by proactive_off or all_off.
func (k *Killswitch) ProactiveOff() bool {
	if k == nil {
		return false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.allOff || k.proactiveOff
}

func (k *Killswitch) String() string {
	if k == nil {
		return "killswitch(none)"
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return fmt.Sprintf("killswitch(all_off=%v disabled=%v)", k.allOff, keys(k.off))
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
