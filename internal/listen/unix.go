// Package listen opens the gateway's trusted unix socket.
//
// Only requests arriving on this socket may carry Tailscale identity headers,
// so the socket must be reachable by `tailscale serve` and nobody else: the
// directory must be a real directory owned by the gateway user with mode 0700
// (no group/other bits), every ancestor must be root- or self-owned and not
// group/world-writable, and the socket is created 0660. Looser permissions
// abort startup.
package listen

import (
	"context"
	"net/http"
	"time"

	"fmt"
	"github.com/gun025bba/eve-serving/internal/auth"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// CheckDir verifies the socket directory: not a symlink, a directory, owned
// by the current uid, and no group/other permission bits.
func CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("unix socket dir: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("unix socket dir %s must be a real directory", dir)
	}
	if p := fi.Mode().Perm(); p&0o077 != 0 {
		return fmt.Errorf("unix socket dir %s has mode %#o; must be 0700 (no group/other access)", dir, p)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("unix socket dir %s must be owned by the gateway user (uid %d), not uid %d", dir, os.Getuid(), st.Uid)
	}
	return CheckAncestors(dir)
}

// CheckAncestors verifies every parent of dir (symlinks resolved) up to "/":
// each must be owned by root or the gateway user and must not be group- or
// world-writable. Otherwise another user could rename the socket dir away
// and plant their own.
func CheckAncestors(dir string) error {
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(dir)))
	if err != nil {
		return fmt.Errorf("unix socket dir parent: %w", err)
	}
	for p := parent; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("unix socket dir ancestor: %w", err)
		}
		if m := fi.Mode().Perm(); m&0o022 != 0 {
			return fmt.Errorf("unix socket dir ancestor %s has mode %#o; must not be group/world-writable", p, m)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Getuid() {
			return fmt.Errorf("unix socket dir ancestor %s is owned by uid %d; must be root or the gateway user", p, st.Uid)
		}
		if p == "/" || p == filepath.Dir(p) {
			return nil
		}
	}
}

// CheckSocket verifies the socket file is a socket with no bits beyond 0660.
func CheckSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("unix socket: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("unix socket %s is not a socket", path)
	}
	if p := fi.Mode().Perm(); p&^0o660 != 0 {
		return fmt.Errorf("unix socket %s has mode %#o; must be 0660 or stricter", path, p)
	}
	return nil
}

// Unix checks the directory, removes a stale socket (never any other file
// type), listens, sets 0660 and re-checks.
func Unix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("unix socket path %q must be absolute", path)
	}
	if err := CheckDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("unix socket path %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	old := syscall.Umask(0o117) // created at most 0660, no window with looser bits
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(true)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		l.Close()
		return nil, err
	}
	if err := CheckSocket(path); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// TrustedServer returns an http.Server for the unix listener: every
// connection it accepts is marked trusted, so Tailscale identity headers are
// honoured on it (and only on it).
func TrustedServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context { return auth.WithTrustedSocket(ctx) }}
}
