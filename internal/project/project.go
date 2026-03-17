// Package project provides shared utilities for locating an agency project root.
package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindProjectDir walks up from the current working directory looking for a
// .agency/ or .bare/ directory, which marks an agency project root.
func FindProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	dir := cwd
	for {
		if IsDir(filepath.Join(dir, ".agency")) || IsDir(filepath.Join(dir, ".bare")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf(
		"not in an agency project (no .agency/ or .bare/ found); run 'agency init' first",
	)
}

// IsDir reports whether path exists and is a directory (not following symlinks).
func IsDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}
