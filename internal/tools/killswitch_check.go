package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// KillswitchReport is the result of a dry run on a candidate kill-switch file
// (eve-gw killswitch-check, §6.7, §10.1 step 1). Nothing is applied: the file
// is only parsed, with the same checks (owner, 0600, regular file, strict keys)
// the running gateway applies.
type KillswitchReport struct {
	Result        string   // "ok", or the failure class (parse_error, bad_perm, ...)
	AllOff        bool     // all_off, or every tool off because the file is unusable
	Disabled      []string // names listed under disabled
	AutoSelectOff bool
	ProactiveOff  bool
	WebToolsOff   bool // web_search and web_fetch are both unavailable
	// OldBuild is how a 5.x gateway reads the same file: "valid" or the
	// failure class it would hit (it rejects unknown keys and then turns every
	// tool off).
	OldBuild string
}

// legacyKillFile is the key set of a 5.x gateway (no auto_select_off).
type legacyKillFile struct {
	AllOff       bool     `yaml:"all_off"`
	Disabled     []string `yaml:"disabled"`
	ProactiveOff bool     `yaml:"proactive_off"`
}

// CheckKillswitchFile parses path without side effects. uid is the expected
// file owner (the gateway account).
func CheckKillswitchFile(path string, uid int) KillswitchReport {
	k := NewKillswitch(path, uid)
	allOff, off, pro, auto, res := k.read()
	r := KillswitchReport{Result: res, AllOff: allOff, AutoSelectOff: auto, ProactiveOff: pro}
	for n := range off {
		r.Disabled = append(r.Disabled, n)
	}
	sort.Strings(r.Disabled)
	if res != "ok" {
		if res != "missing" {
			r.AllOff = true // a gateway reading this file turns every tool off
		}
		r.WebToolsOff = res != "missing"
		r.OldBuild = legacyResult(path, uid, res)
		return r
	}
	r.WebToolsOff = allOff || (off["web_search"] && off["web_fetch"])
	r.OldBuild = legacyResult(path, uid, res)
	return r
}

// legacyResult says how a 5.x gateway reads the file.
func legacyResult(path string, uid int, cur string) string {
	if cur != "ok" {
		return cur // the same structural failure in every build
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "open_error"
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return "read_error"
	}
	var kf legacyKillFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&kf); err != nil && !errors.Is(err, io.EOF) {
		return "parse_error"
	}
	return "valid"
}

// String renders the report for the operator.
func (r KillswitchReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "this build:  %s\n", r.Result)
	fmt.Fprintf(&b, "  all_off=%v auto_select_off=%v proactive_off=%v disabled=[%s]\n", r.AllOff, r.AutoSelectOff, r.ProactiveOff, strings.Join(r.Disabled, ","))
	fmt.Fprintf(&b, "  web tools off: %v (web_search and web_fetch both unavailable)\n", r.WebToolsOff)
	if r.OldBuild == "valid" {
		b.WriteString("5.x build:   valid (same effect for the keys it knows)\n")
	} else {
		fmt.Fprintf(&b, "5.x build:   INVALID (%s): a 5.x gateway would turn EVERY tool off (fail closed). Do not roll back with this file; remove auto_select_off first.\n", r.OldBuild)
	}
	b.WriteString("note: the file is read by polling (mtime, every 2 s; applies within 5 s); replace it atomically (write a temp file, chmod 0600, rename).\n")
	return b.String()
}
