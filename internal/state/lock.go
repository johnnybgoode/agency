// Package state manages persistent application state and file locking.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock represents an exclusive advisory lock on a file.
type Lock struct {
	f     *os.File
	path  string
	nonce string
}

// Nonce returns the random nonce written to the lock file when the lock was
// acquired. The nonce is stored in state.json (LockNonce) so that the next
// startup can cross-check the lock file contents and detect PID reuse.
func (l *Lock) Nonce() string {
	return l.nonce
}

// AcquireLock attempts to acquire an exclusive non-blocking flock on path.
// The parent directory is created if it does not exist. On success it writes
// a random nonce to the lock file; callers should persist the nonce to
// state.json (LockNonce) so that stale-lock detection can cross-check against
// it on the next startup. Returns an error if another process holds the lock.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}

	fd := int(f.Fd()) //nolint:gosec // file descriptor fits in int on all supported platforms
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("state is locked by another process: %s", path)
		}
		return nil, fmt.Errorf("acquiring lock %s: %w", path, err)
	}

	// Write a random nonce to the lock file. The nonce is stored in state.json
	// alongside the PID so that on the next startup we can verify the lock file
	// still contains our nonce before trusting IsProcessAlive — guarding against
	// PID reuse where a recycled PID would otherwise fool the stale-lock check.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		f.Close()
		return nil, fmt.Errorf("generating lock nonce: %w", err)
	}
	nonce := hex.EncodeToString(b)

	// Truncate before writing so a retry after a stale lock doesn't accumulate
	// old nonces in the file.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncating lock file: %w", err)
	}
	if _, err := fmt.Fprint(f, nonce); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing lock nonce: %w", err)
	}

	return &Lock{f: f, path: path, nonce: nonce}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	fd := int(l.f.Fd()) //nolint:gosec // file descriptor fits in int on all supported platforms
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return fmt.Errorf("releasing lock %s: %w", l.path, err)
	}
	return l.f.Close()
}
