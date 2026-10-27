package session

import (
	"errors"
	"os"
	"syscall"
)

// OwnedBySelf fails unless the file is owned by the process uid.
func OwnedBySelf(st os.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() {
		return errors.New("not owned by the gateway's own uid")
	}
	return nil
}
