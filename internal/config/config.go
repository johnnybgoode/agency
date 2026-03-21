// Package config handles TOML configuration loading and merging.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config holds all configuration for the agency tool.
type Config struct {
	Agent    AgentConfig    `toml:"agent"`
	Sandbox  SandboxConfig  `toml:"sandbox"`
	Worktree WorktreeConfig `toml:"worktree"`
	TUI      TUIConfig      `toml:"tui"`
}

// AgentConfig holds agent-specific configuration.
type AgentConfig struct {
	Default     string   `toml:"default"`
	Permissions string   `toml:"permissions"`
	Model       string   `toml:"model"`
	MCPServers  []string `toml:"mcp_servers"`
}

// SandboxConfig holds sandbox-specific configuration.
type SandboxConfig struct {
	Image         string `toml:"image"`          // image used as sandbox template (default "agency:latest")
	DockerfileDir string `toml:"dockerfile_dir"` // optional custom Dockerfile location
}

// WorktreeConfig holds git worktree configuration.
type WorktreeConfig struct {
	BranchPrefix string `toml:"branch_prefix"`
	AutoPush     *bool  `toml:"auto_push,omitempty"`
}

// ThemeConfig holds color settings for TUI elements.
type ThemeConfig struct {
	DangerBg string `toml:"danger_bg"` // ANSI color for danger modal background (default "9")
	DangerFg string `toml:"danger_fg"` // ANSI color for danger modal foreground (default "15")
}

// TUIConfig holds TUI-specific configuration.
type TUIConfig struct {
	SidebarWidth int         `toml:"sidebar_width"`
	Theme        ThemeConfig `toml:"theme"`
}

// DefaultSidebarWidth is the default sidebar width as a percentage of
// terminal width. User-provided values also represent percentages.
const DefaultSidebarWidth = 15

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Agent: AgentConfig{
			Default: "claude",
		},
		Sandbox: SandboxConfig{
			Image: "agency:latest",
		},
		Worktree: WorktreeConfig{
			BranchPrefix: "",
		},
		TUI: TUIConfig{
			SidebarWidth: DefaultSidebarWidth,
			Theme: ThemeConfig{
				DangerBg: "9",
				DangerFg: "15",
			},
		},
	}
}

// GlobalConfigPath returns the path to the global config file.
func GlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "agency", "config.toml")
}

// ProjectConfigPath returns the path to the project-level config file.
func ProjectConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".agency", "config.toml")
}

// WorkspaceConfigPath returns the path to the workspace-local config file inside a worktree.
func WorkspaceConfigPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".agency", "config.toml")
}

// EnforceGlobalConfigPerms chmod 0600s the global config file if it exists.
func EnforceGlobalConfigPerms(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Chmod(path, 0o600)
}

// Load reads configuration from the given paths in order, merging each into
// the defaults. Paths that do not exist are silently skipped.
func Load(paths ...string) (*Config, error) {
	slog.Debug("loading config", "paths", paths)
	base := DefaultConfig()

	for i, path := range paths {
		// Attempt to auto-fix permissions on the global config before reading.
		if i == 0 {
			_ = EnforceGlobalConfigPerms(path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Debug("config file not found, skipping", "path", path)
				continue
			}
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		slog.Debug("config file loaded", "path", path)

		var override Config
		if err := toml.Unmarshal(data, &override); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}

		base = Merge(base, &override)
	}

	return base, nil
}
