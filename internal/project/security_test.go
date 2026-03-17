package project

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Issue 15: IsDir must not follow symlinks ---

func TestIsDir_DoesNotFollowSymlinkToDir(t *testing.T) {
	dir := t.TempDir()

	// Create a real directory elsewhere.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// Create a symlink pointing to the real directory.
	linkPath := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Skipf("symlink creation not supported (may require elevated privileges): %v", err)
	}

	// IsDir uses os.Lstat, so a symlink-to-dir must NOT be reported as a dir.
	if IsDir(linkPath) {
		t.Errorf("IsDir(%q) returned true for a symlink; must use Lstat to avoid symlink following", linkPath)
	}
}

func TestIsDir_TrueForRealDirectory(t *testing.T) {
	dir := t.TempDir()
	if !IsDir(dir) {
		t.Errorf("IsDir(%q) returned false for a real directory", dir)
	}
}

func TestIsDir_FalseForFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if IsDir(f) {
		t.Errorf("IsDir(%q) returned true for a regular file", f)
	}
}

func TestIsDir_FalseForNonExistent(t *testing.T) {
	if IsDir("/does/not/exist/at/all") {
		t.Error("IsDir returned true for non-existent path")
	}
}
