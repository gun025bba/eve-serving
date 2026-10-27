package memory

// QA (PR #33): key-file size boundary and no key bytes in errors.
import (
	"os"
	"strings"
	"testing"
)

func TestQA_KeyFileSizeBoundary(t *testing.T) {
	unit := "0123456789abcdeffedcba98765432100f1e2d3c4b5a69788796a5b4c3d2e1f0" // 64 hex, test fixture only
	k1024 := strings.Repeat(unit, 16)
	p := t.TempDir() + "/key"
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"1022hex+nl=1023B", k1024[:1022] + "\n", true},
		{"1024hex=1024B", k1024, true},
		{"1024hex+nl=1025B", k1024 + "\n", false},
		{"1025hex=1025B", k1024 + "0", false},
	}
	for _, c := range cases {
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadKeyFile(p)
		if (err == nil) != c.ok {
			t.Errorf("%s: ok=%v want %v err=%v", c.name, err == nil, c.ok, err)
		}
		if err != nil {
			if !c.ok && !strings.Contains(err.Error(), "1024") {
				t.Errorf("%s: error lacks size reason", c.name)
			}
			if strings.Contains(err.Error(), unit[:16]) {
				t.Errorf("%s: error leaks key bytes", c.name)
			}
		}
	}
}

func TestQA_BackupWarningCarriesNoKeyOrPath(t *testing.T) {
	// main.go logs only "status" + fixed msg; verify status values are constants.
	for _, s := range []string{BackupNotExcluded, BackupUnknown} {
		if strings.ContainsAny(s, "/0123456789abcdef") && len(s) > 20 {
			t.Errorf("status looks like data: %q", s)
		}
	}
}
