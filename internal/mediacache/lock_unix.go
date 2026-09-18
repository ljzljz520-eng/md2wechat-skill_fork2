//go:build darwin || linux || freebsd

package mediacache

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryLockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func isLockBusyErr(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK)
}
