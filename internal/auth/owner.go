package auth

import (
	"os"
	"syscall"
)

// ownedBySelf reports whether the file is owned by the process uid.
func ownedBySelf(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Getuid()
}
