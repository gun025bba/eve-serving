package memory

// QA (PR #42): fail-closed startup gate edge cases (stubbed tmutil, no real dirs).
import (
	"context"
	"errors"
	"testing"
)

func TestQA_CheckBackupPolicy_Edges(t *testing.T) {
	oldRun, oldOS := tmutilRun, goos
	defer func() { tmutilRun, goos = oldRun, oldOS }()
	dir := "/opt/eve-gw/memory"
	goos = "darwin"
	cases := []struct {
		name, out string
		ok        bool
	}{
		{"verified excluded", "[Excluded]    " + dir + "\n", true},
		{"excluded, trailing slash path", "[Excluded]    " + dir + "/\n", true},
		{"excluded but other path", "[Excluded]    /opt/eve-gw\n", false},
		{"included", "[Included]    " + dir + "\n", false},
		{"two lines", "[Excluded]    " + dir + "\n[Included]    " + dir + "\n", false},
		{"prefix junk", "warning [Excluded]    " + dir + "\n", false},
		{"no path", "[Excluded]\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		out := c.out
		tmutilRun = func(context.Context, string) (string, error) { return out, nil }
		if _, err := CheckBackupPolicy(dir, false); (err == nil) != c.ok {
			t.Errorf("darwin %s: err=%v", c.name, err)
		}
	}
	tmutilRun = func(context.Context, string) (string, error) { return "", errors.New("timeout") }
	if _, err := CheckBackupPolicy(dir, true); err == nil {
		t.Error("darwin tmutil error + opt-in must still refuse")
	}
	tmutilRun = func(context.Context, string) (string, error) { return "[Excluded]    x\n", nil }
	for _, d := range []string{"/Users/a/Google Drive/mem", "/Users/a/OneDrive/mem", "/Users/a/DROPBOX/mem", "/Users/a/Box Sync/mem"} {
		if _, err := CheckBackupPolicy(d, true); !errors.Is(err, ErrSyncedDataDir) {
			t.Errorf("synced %s: %v", d, err)
		}
	}
}
