package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Issue 14: LockNonce prevents PID-reuse attacks ---

func TestAcquireLock_WritesNonEmptyNonce(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer lock.Release()

	nonce := lock.Nonce()
	if nonce == "" {
		t.Error("Lock.Nonce() should return a non-empty nonce after acquisition")
	}
}

func TestAcquireLock_NonceWrittenToLockFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer lock.Release()

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("ReadFile(lockPath) failed: %v", err)
	}

	fileNonce := strings.TrimSpace(string(data))
	if fileNonce == "" {
		t.Error("lock file should contain the nonce, got empty content")
	}
	if fileNonce != lock.Nonce() {
		t.Errorf("nonce in lock file %q does not match Lock.Nonce() %q", fileNonce, lock.Nonce())
	}
}

func TestAcquireLock_NonceIsUnique(t *testing.T) {
	dir := t.TempDir()

	lock1, err := AcquireLock(filepath.Join(dir, "lock1"))
	if err != nil {
		t.Fatalf("AcquireLock lock1: %v", err)
	}
	n1 := lock1.Nonce()
	lock1.Release()

	lock2, err := AcquireLock(filepath.Join(dir, "lock2"))
	if err != nil {
		t.Fatalf("AcquireLock lock2: %v", err)
	}
	n2 := lock2.Nonce()
	lock2.Release()

	if n1 == n2 {
		t.Errorf("consecutive AcquireLock calls should produce unique nonces, both got %q", n1)
	}
}

func TestAcquireLock_NonceChangesAfterReacquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock1, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	n1 := lock1.Nonce()
	lock1.Release()

	lock2, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("second AcquireLock: %v", err)
	}
	n2 := lock2.Nonce()
	lock2.Release()

	if n1 == n2 {
		t.Errorf("nonce should change on re-acquire to prevent PID-reuse attacks, got %q both times", n1)
	}
}
