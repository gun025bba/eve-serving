package auth

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := map[string]bool{"Bearer k1": true, "bearer k1": true, "Bearer k2": false, "k1": false, "": false, "Basic k1": false}
	for h, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		if got := Check(r, "k1"); got != want {
			t.Errorf("%q: got %v", h, got)
		}
	}
}

func TestLoadKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	os.WriteFile(p, []byte(" abc\n"), 0o600)
	if k, err := LoadKey(p); err != nil || k != "abc" {
		t.Fatal(k, err)
	}
	os.WriteFile(p, []byte("\n"), 0o600)
	if _, err := LoadKey(p); err == nil {
		t.Fatal("want empty error")
	}
}
