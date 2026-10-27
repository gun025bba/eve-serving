package memory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestBackupExclusion(t *testing.T) {
	oldRun, oldOS := tmutilRun, goos
	defer func() { tmutilRun, goos = oldRun, oldOS }()
	goos = "darwin"
	cases := []struct {
		out  string
		err  error
		want string
	}{
		{"[Excluded]    /opt/eve-gw/memory\n", nil, BackupExcluded},
		{"[Included]    /opt/eve-gw/memory\n", nil, BackupNotExcluded},
		{"", errors.New("no tmutil"), BackupUnknown},
		{"garbage", nil, BackupUnknown},
		{"[Excluded]    /other/dir\n", nil, BackupUnknown},
		{"[Excluded]    /opt/eve-gw/memory\n[Included]    /opt/eve-gw/memory\n", nil, BackupUnknown},
		{"note: [Excluded]    /opt/eve-gw/memory\n", nil, BackupUnknown},
	}
	for _, c := range cases {
		tmutilRun = func(context.Context, string) (string, error) { return c.out, c.err }
		if got := BackupExclusion("/opt/eve-gw/memory"); got != c.want {
			t.Errorf("%q: got %s want %s", c.out, got, c.want)
		}
	}
	goos = "linux"
	if got := BackupExclusion("/x"); got != BackupUnknown {
		t.Errorf("non-darwin: %s", got)
	}
}

// #31: a key file over 1024 bytes is rejected, not truncated.
func TestMemoryKey_RejectsOversizeFile(t *testing.T) {
	good := "0123456789abcdeffedcba98765432100f1e2d3c4b5a69788796a5b4c3d2e1f0"
	p := t.TempDir() + "/key"
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(good + "\n")
	if _, err := ReadKeyFile(p); err != nil {
		t.Fatalf("good key: %v", err)
	}
	write(good + strings.Repeat("0", MaxKeyFileBytes)) // valid prefix, oversize
	if _, err := ReadKeyFile(p); err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("oversize accepted: %v", err)
	}
}

func TestCheckBackupPolicy_FailClosed(t *testing.T) {
	oldRun, oldOS := tmutilRun, goos
	defer func() { tmutilRun, goos = oldRun, oldOS }()
	dir := "/opt/eve-gw/memory"
	goos = "darwin"
	for out, ok := range map[string]bool{"[Excluded]    " + dir + "\n": true, "[Included]    " + dir + "\n": false, "": false} {
		tmutilRun = func(context.Context, string) (string, error) { return out, nil }
		if _, err := CheckBackupPolicy(dir, true); (err == nil) != ok {
			t.Errorf("darwin %q: err=%v", out, err)
		}
	}
	tmutilRun = func(context.Context, string) (string, error) { return "", errors.New("missing") }
	if _, err := CheckBackupPolicy(dir, true); err == nil {
		t.Error("darwin unknown must refuse even with opt-in")
	}
	goos = "linux"
	if _, err := CheckBackupPolicy(dir, false); !errors.Is(err, ErrBackupNonDarwin) {
		t.Error("non-darwin without opt-in must refuse", err)
	}
	if _, err := CheckBackupPolicy(dir, true); err != nil {
		t.Error("non-darwin opt-in", err)
	}
	for _, d := range []string{"/Users/a/Library/Mobile Documents/com~apple~CloudDocs/mem", "/Users/a/Library/CloudStorage/OneDrive-x/mem", "/Users/a/Dropbox/mem"} {
		if _, err := CheckBackupPolicy(d, true); !errors.Is(err, ErrSyncedDataDir) {
			t.Error(d, err)
		}
	}
}

func TestTmutilPathPinned(t *testing.T) {
	if TmutilPath != "/usr/bin/tmutil" {
		t.Fatal(TmutilPath)
	}
}
