package domains

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	policydata "github.com/gun025bba/eve-serving/deploy/policy"
)

func repoPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := Load(policydata.FS, time.Now())
	if err != nil {
		t.Fatalf("repository policy must load: %v", err)
	}
	return p
}

// TestRepoPolicy_Loads also enforces the CI rule: the committed malware
// snapshot verifies and is at most 30 days old.
func TestRepoPolicy_Loads(t *testing.T) {
	p := repoPolicy(t)
	if p.SnapshotFallback {
		t.Fatal("current malware snapshot must verify")
	}
	if err := CheckSnapshotFiles(policydata.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestIPLiteral_Variants(t *testing.T) {
	p := repoPolicy(t)
	priv := []string{
		"127.0.0.1", "2130706433", "0177.0.0.1", "0x7f.1", "0x7f000001", "0x7F.0.0.1", "127.1", "127.0.1", "0177.1",
		"10.1", "10.0.0.1", "167772161", "012.0.0.1", "192.168.1.1", "0xc0.0xa8.1.1", "172.16.0.1",
		"169.254.169.254", "0xa9fea9fe", "100.64.0.1", "100.100.100.100", "0.0.0.0", "0", "224.0.0.1", "255.255.255.255",
		"[::1]", "::1", "[0:0:0:0:0:0:0:1]", "[::ffff:7f00:1]", "[::ffff:127.0.0.1]", "[::127.0.0.1]",
		"[64:ff9b::7f00:1]", "[64:ff9b::10.0.0.1]", "[fe80::1%25en0]", "[fe80::1]", "[fd00::1]", "[fc00::1]", "[::]", "[ff02::1]",
		"[::ffff:10.0.0.1]", "[::ffff:a9fe:a9fe]", "127.0.0.1.", "[fd7a:115c:a1e0::1]",
	}
	for _, h := range priv {
		d := p.CheckHost(h)
		if !d.Blocked || d.Reason != ReasonIPLiteralPriv {
			t.Errorf("%q: got %+v, want %s", h, d, ReasonIPLiteralPriv)
		}
	}
	pub := []string{"8.8.8.8", "134744072", "0x8.0x8.0x8.0x8", "010.010.010.010", "[2001:4860:4860::8888]", "[::ffff:8.8.8.8]"}
	for _, h := range pub {
		d := p.CheckHost(h)
		if !d.Blocked || d.Reason != ReasonIPLiteral {
			t.Errorf("%q: got %+v, want %s", h, d, ReasonIPLiteral)
		}
	}
	bad := []string{"1.2.3.4.5", "256.1.1.1", "0x1ffffffff", "08.1.1.1", "1.2.3.0x", "4294967296", "example.123", "[::zz]"}
	for _, h := range bad {
		d := p.CheckHost(h)
		if !d.Blocked {
			t.Errorf("%q must be rejected, got %+v", h, d)
		}
	}
}

func TestIPLiteral_WHATWGGolden(t *testing.T) {
	// Expected values follow the WHATWG URL host parser.
	for in, want := range map[string]string{
		"2130706433": "127.0.0.1", "0177.0.0.1": "127.0.0.1", "0x7f.1": "127.0.0.1", "0x7f000001": "127.0.0.1",
		"127.1": "127.0.0.1", "10.1": "10.0.0.1", "1.2.3": "1.2.0.3", "0x7f.0x0.0x0.0x1": "127.0.0.1", "0x.0x.0x.0x": "0.0.0.0",
		"192.168.257": "192.168.1.1", "[::ffff:127.0.0.1]": "::ffff:127.0.0.1", "1.2.3.4.": "1.2.3.4",
	} {
		a, err := ParseIPLiteral(in)
		if err != nil || a.String() != want {
			t.Errorf("%q -> %v %v, want %s", in, a, err, want)
		}
	}
	if _, err := ParseIPLiteral("example.com"); err != errNotIP {
		t.Fatal("names are not IP literals")
	}
}

func TestNormalize_Table(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM": "example.com", "example.com.": "example.com", "example.com:8443": "example.com",
		"한국.kr": "xn--3e0b707e.kr", "BÜCHER.de": "xn--bcher-kva.de",
	} {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "a..b", "xn--a.com\u202e", strings.Repeat("a", 64) + ".com"} {
		if _, err := NormalizeHost(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	p := repoPolicy(t)
	if d := p.CheckHost("xn--a.com\u202e"); !d.Blocked || d.Reason != ReasonInvalidHost {
		t.Fatalf("IDNA failure must block: %+v", d)
	}
}

func testFS(yaml string, cats map[string]string) fstest.MapFS {
	m := fstest.MapFS{"web-domains.yaml": {Data: []byte(yaml)}}
	for k, v := range cats {
		m["categories/"+k+".txt"] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestWildcard_LabelBoundaryAndDenyWins(t *testing.T) {
	fsys := testFS("version: 1\nmode: allowlist_only\ndeny_categories: [paste]\ndeny: [\"*.x.com\", \"bad.example\"]\nallow: [\"*.x.com\", \"x.com\", \"bad.example\", \"ok.example\", \"한국.kr\"]\n",
		map[string]string{"paste": "paste.*\n"})
	p, err := Load(fsys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"a.x.com": ReasonDomainDenied, "a.b.x.com": ReasonDomainDenied, "x.com": "", "evil-x.com": ReasonNotAllowlisted,
		"xx.com": ReasonNotAllowlisted, "bad.example": ReasonDomainDenied, "BAD.example.": ReasonDomainDenied, "ok.example": "",
		"paste.example": ReasonDomainDenied, "paste": ReasonNotAllowlisted, "한국.KR": "", "other.example": ReasonNotAllowlisted,
	}
	for h, want := range cases {
		if d := p.CheckHost(h); d.Reason != want || d.Blocked != (want != "") {
			t.Errorf("%q: got %+v, want %q", h, d, want)
		}
	}
}

func TestCategories_RepresentativeHosts(t *testing.T) {
	p := repoPolicy(t)
	for _, h := range []string{"bit.ly", "t.co", "tinyurl.com", "goo.gl", "abc.ngrok.io", "x.ngrok-free.app", "y.trycloudflare.com",
		"z.loca.lt", "pastebin.com", "paste.ee", "hastebin.com", "transfer.sh", "127.0.0.1.nip.io", "10.0.0.1.sslip.io", "a.xip.io"} {
		if d := p.CheckHost(h); !d.Blocked || d.Reason != ReasonDomainDenied || d.RuleID == "" {
			t.Errorf("%q must be denied by a category, got %+v", h, d)
		}
	}
	for _, h := range []string{"ngrok.io", "example.com", "news.naver.com", "ko.wikipedia.org"} {
		if d := p.CheckHost(h); d.Blocked {
			t.Errorf("%q must pass, got %+v", h, d)
		}
	}
}

func TestLoad_FailClosed(t *testing.T) {
	bad := []string{
		"version: 1\nmode: denylist\nunknown: 1\n",
		"version: 2\nmode: denylist\n",
		"version: 1\nmode: open\n",
		"version: 1\nmode: denylist\ndeny_categories: [nope]\n",
		"version: 1\nmode: denylist\ndeny: [\"a*b.com\"]\n",
		"version: 1\nmode: denylist\ndeny_categories: [malware]\n", // no snapshot
		": not yaml",
	}
	for _, y := range bad {
		if _, err := Load(testFS(y, nil), time.Now()); err == nil {
			t.Errorf("must fail: %q", y)
		}
	}
	if _, err := Load(fstest.MapFS{}, time.Now()); err == nil {
		t.Error("missing file must fail")
	}
	var nilP *Policy
	if d := nilP.CheckHost("example.com"); !d.Blocked || d.Reason != ReasonPolicyUnloaded {
		t.Fatal("nil policy must block everything")
	}
}

func signedFS(t *testing.T, gen time.Time, corruptCur, withPrev, corruptPrev bool) fstest.MapFS {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	snap := func(host string) []byte {
		return []byte("# generated: " + gen.UTC().Format(time.RFC3339) + "\n" + host + "\n")
	}
	sig := func(b []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))) }
	m := testFS("version: 1\nmode: denylist\ndeny_categories: [malware]\n", nil)
	m["keys/malware-ed25519.pub"] = &fstest.MapFile{Data: []byte(base64.StdEncoding.EncodeToString(pub))}
	cur := snap("cur.example")
	m["categories/malware.txt"] = &fstest.MapFile{Data: cur}
	m["categories/malware.txt.sig"] = &fstest.MapFile{Data: sig(cur)}
	if corruptCur {
		m["categories/malware.txt"] = &fstest.MapFile{Data: append(cur, []byte("evil.example\n")...)}
	}
	if withPrev {
		prev := snap("prev.example")
		m["categories/malware.prev.txt"] = &fstest.MapFile{Data: prev}
		m["categories/malware.prev.txt.sig"] = &fstest.MapFile{Data: sig(prev)}
		if corruptPrev {
			m["categories/malware.prev.txt.sig"] = &fstest.MapFile{Data: sig([]byte("x"))}
		}
	}
	return m
}

func TestMalwareSnapshot_Rules(t *testing.T) {
	now := time.Now()
	p, err := Load(signedFS(t, now, false, false, false), now)
	if err != nil || !p.CheckHost("cur.example").Blocked || p.SnapshotFallback {
		t.Fatalf("valid snapshot: %v %+v", err, p)
	}
	p, err = Load(signedFS(t, now, true, true, false), now)
	if err != nil || !p.SnapshotFallback || !p.CheckHost("prev.example").Blocked || p.CheckHost("cur.example").Blocked || len(p.Warnings) == 0 {
		t.Fatalf("tampered current must fall back to previous: %v %+v", err, p)
	}
	if _, err := Load(signedFS(t, now, true, false, false), now); err == nil {
		t.Fatal("tampered current without previous must fail closed")
	}
	if _, err := Load(signedFS(t, now, true, true, true), now); err == nil {
		t.Fatal("tampered current and previous must fail closed")
	}
	old := now.Add(-31 * 24 * time.Hour)
	fs := signedFS(t, old, false, false, false)
	p, err = Load(fs, now)
	if err != nil || !p.CheckHost("cur.example").Blocked || len(p.Warnings) == 0 {
		t.Fatalf("stale snapshot keeps enforcing with a warning: %v %+v", err, p)
	}
	if err := CheckSnapshotFiles(fs, now); err == nil {
		t.Fatal("CI check must fail for a snapshot older than 30 days")
	}
}

// Low (PR #13 review): key ids and an overlapping rotation window.
func TestMalwareSnapshot_KeyRotationOverlap(t *testing.T) {
	now := time.Now()
	oldPub, oldPriv, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte("# generated: " + now.UTC().Format(time.RFC3339) + "\nx.example\n")
	sig := func(id string, k ed25519.PrivateKey) []byte {
		return []byte(id + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k, body)))
	}
	keys := []Key{{ID: "k1", Pub: oldPub, NotAfter: now.Add(7 * 24 * time.Hour)}, {ID: "k2", Pub: newPub}}
	for _, tc := range []struct {
		sig  []byte
		at   time.Time
		ok   bool
		name string
	}{
		{sig("k1", oldPriv), now, true, "old key inside overlap"},
		{sig("k2", newPriv), now, true, "new key"},
		{sig("k1", oldPriv), now.Add(8 * 24 * time.Hour), false, "old key after not-after"},
		{sig("k2", oldPriv), now, false, "wrong key for id"},
		{sig("k9", newPriv), now, false, "unknown id"},
		{[]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(newPriv, body))), now, true, "legacy bare signature"},
	} {
		_, err := VerifySnapshot(keys, body, tc.sig, tc.at)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
	k, err := PublicKey([]byte("# key-id: k1\n# not-after: 2030-01-01T00:00:00Z\n" + base64.StdEncoding.EncodeToString(oldPub)))
	if err != nil || k.ID != "k1" || k.NotAfter.Year() != 2030 {
		t.Fatal(k, err)
	}
}

// Low (PR #13 review): the repo ships a verified fallback snapshot and the
// header records source and upstream commit.
func TestRepoSnapshot_FallbackAndProvenance(t *testing.T) {
	if err := CheckSnapshotFiles(policydata.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	cur, _ := policydata.FS.ReadFile("categories/malware.txt")
	for _, h := range []string{"source", "upstream-commit"} {
		if v, ok := SnapshotHeader(cur, h); !ok || v == "" {
			t.Errorf("missing %s header", h)
		}
	}
}

// Low (PR #13 review): 6to4 and Teredo are non-public in the policy layer.
func TestIsNonPublic_6to4Teredo(t *testing.T) {
	for _, s := range []string{"2002:c0a8:0101::1", "2002:7f00:1::", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1"} {
		if !IsNonPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s must be non-public", s)
		}
	}
	if IsNonPublic(netip.MustParseAddr("2001:4860:4860::8888")) {
		t.Error("public IPv6 must stay public")
	}
}
