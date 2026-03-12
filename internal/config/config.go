package config

import (
	"fmt"
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
	Type   string `toml:"type"`
	Image  string `toml:"image"`
	Memory string `toml:"memory"`
	CPUs   int    `toml:"cpus"`
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

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Agent: AgentConfig{
			Default: "claude",
		},
		Sandbox: SandboxConfig{
			Type:   "docker",
			Image:  "claude-sandbox:latest",
			Memory: "4g",
			CPUs:   2,
		},
		Worktree: WorktreeConfig{
			BranchPrefix: "agent/",
		},
	}
}

// GlobalConfigPath returns the path to the global config file.
func GlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.config/agency/config.toml"
}

// ProjectConfigPath returns the path to the project-level config file.
func ProjectConfigPath(projectDir string) string {
	return projectDir + "/.tool/config.toml"
}

// SessionConfigPath returns the path to the session-local config file inside a worktree.
func SessionConfigPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".tool", "config.toml")
}

// EnforceGlobalConfigPerms chmod 0600s the global config file if it exists.
func EnforceGlobalConfigPerms(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Chmod(path, 0600)
}

// Load reads configuration from the given paths in order, merging each into
// the defaults. Paths that do not exist are silently skipped. Credential
// fields in any path after the first trigger a warning to stderr.
func Load(paths ...string) (*Config, error) {
	base := DefaultConfig()

	for i, path := range paths {
		// For the global config (first path), check file permissions.
		if i == 0 {
			if info, err := os.Stat(path); err == nil {
				perm := info.Mode().Perm()
				if perm&0177 != 0 {
					fmt.Fprintf(os.Stderr, "warning: global config %s has permissions %04o, should be 0600\n", path, perm)
				}
			}
		}

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}

		var override Config
		if err := toml.Unmarshal(data, &override); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}

		if i != 0 {
			if override.Credentials.AnthropicAPIKey != "" || override.Credentials.GithubToken != "" {
				fmt.Fprintf(os.Stderr, "warning: credentials found in %s — consider storing credentials only in the global config\n", path)
			}
		}

		base = Merge(base, &override)
	}

	// Fall back to host environment variables for unset credentials.
	if base.Credentials.AnthropicAPIKey == "" {
		base.Credentials.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if base.Credentials.GithubToken == "" {
		base.Credentials.GithubToken = os.Getenv("GITHUB_TOKEN")
	}

	return base, nil
}
