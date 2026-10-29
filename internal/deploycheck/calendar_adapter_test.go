// Package deploycheck holds static checks of deploy/ shell scripts that cannot run
// in CI (they need macOS, root and a live gateway).
package deploycheck

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

const script = "../../deploy/install-calendar-adapter.sh"

func read(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assign(t *testing.T, s, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + `=(\S*)`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%s= not found", name)
	}
	return m[1]
}

// Security Lows (#107 review): the pin is a full 40-hex SHA, compared exactly.
func TestCalendarAdapterPinIsFullSHA(t *testing.T) {
	s := read(t)
	if sha := assign(t, s, "PIM_SHA"); !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		t.Fatalf("PIM_SHA %q is not 40 lowercase hex", sha)
	}
	for _, want := range []string{
		`is_hex_len "$PIM_SHA" 40 || die`,
		`[ "$FULL" = "$PIM_SHA" ] || die`,
		`is_hex_len "$EXP_PIM_SHA256" 64`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(s, `case "$FULL" in "$PIM_SHA"*)`) {
		t.Error("prefix comparison of the resolved HEAD is back")
	}
}

// The hex-length helper itself, run with bash.
func TestCalendarAdapterHexLen(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	m := regexp.MustCompile(`(?m)^is_hex_len\(\).*$`).FindString(read(t))
	if m == "" {
		t.Fatal("is_hex_len not found")
	}
	h40 := strings.Repeat("a1", 20)
	h64 := strings.Repeat("0f", 32)
	for _, c := range []struct {
		v    string
		n    string
		want bool
	}{
		{h40, "40", true}, {h64, "64", true},
		{h40[:39], "40", false}, {h40[:7], "40", false}, {h40 + "a", "40", false},
		{strings.ToUpper(h40), "40", false}, {h40[:39] + "g", "40", false}, {"", "40", false},
		{h64, "40", false}, {h40, "64", false}, {h40[:20] + " " + h40[21:], "40", false},
	} {
		cmd := exec.Command(bash, "-c", m+"\n"+`is_hex_len "$1" "$2"`, "x", c.v, c.n)
		got := cmd.Run() == nil
		if got != c.want {
			t.Errorf("is_hex_len(%q, %s) = %v, want %v", c.v, c.n, got, c.want)
		}
	}
}

// Security Low: no predictable /tmp paths for root; a 0700 mktemp -d holds build and logs.
func TestCalendarAdapterPrivateWorkDir(t *testing.T) {
	s := read(t)
	for _, want := range []string{
		`WORK=$(mktemp -d /tmp/eve-install-calendar-adapter.XXXXXXXXXX)`,
		`chmod 0700 "$WORK"`,
		`BUILD=$WORK/pim-build`,
		`LOG=$WORK/install-$TS_STAMP.log`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for _, bad := range []string{`BUILD=/tmp/`, `LOG=/tmp/`, `/tmp/eve-pim-build`} {
		if strings.Contains(s, bad) {
			t.Errorf("script still contains the predictable path %q", bad)
		}
	}
}

// Security Low: the pinned commit stays reachable (kept branch + fetch by SHA), without tags.
func TestCalendarAdapterPinStaysReachable(t *testing.T) {
	s := read(t)
	if keep := assign(t, s, "KEEP_REF"); !strings.HasPrefix(keep, "keep/") || !strings.Contains(keep, assign(t, s, "PIM_SHA")[:7]) {
		t.Fatalf("KEEP_REF %q must be a keep/ branch named after the pinned commit", keep)
	}
	for _, want := range []string{`fetch -q origin "$PIM_SHA"`, `cat-file -e "$PIM_SHA^{commit}"`} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if regexp.MustCompile(`git[^\n]*\btag\b`).MatchString(s) {
		t.Error("the installer must not create tags")
	}
}

// bash -n on the whole script.
func TestCalendarAdapterSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
