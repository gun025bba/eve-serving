package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Killswitch can only turn tools OFF (docs/tool-platform.md §1.3). It is backed
// by one 0600 YAML file owned by the service account. The file is reloaded on
// SIGHUP and, independent of any signal, whenever its mtime, size, inode,
// mode or owner changes: Watch polls every KillswitchPollInterval (2 s), so a
// change is in force well inside the 5 s the design requires (§6.7, G4).
// Replace the file atomically (write a temp file in the same directory, then
// rename over it); an in-place rewrite can be read half-written, and a
// half-written file is a parse error, i.e. everything off.
//
//   - no file at startup  -> no tool is disabled (5.x behaviour), but automatic
//     web tool selection stays OFF until a valid file exists (N-2: the 5 s
//     off switch must be there before the web is on by default)
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
	// autoOff turns automatic web tool selection off for everyone (the
	// runtime form of web.auto_select_off / GW_WEB_AUTO_SELECT_OFF).
	autoOff bool
	loaded  bool
	// noFile: the file did not exist at startup and no valid one was read since.
	noFile bool
	// sig is the file signature of the last poll.
	sig    fileSig
	sigSet bool
	// fileOK is true only when the most recent read parsed the file OK.
	fileOK bool
	OnLoad func(result string, disabled []string, allOff bool) // audit/log hook
}

type killFile struct {
	AllOff       bool     `yaml:"all_off"`
	Disabled     []string `yaml:"disabled"`
	ProactiveOff bool     `yaml:"proactive_off"`
	// AutoSelectOff switches automatic web tool selection off (6.0.0).
	AutoSelectOff bool `yaml:"auto_select_off"`
}

// NewKillswitch creates a switch for path; uid is the expected file owner.
// A nil *Killswitch disables nothing.
func NewKillswitch(path string, uid int) *Killswitch {
	return &Killswitch{path: path, uid: uid, off: map[string]bool{}}
}

// Reload re-reads the file and returns the outcome string.
func (k *Killswitch) Reload() string {
	sig := k.signature() // taken before the read: a change during the read is seen by the next poll
	allOff, off, pOff, aOff, res := k.read()
	k.mu.Lock()
	switch res {
	case "missing":
		if !k.loaded { // startup without file: no tool disabled, automatic selection off
			k.allOff, k.off, k.proactiveOff, k.autoOff = false, map[string]bool{}, false, false
			k.noFile = true
		} // on reload: keep previous state
	case "ok":
		k.allOff, k.off, k.proactiveOff, k.autoOff = allOff, off, pOff, aOff
		k.noFile = false
	default: // any error: fail closed
		k.allOff, k.off, k.proactiveOff, k.autoOff = true, map[string]bool{}, true, true
	}
	k.sig, k.sigSet = sig, true
	k.fileOK = res == "ok"
	k.loaded = true
	a, names := k.allOff, keys(k.off)
	k.mu.Unlock()
	if k.OnLoad != nil {
		k.OnLoad(res, names, a)
	}
	return res
}

func (k *Killswitch) read() (allOff bool, off map[string]bool, proactiveOff, autoOff bool, res string) {
	f, err := os.OpenFile(k.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, false, false, "missing"
	}
	if err != nil {
		return false, nil, false, false, "open_error"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false, nil, false, false, "not_regular"
	}
	if st.Mode().Perm() != 0o600 {
		return false, nil, false, false, "bad_perm"
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != k.uid {
		return false, nil, false, false, "bad_owner"
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false, nil, false, false, "read_error"
	}
	var kf killFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&kf); err != nil && !errors.Is(err, io.EOF) {
		return false, nil, false, false, "parse_error"
	}
	off = map[string]bool{}
	for _, n := range kf.Disabled {
		if !nameRe.MatchString(n) {
			return false, nil, false, false, "parse_error"
		}
		off[n] = true
	}
	return kf.AllOff, off, kf.ProactiveOff, kf.AutoSelectOff, "ok"
}

// KillswitchPollInterval is how often Watch looks at the file. Together with
// the reload itself it keeps a change under the 5 s bound of §6.7 (G4).
var KillswitchPollInterval = 2 * time.Second

// fileSig identifies one version of the kill-switch file.
type fileSig struct {
	exists bool
	mtime  time.Time
	size   int64
	mode   os.FileMode
	ino    uint64
	uid    uint32
}

func (k *Killswitch) signature() fileSig {
	st, err := os.Lstat(k.path) // Lstat: a symlink swap is a change too
	if err != nil {
		return fileSig{}
	}
	g := fileSig{exists: true, mtime: st.ModTime(), size: st.Size(), mode: st.Mode()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		g.ino, g.uid = uint64(sys.Ino), sys.Uid
	}
	return g
}

// Poll reloads the file when its signature changed since the last load or
// poll and reports whether it did. It never blocks on anything but the file.
func (k *Killswitch) Poll() bool {
	if k == nil {
		return false
	}
	now := k.signature()
	k.mu.RLock()
	same := k.sigSet && now == k.sig
	k.mu.RUnlock()
	if same {
		return false
	}
	k.Reload()
	return true
}

// Watch polls the file every KillswitchPollInterval until ctx ends. It needs
// no signal: this is what makes the switch work without SIGHUP.
func (k *Killswitch) Watch(ctx context.Context) {
	if k == nil {
		return
	}
	t := time.NewTicker(KillswitchPollInterval)
	defer t.Stop()
	k.WatchTicks(ctx, t.C)
}

// WatchTicks polls once per value received from ticks (Watch passes a 2 s
// ticker; tests pass an injected clock).
func (k *Killswitch) WatchTicks(ctx context.Context, ticks <-chan time.Time) {
	if k == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			k.Poll()
		}
	}
}

// AutoSelectOff reports whether automatic web tool selection is switched off
// by the file. Everything-off (all_off, any read or parse error) counts too,
// and so does a file that did not exist at startup and has not appeared since
// (fail closed, N-2). A nil switch never turns it off (the gateway start-up
// code turns it off itself when no file is configured, see
// config.Config.RequireKillswitch).
func (k *Killswitch) AutoSelectOff() bool {
	if k == nil {
		return false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.allOff || k.autoOff || k.noFile
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

// ProactiveAllowed is the single gate for push and proactive sending. It is
// true ONLY when a kill-switch file is configured, its latest read parsed OK,
// and neither all_off nor proactive_off is set. A nil switch (no path), a
// missing file or any read/parse error means not allowed.
func (k *Killswitch) ProactiveAllowed() bool {
	if k == nil {
		return false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.fileOK && !k.allOff && !k.proactiveOff
}

func (k *Killswitch) String() string {
	if k == nil {
		return "killswitch(none)"
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return fmt.Sprintf("killswitch(all_off=%v auto_select_off=%v disabled=%v)", k.allOff, k.autoOff, keys(k.off))
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
