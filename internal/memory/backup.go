package memory

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Backup exclusion status of the memory data_dir (memory.md §9.1).
const (
	BackupExcluded    = "excluded"
	BackupNotExcluded = "not_excluded"
	BackupUnknown     = "unknown" // could not verify (no tmutil, error, not macOS)
)

// tmutilRun runs `tmutil isexcluded <dir>`; replaced in tests.
var tmutilRun = func(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmutil", "isexcluded", dir).Output()
	return string(out), err
}

var goos = runtime.GOOS

// BackupExclusion reports whether Time Machine excludes dir. It never changes
// anything (setting the exclusion is a runbook step, not a startup action).
// Callers log a warning for anything but BackupExcluded.
func BackupExclusion(dir string) string {
	if goos != "darwin" {
		return BackupUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tmutilRun(ctx, dir)
	if err != nil {
		return BackupUnknown
	}
	switch {
	case strings.Contains(out, "[Excluded]"):
		return BackupExcluded
	case strings.Contains(out, "[Included]"):
		return BackupNotExcluded
	}
	return BackupUnknown
}
