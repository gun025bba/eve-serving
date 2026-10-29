// Package deploycheck holds checks of deploy/ shell scripts that cannot run end to end in CI
// (the real installer needs macOS, root and a live gateway). Static checks plus the bash
// behaviour test, which sources the installer and drives it against a temp dir.
package deploycheck

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	script       = "../../deploy/install-calendar-adapter.sh"
	behaviour    = "../../deploy/test-calendar-adapter.sh"
	buildScript  = "../../pim/eve-pim/build-release.sh"
	signScript   = "../../deploy/nopasswd/sign-pim-manifest.sh"
	infoPlist    = "../../pim/eve-pim/Support/eve-pim-Info.plist"
	entitlements = "../../pim/eve-pim/Support/eve-pim.entitlements"
	agentTmpl    = "../../deploy/launchd/com.eve.pim.plist.template"
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

func read(t *testing.T) string { return readFile(t, script) }

func assign(t *testing.T, s, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + `=(\S*)`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%s= not found", name)
	}
	return m[1]
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

// Security Lows (#107 review): the pin is a full 40-hex SHA, compared exactly (no prefix match).
func TestCalendarAdapterPinIsFullSHA(t *testing.T) {
	s := read(t)
	if sha := assign(t, s, "PIM_SHA"); !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		t.Fatalf("PIM_SHA %q is not 40 lowercase hex", sha)
	}
	mustContain(t, "installer", s,
		`is_hex_len "$PIM_SHA" 40 || die`,
		`[ "$MF_SHA" = "$PIM_SHA" ] || die`,
		`pim_sha) is_hex_len "$v" 40`,
		`pim_sha256) is_hex_len "$v" 64`,
		`[ "$(sha_of "$WORK/in.eve-pim")" = "$MF_SHA256" ] || die`,
	)
	// macOS bash 3.2 (UTF-8 locale) lets the range [a-f] match upper case: hex checks list the digits.
	for _, f := range []string{script, signScript, buildScript} {
		if strings.Contains(readFile(t, f), "[0-9a-f]") || strings.Contains(readFile(t, f), "[!0-9a-f]") {
			t.Errorf("%s uses the locale-dependent range [0-9a-f]", f)
		}
	}
	if strings.Contains(s, `case "$FULL" in "$PIM_SHA"*)`) {
		t.Error("prefix comparison of the pin is back")
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
		{strings.ToUpper(h40), "40", false}, {strings.ToUpper(h64), "64", false}, {h40[:39] + "G", "40", false}, {h40[:39] + "g", "40", false}, {"", "40", false},
		{h64, "40", false}, {h40, "64", false}, {h40[:20] + " " + h40[21:], "40", false},
	} {
		cmd := exec.Command(bash, "-c", m+"\n"+`is_hex_len "$1" "$2"`, "x", c.v, c.n)
		got := cmd.Run() == nil
		if got != c.want {
			t.Errorf("is_hex_len(%q, %s) = %v, want %v", c.v, c.n, got, c.want)
		}
	}
}

// Security Low: no predictable /tmp paths for root; a 0700 mktemp -d holds the inputs' copies and the log.
func TestCalendarAdapterPrivateWorkDir(t *testing.T) {
	s := read(t)
	mustContain(t, "installer", s,
		`WORK=$(mktemp -d /tmp/eve-install-calendar-adapter.XXXXXXXXXX)`,
		`chmod 0700 "$WORK"`,
		`LOG=$WORK/install-$TS_STAMP.log`,
	)
	mustNotContain(t, "installer", s, `LOG=/tmp/`, `/tmp/eve-pim-build`, `BUILD=`)
}

// Security Low: the pinned commit stays reachable (kept branch), without tags.
func TestCalendarAdapterPinStaysReachable(t *testing.T) {
	s := read(t)
	if keep := assign(t, s, "KEEP_REF"); !strings.HasPrefix(keep, "keep/") || !strings.Contains(keep, assign(t, s, "PIM_SHA")[:7]) {
		t.Fatalf("KEEP_REF %q must be a keep/ branch named after the pinned commit", keep)
	}
	if regexp.MustCompile(`git[^\n]*\btag\b`).MatchString(s) {
		t.Error("the installer must not create tags")
	}
}

// No local build any more: the Mac only verifies and installs what CI built and signed.
func TestCalendarAdapterHasNoLocalBuild(t *testing.T) {
	s := stripShellComments(read(t))
	mustNotContain(t, "installer", s,
		"git clone", "git -C", "git fetch", "git checkout", "cat-file", "swift build", "swift test", "xcrun", "xcode-select",
		"EXP_PIM_SHA256", "RUN_TESTS", "PIMHOME", "$BUILD", "pim-build", "eve-pim/Package.swift",
	)
	if regexp.MustCompile(`(?m)^\s*(\S+\s+)*(swift|git)\s`).MatchString(s) {
		t.Error("the installer runs git or swift")
	}
	if regexp.MustCompile(`\beval\b`).MatchString(s) {
		t.Error("eval in the installer")
	}
}

func stripShellComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Trust: the verifier is the passwordless-deploy entrypoint's (pinned key, sourced as root), no new sudoers verb, and the fixed
// locations / tools are plain assignments the environment cannot override.
func TestCalendarAdapterTrustAndFixedPaths(t *testing.T) {
	s := read(t)
	mustContain(t, "installer", s,
		`( . "$DEPLOY_ENTRY" && verify_sig "$1" "$2" )`,
		`dir_chain_ok "$(/usr/bin/dirname "$DEPLOY_ENTRY")"`,
		`TRUST_ROOT=/ `,
		`DEPLOY_ENTRY=/usr/local/libexec/eve/deploy`,
		`SOCK_BASE=/opt/eve-pim`,
		`PIM_DIR=$SOCK_BASE/bin`,
		`PIM_BIN=$PIM_DIR/eve-pim`,
		`OWNER_ARGS="-o root -g wheel"`,
		`INSTALL=/usr/bin/install; MV=/bin/mv; CODESIGN=/usr/bin/codesign`,
	)
	// review L-1: no test hook (a place to re-point the trust root) in the production file; tests replace nothing but fixed paths
	mustNotContain(t, "installer", s, "eve_deploy_overrides")
	for _, name := range []string{"TRUST_ROOT", "PIM_PIN_READY", "DEPLOY_ENTRY", "PIM_DIR", "PIM_BIN", "PIM_SB", "PIM_MF", "PIM_PREV", "OWNER_ARGS", "ROOT_UID", "ROOT_GID", "PIM_SHA", "ALLOWED_USAGE_KEYS", "ALLOWED_ENTITLEMENTS"} {
		if regexp.MustCompile(`(?m)^` + name + `="?\$\{`).MatchString(s) {
			t.Errorf("%s is environment-overridable", name)
		}
	}
	for _, f := range []string{"../../deploy/nopasswd/setup-deploy-nopasswd.sh", "../../deploy/nopasswd/eve-deploy"} {
		if strings.Contains(strings.ToLower(readFile(t, f)), "pim") {
			t.Errorf("%s mentions the calendar adapter: the sudoers surface must not grow", f)
		}
	}
	mustNotContain(t, "installer", stripShellComments(s), "sudoers", "visudo")
	mustNotContain(t, "behaviour test", readFile(t, behaviour), "eve_deploy_overrides() {")
	if !strings.HasPrefix(s, "#!/bin/bash\n") {
		t.Error("shebang")
	}
}

// Install = temp file in the SAME directory with owner+mode set, hash check, one rename, hash check again; replace only when the hash differs.
func TestCalendarAdapterAtomicInstallShape(t *testing.T) {
	s := read(t)
	mustContain(t, "installer", s,
		`tmp="$(/usr/bin/dirname "$2")/.${2##*/}.new.$$"`,
		`"$INSTALL" $OWNER_ARGS -m "$3" "$1" "$tmp"`,
		`"$MV" -f "$tmp" "$2"`,
		`[ "$(sha_of "$2")" = "$want" ] && owned_mode_ok "$2" "$3"`,
		`adapter_current() { [ "$(installed_hash)" = "$MF_SHA256" ] && owned_mode_ok "$PIM_BIN" 755; }`,
		`[ "$(sha_of "$PIM_BIN")" = "$MF_SHA256" ] || die "installed adapter hash changed after install"`,
		`check_signature "$PIM_BIN" || die`,
	)
	mustNotContain(t, "installer", s, `cp "$WORK/in.eve-pim" "$PIM_BIN"`, `> "$PIM_BIN"`, `>"$PIM_BIN"`)
	// the login home is never an execution location any more
	mustNotContain(t, "installer", s, `as_user "$PIMHOME`, `$U_HOME/eve-pim/eve-pim"`+` --socket-dir`)
}

// The launch template runs the binary from the fixed root-owned directory; writes stay off.
func TestCalendarAdapterLaunchTemplate(t *testing.T) {
	tpl := readFile(t, agentTmpl)
	mustContain(t, "template", tpl, "__PIM_DIR__/eve-pim.sb", "__PIM_DIR__/eve-pim<")
	mustNotContain(t, "template", stripComments(tpl), "__HOME__/eve-pim", "writes")
	mustContain(t, "installer", read(t), `__PIM_DIR__`, `.replace("__PIM_DIR__", pimdir)`)
}

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
	// the lists the installer and the build script enforce are the same lists
	for _, f := range []string{script, buildScript} {
		s := readFile(t, f)
		if f == script {
			if got := list(t, s, "ALLOWED_USAGE_KEYS"); !eq(got, allowedUsage) {
				t.Errorf("installer ALLOWED_USAGE_KEYS = %v", got)
			}
			if got := list(t, s, "ALLOWED_ENTITLEMENTS"); !eq(got, allowedEnts) {
				t.Errorf("installer ALLOWED_ENTITLEMENTS = %v", got)
			}
		} else {
			if got := list(t, s, "ALLOWED_USAGE"); !eq(got, allowedUsage) {
				t.Errorf("build script ALLOWED_USAGE = %v", got)
			}
			if got := list(t, s, "ALLOWED_ENT"); !eq(got, allowedEnts) {
				t.Errorf("build script ALLOWED_ENT = %v", got)
			}
		}
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
	mustContain(t, "sign-pim-manifest.sh", readFile(t, signScript), "eve-pim-manifest=1", "pim_sha=$SHA", "pim_sha256=$(sha_of", "-n eve-deploy")
}

// bash -n on the scripts.
func TestCalendarAdapterSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, f := range []string{script, behaviour, buildScript, signScript} {
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", f, err, out)
		}
	}
}

// The behaviour test: hash mismatch refusal, bad pim_sha format, same-hash no-op, atomic install, post-install re-verify, rollback rules.
// The signature checks use the real ssh-keygen and the real entrypoint verifier; codesign is a stub.
func TestCalendarAdapterBehaviour(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if _, err := os.Stat("/usr/bin/ssh-keygen"); err != nil { // the entrypoint's verifier calls this absolute path
		t.Skip("no /usr/bin/ssh-keygen")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bash, behaviour).CombinedOutput()
	if err != nil {
		t.Fatalf("behaviour test failed: %v\n%s", err, out)
	}
	t.Logf("%s", strings.TrimSpace(string(out)))
}

// Security M-1: the pin PIM_SHA predates the source this installer ships with and is deliberately NOT changed in this PR.
// While PIM_PIN_READY=0 the installer refuses every install and the sign helper refuses without --test-only. The change that moves the
// pin (deploy-major) sets PIM_PIN_READY=1, and then PIM_SHA must really contain the adapter source and be an ancestor of HEAD.
func TestCalendarAdapterPinGate(t *testing.T) {
	s := read(t)
	ready := assign(t, s, "PIM_PIN_READY")
	if ready != "0" && ready != "1" {
		t.Fatalf("PIM_PIN_READY=%q must be 0 or 1", ready)
	}
	mustContain(t, "installer", s,
		`pin_gate || die "adapter pin not ready: refusing to install"`,
		`if [ "$MODE" = install ] && ! pin_gate >&2; then exit 1; fi`,
		`[ "$PIM_PIN_READY" = 1 ] || {`,
	)
	mustContain(t, "sign helper", readFile(t, signScript), "--test-only", "PIM_PIN_READY=1", "NEVER run in CI")
	if ready == "0" {
		// the pin must still be the pre-source commit (the PR does not touch it)
		if sha := assign(t, s, "PIM_SHA"); sha != "6ce19939245e021f7dff5830d3a4273a85c9c949" {
			t.Errorf("PIM_SHA changed to %s while PIM_PIN_READY=0: the pin moves only in the separate deploy-major change", sha)
		}
		return
	}
	// PIM_PIN_READY=1: the pin must cover the shipped source. Needs full history (actions/checkout fetch-depth: 0 in the job that runs this).
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("PIM_PIN_READY=1 but git is not available to verify the pin")
	}
	sha := assign(t, s, "PIM_SHA")
	for _, f := range []string{"pim/eve-pim/build-release.sh", "pim/eve-pim/Support/eve-pim.entitlements", "pim/eve-pim/Support/eve-pim-Info.plist"} {
		if out, err := exec.Command(git, "cat-file", "-e", sha+":"+f).CombinedOutput(); err != nil {
			t.Errorf("PIM_PIN_READY=1 but PIM_SHA %s does not contain %s: %v %s", sha, f, err, out)
		}
	}
	if out, err := exec.Command(git, "merge-base", "--is-ancestor", sha, "HEAD").CombinedOutput(); err != nil {
		t.Errorf("PIM_SHA %s is not an ancestor of HEAD: %v %s", sha, err, out)
	}
}

// Security L-5/L-3/L-4/L-7c and the rollback hash: the shapes of the fixes (the behaviour test exercises them).
func TestCalendarAdapterHardeningShapes(t *testing.T) {
	s := read(t)
	// exact hardened-runtime check: the flags line only, name list exact, runtime bit set; never `*runtime*` on the whole output
	mustContain(t, "installer", s,
		`case ",$fl," in *,runtime,*) ;;`,
		`0x10000`,
		`grep -c '^CodeDirectory .*flags=0x'`,
	)
	if strings.Contains(s, "*runtime*)") {
		t.Error("substring match on the whole codesign output is back")
	}
	// the expected hashes are passed on install and on rollback
	mustContain(t, "installer", s,
		`put_current "$PIM_PREV" "$PIM_PREV_MF" "$PIM_PREV_SIG" "$p" "$pmh" "$psh"`,
		`put_current "$WORK/in.eve-pim" "$WORK/in.MANIFEST" "$WORK/in.MANIFEST.sig" "$MF_SHA256"`,
		`atomic_put "$1" "$PIM_BIN" 755 "$wb"`,
	)
	// fail closed without a verified rollback
	mustContain(t, "installer", s, `rotate_prev "$cur" || die "refusing to replace the installed adapter without a verified rollback`)
	mustContain(t, "installer", s, "  return 1\n}\nput_current()")
	// parent directories and work dir cleanup
	mustContain(t, "installer", s, `bin_dir_ok() { dir_chain_ok "$PIM_DIR"; }`, `cleanup_work`, `-name 'install-*.log'`)
	if !regexp.MustCompile(`(?s)on_exit\(\).*cleanup_work; \}`).MatchString(s) {
		t.Error("on_exit must clean the work dir")
	}
	// TCC prompt handling in the installer text: deny, pause, check path and sha256; no Reminders wording
	mustContain(t, "installer", s, "예상 밖 팝업", "허용 안 함", "PAUSE", "shasum -a 256 /opt/eve-pim/bin/eve-pim")
	mustNotContain(t, "installer", s, "Calendar/Reminders (TCC)", "미리 알림 권한")
}

// Security M-1/M-2/L-6/L-7: what the runbook and CHANGELOG must say.
func TestCalendarAdapterDocs(t *testing.T) {
	rb := readFile(t, "../../docs/runbooks/5.1.0-calendar-briefing.md")
	cl := readFile(t, "../../CHANGELOG.md")
	mustContain(t, "runbook", rb,
		"separate deploy-major change", "must not be signed or installed", "PIM_PIN_READY",
		"The build job holds no secrets", "cosign", "local use and tests only",
		"unexpected TCC prompt", "PAUSE", "sha256", "Classify rule", "namespace",
		"Constraints for the cosign switch", "--certificate-github-workflow-sha", "--insecure-ignore-tlog",
	)
	mustNotContain(t, "runbook", rb, "--key <release key>", "Calendar/Reminders (TCC)")
	mustContain(t, "CHANGELOG", cl, "PIM_PIN_READY", "separate deploy-major change", "must not be signed or installed")
	mustNotContain(t, "CHANGELOG", cl, "creates and signs the manifest;")
}
