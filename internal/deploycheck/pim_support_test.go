// Package deploycheck holds static checks of the eve-pim adapter's build inputs (calendar-only entitlements and
// Info.plist, the build script's signing). The old installer checks (#154, ssh-keygen) were dropped with that installer.
package deploycheck

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	buildScript  = "../../pim/eve-pim/build-release.sh"
	infoPlist    = "../../pim/eve-pim/Support/eve-pim-Info.plist"
	entitlements = "../../pim/eve-pim/Support/eve-pim.entitlements"
	eventKit     = "../../pim/eve-pim/Sources/eve-pim/EventKitStore.swift"
)

// The adapter is calendar-only: these are the only privacy usage strings and the only entitlement it may carry.
var (
	allowedUsage = []string{"NSCalendarsFullAccessUsageDescription", "NSCalendarsUsageDescription"}
	allowedEnts  = []string{"com.apple.security.personal-information.calendars"}
	allowedInfo  = append([]string{"CFBundleIdentifier", "CFBundleName", "CFBundleVersion"}, allowedUsage...)
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustContain(t *testing.T, what, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("%s lacks %q", what, w)
		}
	}
}

func mustNotContain(t *testing.T, what, s string, bads ...string) {
	t.Helper()
	for _, b := range bads {
		if strings.Contains(s, b) {
			t.Errorf("%s still contains %q", what, b)
		}
	}
}

// keys returns every <key>..</key> of a (flat) plist.
func keys(plist string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<key>([^<]*)</key>`).FindAllStringSubmatch(stripComments(plist), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func stripComments(s string) string {
	return regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(s, "")
}

// disallowed returns the keys of a plist that are not in the allow list.
func disallowed(plist string, allow []string) []string {
	ok := map[string]bool{}
	for _, a := range allow {
		ok[a] = true
	}
	var bad []string
	for _, k := range keys(plist) {
		if !ok[k] {
			bad = append(bad, k)
		}
	}
	return bad
}

// the words of a quoted space-padded list assignment: NAME=" a b "
func list(t *testing.T, s, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + `="([^"]*)"`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%s=\"...\" not found", name)
	}
	f := strings.Fields(m[1])
	sort.Strings(f)
	return f
}

func eq(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

// Calendar-only Info.plist and entitlements (and what rejects anything else).
func TestPimSupportFilesAreCalendarOnly(t *testing.T) {
	info, ent := readFile(t, infoPlist), readFile(t, entitlements)
	if bad := disallowed(info, allowedInfo); len(bad) > 0 {
		t.Errorf("Info.plist has keys beyond calendar: %v", bad)
	}
	if bad := disallowed(ent, allowedEnts); len(bad) > 0 {
		t.Errorf("entitlements have keys beyond calendar: %v", bad)
	}
	if !eq(keys(ent), allowedEnts) {
		t.Errorf("entitlements = %v, want exactly %v", keys(ent), allowedEnts)
	}
	have := map[string]bool{}
	for _, k := range keys(info) {
		have[k] = true
	}
	if !have["NSCalendarsUsageDescription"] || !have["NSCalendarsFullAccessUsageDescription"] || !have["CFBundleIdentifier"] {
		t.Errorf("Info.plist lacks the calendar usage strings or the bundle id: %v", keys(info))
	}
	mustContain(t, "Info.plist", info, "<string>com.eve.pim</string>")
	mustContain(t, "entitlements", ent, "<true/>")
	// the lists the build script enforces are the same lists
	bs := readFile(t, buildScript)
	if got := list(t, bs, "ALLOWED_USAGE"); !eq(got, allowedUsage) {
		t.Errorf("build script ALLOWED_USAGE = %v", got)
	}
	if got := list(t, bs, "ALLOWED_ENT"); !eq(got, allowedEnts) {
		t.Errorf("build script ALLOWED_ENT = %v", got)
	}
}

// Negative: the rejection logic itself, on synthetic plists that ask for contacts, full disk access and more.
func TestPlistKeyRejection(t *testing.T) {
	mk := func(ks ...string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><plist version="1.0"><dict>`)
		for _, k := range ks {
			b.WriteString("<key>" + k + "</key><true/>")
		}
		b.WriteString("</dict></plist>")
		return b.String()
	}
	for _, k := range []string{
		"NSContactsUsageDescription", "NSRemindersUsageDescription", "NSRemindersFullAccessUsageDescription",
		"NSSystemAdministrationUsageDescription", "NSDesktopFolderUsageDescription", "NSDocumentsFolderUsageDescription",
		"NSPhotoLibraryUsageDescription", "NSLocationUsageDescription", "NSAppleEventsUsageDescription",
	} {
		if bad := disallowed(mk("NSCalendarsUsageDescription", k), allowedUsage); len(bad) != 1 || bad[0] != k {
			t.Errorf("usage key %s not rejected: %v", k, bad)
		}
	}
	for _, k := range []string{
		"com.apple.security.personal-information.addressbook", "com.apple.security.personal-information.photos-library",
		"com.apple.security.personal-information.location", "com.apple.security.files.all", "com.apple.security.cs.allow-jit",
		"com.apple.security.cs.disable-library-validation", "com.apple.security.network.client", "com.apple.security.get-task-allow",
		"com.apple.private.tcc.allow",
	} {
		if bad := disallowed(mk("com.apple.security.personal-information.calendars", k), allowedEnts); len(bad) != 1 || bad[0] != k {
			t.Errorf("entitlement %s not rejected: %v", k, bad)
		}
	}
	if bad := disallowed(mk(allowedUsage...), allowedUsage); len(bad) != 0 {
		t.Errorf("calendar keys wrongly rejected: %v", bad)
	}
	// a key hidden in a comment does not count as present, and an actual key is not hidden by one
	if got := keys("<!-- <key>NSContactsUsageDescription</key> -->" + mk("NSCalendarsUsageDescription")); !eq(got, []string{"NSCalendarsUsageDescription"}) {
		t.Errorf("comment handling: %v", got)
	}
}

// Build config for the CI job: hardened-runtime ad-hoc signature with exactly these files; the helper no longer asks for Reminders.
func TestPimBuildSigning(t *testing.T) {
	b := readFile(t, buildScript)
	if !regexp.MustCompile(`(?m)^codesign [^\n]*-s - [^\n]*-o runtime\b[^\n]*--entitlements "\$ENT"`).MatchString(b) &&
		!regexp.MustCompile(`(?m)^codesign [^\n]*-s - -o runtime `).MatchString(b) {
		t.Error("build script does not sign with `codesign -s - -o runtime`")
	}
	mustContain(t, "build script", b, "--entitlements", "-sectcreate", "__info_plist", "Support/eve-pim.entitlements", "Support/eve-pim-Info.plist",
		"codesign --verify --strict", `pim_sha256=$(sha_of`, "ALLOWED_USAGE", "ALLOWED_ENT")
	mustNotContain(t, "build script", b, "--deep", "get-task-allow", "Reminders", "Contacts")
	ek := readFile(t, eventKit)
	if !regexp.MustCompile(`for type in \[EKEntityType\.event\] where`).MatchString(ek) {
		t.Error("EventKitStore.requestAccess must request Calendar only (a Reminders request without its usage string kills the process)")
	}
	if strings.Contains(ek, "[EKEntityType.event, .reminder]") {
		t.Error("EventKitStore.requestAccess asks for Reminders again")
	}
}

// bash -n on the build script.
func TestPimBuildScriptSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-n", buildScript).CombinedOutput(); err != nil {
		t.Fatalf("bash -n %s: %v\n%s", buildScript, err, out)
	}
}
