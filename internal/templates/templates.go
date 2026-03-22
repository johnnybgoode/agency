// Package templates embeds template subdirectories (e.g. docker, agent,
// config) directly into the binary. Each subdirectory of this package is
// accessible via [Sub].
package templates

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// files holds all template subdirectories embedded into the binary.
// Only named subdirectories are embedded to avoid including Go source files.
//
//go:embed docker tmux
var files embed.FS

// Sub returns an [fs.FS] rooted at the named subdirectory (e.g. "docker").
// It returns an error if the subdirectory does not exist in the embedded data.
func Sub(name string) (fs.FS, error) {
	return fs.Sub(files, name)
}

// WriteTmuxConf writes the embedded tmux/tmux.conf to dir/tmux.conf and
// returns the absolute path. Safe to call multiple times; overwrites silently.
func WriteTmuxConf(dir string) (string, error) {
	data, err := files.ReadFile("tmux/tmux.conf")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}
