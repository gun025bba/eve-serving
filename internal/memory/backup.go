package memory

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Backup exclusion status of the memory data_dir (memory.md §9.1).
const (
	BackupExcluded    = "excluded"
	BackupNotExcluded = "not_excluded"
	BackupUnknown     = "unknown" // could not verify (no tmutil, error, unexpected output, not macOS)
)

// TmutilPath is pinned: PATH is never consulted.
const TmutilPath = "/usr/bin/tmutil"

// tmutilRun runs `/usr/bin/tmutil isexcluded <dir>`; replaced in tests.
var tmutilRun = func(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, TmutilPath, "isexcluded", dir).Output()
	return string(out), err
}

var goos = runtime.GOOS

// isexcluded prints exactly one line: "[Excluded]<spaces><path>".
var tmLine = regexp.MustCompile(`^\[(Excluded|Included)\]\s+(\S.*)$`)

// BackupExclusion reports whether Time Machine excludes dir. It never changes
// anything (setting the exclusion is a runbook step). Only an exact,
// single-line answer about dir itself counts; anything else is unknown.
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
	out = strings.TrimSuffix(out, "\n")
	if strings.Contains(out, "\n") {
		return BackupUnknown
	}
	m := tmLine.FindStringSubmatch(out)
	if m == nil || filepath.Clean(m[2]) != filepath.Clean(dir) {
		return BackupUnknown
	}
	if m[1] == "Excluded" {
		return BackupExcluded
	}
	return BackupNotExcluded
}

// Path segments of cloud-sync folders; data_dir must not live under them.
var syncMarkers = []string{"/library/mobile documents/", "/library/cloudstorage/", "/icloud drive/", "/dropbox/", "/onedrive", "/google drive/", "/googledrive/", "/box sync/"}

var (
	ErrBackupNotExcluded = errors.New("memory: data_dir is not confirmed excluded from Time Machine")
	ErrBackupNonDarwin   = errors.New("memory: backup exclusion cannot be verified on this OS; set memory.allow_unverified_backup to opt in")
	ErrSyncedDataDir     = errors.New("memory: data_dir is inside a cloud-sync folder")
)

// CheckBackupPolicy is the fail-closed startup gate while memory is on:
//   - data_dir under an iCloud/Dropbox/OneDrive/Google Drive folder → refuse;
//   - macOS: Time Machine must report the dir [Excluded] (not_excluded or
//     unknown → refuse);
//   - other OS: refuse unless allowUnverified (explicit operator opt-in).
func CheckBackupPolicy(dir string, allowUnverified bool) (status string, err error) {
	low := strings.ToLower(filepath.Clean(dir)) + "/"
	for _, m := range syncMarkers {
		if strings.Contains(low, m) {
			return BackupUnknown, ErrSyncedDataDir
		}
	}
	st := BackupExclusion(dir)
	switch {
	case st == BackupExcluded:
		return st, nil
	case goos == "darwin":
		return st, fmt.Errorf("%w (status %s)", ErrBackupNotExcluded, st)
	case allowUnverified:
		return st, nil
	}
	return st, ErrBackupNonDarwin
}
