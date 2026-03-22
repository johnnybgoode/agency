// Package templates embeds template subdirectories (e.g. docker, agent,
// config) directly into the binary. Each subdirectory of this package is
// accessible via [Sub].
package templates

import (
	"embed"
	"encoding/json"
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

// WriteClaudeHooks writes the embedded Claude Code statusline script and settings
// into worktreeDir/.claude/. The script is always overwritten; the statusline
// command in settings.json is only added if one isn't already configured.
func WriteClaudeHooks(worktreeDir string) error {
	hooksDir := filepath.Join(worktreeDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return err
	}

	// Always write the script (overwrite is safe — it's our template).
	hookData, err := files.ReadFile("claude/hooks/write-agent-status.cjs")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "write-agent-status.cjs"), hookData, 0o600); err != nil {
		return err
	}

	// Ensure the statusline command is registered in settings.json.
	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.json")
	if err := mergeStatusline(settingsPath); err != nil {
		return err
	}

	return nil
}

// agencyStatuslineCommand is the command registered as the statusline.
const agencyStatuslineCommand = "node .claude/hooks/write-agent-status.cjs"

// mergeStatusline ensures the agency statusline is registered in settings.json.
// If the file doesn't exist, it creates it. If it exists but already has a
// statusline configured, it leaves the existing one in place. If it exists
// without a statusline, it adds ours while preserving all other settings.
func mergeStatusline(settingsPath string) error {
	settings := make(map[string]any)

	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			// Malformed JSON — don't touch it.
			return nil
		}
	}

	// Don't overwrite an existing statusline configuration.
	if settings["statusLine"] != nil {
		return nil
	}

	settings["statusLine"] = map[string]any{
		"type":    "command",
		"command": agencyStatuslineCommand,
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o600)
}
