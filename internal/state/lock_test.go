package state

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}

func TestDoubleAcquireFails(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock1, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("first AcquireLock failed: %v", err)
	}
	defer lock1.Release() //nolint:errcheck

	_, err = AcquireLock(lockPath)
	if err == nil {
		t.Fatal("second AcquireLock should have failed, got nil error")
	}

	// The error message should be informative.
	if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "lock") {
		t.Errorf("error should mention lock: %v", err)
	}
}

func TestReleaseUnlocksForReacquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock1, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("first AcquireLock failed: %v", err)
	}

	if err := lock1.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	lock2, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}

	if err := lock2.Release(); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}
}

func TestAcquireCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "nested", "subdir", "test.lock")

	lock, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("AcquireLock with nonexistent parent dir failed: %v", err)
	}
	defer lock.Release() //nolint:errcheck
}
