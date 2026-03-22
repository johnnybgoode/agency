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

	// Merge our Stop hook into settings.json, preserving existing content.
	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.json")
	if err := mergeStopHook(settingsPath); err != nil {
		return err
	}

	return nil
}

// agencyHookCommand is the command string used to identify our hook entry.
const agencyHookCommand = "node .claude/hooks/write-agent-status.js"

// mergeStopHook ensures the agency Stop hook is registered in the settings file.
// If the file doesn't exist, it creates it with just the hook. If it exists,
// it parses the JSON, adds a matcher entry for our hook if not already present,
// and writes back with the rest of the file preserved.
//
// Claude Code hook schema: each event key contains an array of matcher objects,
// where each matcher has a "matcher" string and a "hooks" array of commands:
//
//	{"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "..."}]}]}}
func mergeStopHook(settingsPath string) error {
	settings := make(map[string]any)

	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			// Malformed JSON — don't touch it.
			return nil
		}
	}

	// Navigate to hooks.Stop, creating intermediate structure as needed.
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	stopMatchers, _ := hooks["Stop"].([]any)

	// Check if our hook command is already registered in any matcher entry.
	for _, matcher := range stopMatchers {
		m, ok := matcher.(map[string]any)
		if !ok {
			continue
		}
		hooksList, _ := m["hooks"].([]any)
		for _, h := range hooksList {
			if entry, ok := h.(map[string]any); ok {
				if cmd, _ := entry["command"].(string); cmd == agencyHookCommand {
					return nil // already present
				}
			}
		}
	}

	// Append a new matcher entry with our hook.
	stopMatchers = append(stopMatchers, map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": agencyHookCommand,
			},
		},
	})
	hooks["Stop"] = stopMatchers

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o600)
}
