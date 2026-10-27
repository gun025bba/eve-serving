package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// homeDir is replaceable in tests.
var homeDir = os.UserHomeDir

// underDesktopDocs reports whether dir lies under ~/Desktop or ~/Documents,
// which iCloud "Desktop & Documents" may sync. The setting cannot be read
// reliably, so on macOS these paths are always refused (symlinks resolved).
// desktopDocsRe catches /Users/<name>/Desktop|Documents/ for any account,
// independent of $HOME (path is lower-cased before matching).
var desktopDocsRe = regexp.MustCompile(`/users/[^/]+/(desktop|documents)/`)

func underDesktopDocs(dir string) (bool, error) {
	h, err := homeDir()
	if err == nil && h == "" {
		err = errors.New("empty home")
	}
	if err != nil {
		return false, err // fail closed in the caller
	}
	cands := []string{filepath.Clean(dir)}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		cands = append(cands, r)
	}
	hs := []string{filepath.Clean(h)}
	if r, err := filepath.EvalSymlinks(h); err == nil {
		hs = append(hs, r)
	}
	for _, c := range cands {
		c = strings.ToLower(c) + "/"
		for _, hh := range hs {
			for _, sub := range []string{"desktop", "documents"} {
				if strings.HasPrefix(c, strings.ToLower(filepath.Join(hh, sub))+"/") {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// CheckBackupPolicy is the fail-closed startup gate while memory is on:
//   - data_dir under an iCloud/Dropbox/OneDrive/Google Drive folder → refuse;
//   - macOS: Time Machine must report the dir [Excluded] (not_excluded or
//     unknown → refuse);
//   - other OS: refuse unless allowUnverified (explicit operator opt-in).
func CheckBackupPolicy(dir string, allowUnverified bool) (status string, err error) {
	low := strings.ToLower(filepath.Clean(dir)) + "/"
	if desktopDocsRe.MatchString(low) {
		return BackupUnknown, ErrSyncedDataDir
	}
	if goos == "darwin" {
		in, err := underDesktopDocs(dir)
		if err != nil {
			return BackupUnknown, fmt.Errorf("%w: home directory unknown (%v)", ErrSyncedDataDir, err)
		}
		if in {
			return BackupUnknown, ErrSyncedDataDir
		}
	}
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
