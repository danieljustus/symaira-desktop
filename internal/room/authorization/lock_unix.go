//go:build !windows

package authorization

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(file *os.File, wait bool) error {
	flags := unix.LOCK_EX
	if !wait {
		flags |= unix.LOCK_NB
	}
	for {
		err := unix.Flock(int(file.Fd()), flags)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrBusy
		}
		return err
	}
}
func unlock(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
