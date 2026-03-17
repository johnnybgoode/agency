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
	Agent       AgentConfig       `toml:"agent"`
	Sandbox     SandboxConfig     `toml:"sandbox"`
	Credentials CredentialsConfig `toml:"credentials"`
	Worktree    WorktreeConfig    `toml:"worktree"`
	TUI         TUIConfig         `toml:"tui"`
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
	Type          string `toml:"type"`
	Image         string `toml:"image"`
	Memory        string `toml:"memory"`
	CPUs          int    `toml:"cpus"`
	DockerfileDir string `toml:"dockerfile_dir"`
}

// CredentialsConfig holds sensitive credential configuration.
type CredentialsConfig struct {
	AnthropicAPIKey string `toml:"anthropic_api_key"`
	GithubToken     string `toml:"github_token"`
}

// WorktreeConfig holds git worktree configuration.
type WorktreeConfig struct {
	BranchPrefix string `toml:"branch_prefix"`
	AutoPush     *bool  `toml:"auto_push,omitempty"`
}

// TUIConfig holds TUI-specific configuration.
type TUIConfig struct {
	SidebarWidth int `toml:"sidebar_width"`
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
			Type:   "docker",
			Image:  "agency:latest",
			Memory: "4g",
			CPUs:   2,
		},
		Worktree: WorktreeConfig{
			BranchPrefix: "",
		},
		TUI: TUIConfig{
			SidebarWidth: DefaultSidebarWidth,
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
// the defaults. Paths that do not exist are silently skipped. Credential
// fields in any path after the first trigger a warning to stderr.
func Load(paths ...string) (*Config, error) {
	slog.Debug("loading config", "paths", paths)
	base := DefaultConfig()

	for i, path := range paths {
		// For the global config (first path), check file permissions.
		if i == 0 {
			if info, err := os.Stat(path); err == nil {
				perm := info.Mode().Perm()
				if perm&0o177 != 0 {
					slog.Warn("insecure config permissions", "path", path, "permissions", fmt.Sprintf("0o%o", perm))
				}
			}
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

		if i != 0 {
			if override.Credentials.AnthropicAPIKey != "" || override.Credentials.GithubToken != "" {
				slog.Warn("credentials in non-global config", "path", path)
			}
		}

		base = Merge(base, &override)
	}

	// Fall back to host environment variables for unset credentials.
	if base.Credentials.AnthropicAPIKey == "" {
		base.Credentials.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
		if base.Credentials.AnthropicAPIKey != "" {
			slog.Debug("credential from env", "key", "ANTHROPIC_API_KEY")
		}
	}
	if base.Credentials.GithubToken == "" {
		base.Credentials.GithubToken = os.Getenv("GITHUB_TOKEN")
		if base.Credentials.GithubToken != "" {
			slog.Debug("credential from env", "key", "GITHUB_TOKEN")
		}
	}

	return base, nil
}
