package session

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := NewCode()
		if err != nil || !re.MatchString(c) || seen[c] {
			t.Fatal(c, err)
		}
		seen[c] = true
	}
	for in, want := range map[string]string{"abcd-efgh": "ABCDEFGH", "o1il 2345": "01112345", "ABCD EFGH": "ABCDEFGH"} {
		if got, ok := NormalizeCode(in); !ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "ABCD-EFG", "ABCD-EFGHJ", "UUUU-UUUU", "ABCD_EFGH"} {
		if _, ok := NormalizeCode(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLoadSecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pepper")
	os.WriteFile(p, []byte("secret\n"), 0o600)
	if b, err := LoadSecret(p); err != nil || string(b) != "secret" {
		t.Fatal(b, err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		os.Chmod(p, mode)
		if _, err := LoadSecret(p); err == nil {
			t.Errorf("%#o accepted", mode)
		}
	}
	os.Chmod(p, 0o400)
	if _, err := LoadSecret(p); err != nil {
		t.Fatal("0400 refused", err)
	}
	if _, err := LoadSecret(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := New(NewMemory(), []byte("short")); err == nil {
		t.Fatal("short pepper accepted")
	}
	if Mask("abcdefgh") != "abcd…" || Mask("ab") != "…" {
		t.Fatal(Mask("abcdefgh"))
	}
}
