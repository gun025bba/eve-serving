package memory

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCheckBackupPolicy_DesktopDocuments(t *testing.T) {
	oldRun, oldOS, oldHome := tmutilRun, goos, homeDir
	defer func() { tmutilRun, goos, homeDir = oldRun, oldOS, oldHome }()
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	tmutilRun = func(_ context.Context, d string) (string, error) { return "[Excluded]    " + d + "\n", nil }
	goos = "darwin"
	for _, d := range []string{filepath.Join(home, "Desktop", "eve"), filepath.Join(home, "Documents"), filepath.Join(home, "documents", "x", "mem")} {
		if _, err := CheckBackupPolicy(d, false); !errors.Is(err, ErrSyncedDataDir) {
			t.Errorf("%s: %v", d, err)
		}
	}
	for _, d := range []string{filepath.Join(home, "Library", "Application Support", "eve"), filepath.Join(home, "DesktopX")} {
		if _, err := CheckBackupPolicy(d, false); errors.Is(err, ErrSyncedDataDir) {
			t.Errorf("%s refused", d)
		}
	}
}
