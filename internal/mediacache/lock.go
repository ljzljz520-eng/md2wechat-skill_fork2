package mediacache

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ErrLockBusy means another process (or handle) holds the lock past timeout.
var ErrLockBusy = errors.New("mediacache: lock busy")

// lockRetryInterval bounds busy-wait polling for a contended lock.
const lockRetryInterval = 20 * time.Millisecond

// fileLock is an exclusive, process-shared advisory lock.
type fileLock struct {
	name string
	file *os.File
}

// acquireLock opens path and takes an exclusive lock, retrying until timeout.
func acquireLock(path string, timeout time.Duration) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := tryLockFile(f); err == nil {
			return &fileLock{name: path, file: f}, nil
		} else if !isLockBusyErr(err) {
			_ = f.Close()
			return nil, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockBusy
		}
		time.Sleep(lockRetryInterval)
	}
}

func (l *fileLock) unlock() error {
	_ = unlockFile(l.file)
	return l.file.Close()
}
