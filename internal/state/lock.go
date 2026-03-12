package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock represents an exclusive advisory lock on a file.
type Lock struct {
	f    *os.File
	path string
}

// AcquireLock attempts to acquire an exclusive non-blocking flock on path.
// The parent directory is created if it does not exist. Returns an error if
// another process holds the lock.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}

	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("state is locked by another process: %s", path)
		}
		return nil, fmt.Errorf("acquiring lock %s: %w", path, err)
	}

	return &Lock{f: f, path: path}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	fd := int(l.f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return fmt.Errorf("releasing lock %s: %w", l.path, err)
	}
	return l.f.Close()
}
