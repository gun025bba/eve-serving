package listen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shortDir(t *testing.T) string {
	// unix socket paths are limited to ~104 bytes on macOS
	d, err := os.MkdirTemp("/tmp", "evs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestUnixPerms(t *testing.T) {
	d := shortDir(t)
	os.Chmod(d, 0o755)
	if _, err := Unix(filepath.Join(d, "gw.sock")); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatal("0755 dir accepted:", err)
	}
	os.Chmod(d, 0o710)
	if _, err := Unix(filepath.Join(d, "gw.sock")); err == nil {
		t.Fatal("0710 dir accepted")
	}
	os.Chmod(d, 0o700)
	p := filepath.Join(d, "gw.sock")
	l, err := Unix(p)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode %#o", fi.Mode().Perm())
	}
	os.Chmod(p, 0o666)
	if err := CheckSocket(p); err == nil {
		t.Fatal("0666 socket accepted")
	}
	l.Close()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("socket not unlinked on close")
	}
	// a regular file at the path is never removed
	os.WriteFile(p, []byte("x"), 0o600)
	if _, err := Unix(p); err == nil {
		t.Fatal("regular file replaced")
	}
	os.Remove(p)
	// symlinked dir rejected
	ln := filepath.Join(shortDir(t), "link")
	os.Symlink(d, ln)
	if _, err := Unix(filepath.Join(ln, "gw.sock")); err == nil {
		t.Fatal("symlink dir accepted")
	}
	if _, err := Unix("rel.sock"); err == nil {
		t.Fatal("relative path accepted")
	}
}
