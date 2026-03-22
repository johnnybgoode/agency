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
//go:embed docker tmux claude
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

// WriteClaudeHooks writes the embedded Claude Code hook script and settings
// into worktreeDir/.claude/. The hook script is always overwritten; settings.json
// is only written if it does not already exist (to preserve user customizations).
func WriteClaudeHooks(worktreeDir string) error {
	hooksDir := filepath.Join(worktreeDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return err
	}

	// Always write the hook script (overwrite is safe — it's our template).
	hookData, err := files.ReadFile("claude/hooks/write-agent-status.js")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "write-agent-status.js"), hookData, 0o600); err != nil {
		return err
	}

	// Only write settings.json if it doesn't already exist.
	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		settingsData, readErr := files.ReadFile("claude/settings.json")
		if readErr != nil {
			return readErr
		}
		if err := os.WriteFile(settingsPath, settingsData, 0o600); err != nil {
			return err
		}
	}

	return nil
}
