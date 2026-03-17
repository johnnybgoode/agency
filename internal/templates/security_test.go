package templates

import (
	"io/fs"
	"strings"
	"testing"
)

// --- Issue 12: go:embed must not include .go source files ---

func TestEmbeddedFS_NoGoSourceFiles(t *testing.T) {
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			t.Errorf("embedded FS contains Go source file %q; //go:embed should use explicit subdir patterns", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

func TestEmbeddedFS_ContainsDockerSubdir(t *testing.T) {
	// The docker subdir must still be present — this ensures we didn't
	// accidentally strip the useful content when narrowing the embed glob.
	entries, err := fs.ReadDir(files, "docker")
	if err != nil {
		t.Fatalf("docker subdir missing from embedded FS: %v", err)
	}
	if len(entries) == 0 {
		t.Error("embedded docker subdir is empty")
	}
}
