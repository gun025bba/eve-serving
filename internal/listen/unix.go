// Package listen opens the gateway's trusted unix socket.
//
// Only requests arriving on this socket may carry Tailscale identity headers,
// so the socket must be reachable by `tailscale serve` and nobody else: the
// directory must be a real directory owned by the gateway user with mode 0700
// (no group/other bits), and the socket is created 0660. Looser permissions
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
	return nil
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
