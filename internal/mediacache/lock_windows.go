//go:build windows

package mediacache

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryLockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, ^uint32(0), ^uint32(0), &ol,
	)
}

func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &ol)
}

func isLockBusyErr(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
