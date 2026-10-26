package listen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shortDir(t *testing.T) string {
	// /tmp is world-writable (and fails CheckAncestors), so use a dir under
	// the package dir; keep it short for the ~104-byte macOS path limit.
	d, err := os.MkdirTemp(".", "s")
	if err == nil {
		d, err = filepath.Abs(d)
	}
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(d, 0o700)
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

func TestUnixAncestors(t *testing.T) {
	base := shortDir(t)
	mid := filepath.Join(base, "m")
	run := filepath.Join(mid, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(run, 0o700)
	os.Chmod(mid, 0o755)
	if err := CheckDir(run); err != nil {
		t.Fatal("good tree rejected:", err)
	}
	for _, m := range []os.FileMode{0o775, 0o757, 0o1777} {
		os.Chmod(mid, m)
		if err := CheckDir(run); err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Fatalf("ancestor mode %#o accepted: %v", m, err)
		}
		if _, err := Unix(filepath.Join(run, "gw.sock")); err == nil {
			t.Fatalf("Unix accepted ancestor mode %#o", m)
		}
	}
	os.Chmod(mid, 0o755)
	// world-writable /tmp-style ancestor
	if d, err := os.MkdirTemp("/tmp", "evs"); err == nil {
		defer os.RemoveAll(d)
		os.Chmod(d, 0o700)
		if st, _ := os.Stat("/tmp"); st != nil && st.Mode().Perm()&0o002 != 0 {
			if err := CheckDir(d); err == nil {
				t.Fatal("dir under world-writable /tmp accepted")
			}
		}
	}
}
