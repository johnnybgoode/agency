package config

import "strings"

// Merge combines base and override configs into a new Config. For scalar
// fields, non-zero override values replace base values. For MCPServers, if any
// element is prefixed with "+", those elements are appended to the base list;
// otherwise a non-empty override list replaces the base list entirely. Neither
// input is mutated.
func Merge(base, override *Config) *Config {
	result := *base

	// Agent
	if override.Agent.Default != "" {
		result.Agent.Default = override.Agent.Default
	}
	if override.Agent.Permissions != "" {
		result.Agent.Permissions = override.Agent.Permissions
	}
	if override.Agent.Model != "" {
		result.Agent.Model = override.Agent.Model
	}
	result.Agent.MCPServers = mergeMCPServers(base.Agent.MCPServers, override.Agent.MCPServers)

	// Sandbox
	if override.Sandbox.Type != "" {
		result.Sandbox.Type = override.Sandbox.Type
	}
	if override.Sandbox.Image != "" {
		result.Sandbox.Image = override.Sandbox.Image
	}
	if override.Sandbox.Memory != "" {
		result.Sandbox.Memory = override.Sandbox.Memory
	}
	if override.Sandbox.CPUs != 0 {
		result.Sandbox.CPUs = override.Sandbox.CPUs
	}
	if override.Sandbox.Network != "" {
		result.Sandbox.Network = override.Sandbox.Network
	}
	if override.Sandbox.DockerfileDir != "" {
		result.Sandbox.DockerfileDir = override.Sandbox.DockerfileDir
	}

	// Credentials
	if override.Credentials.AnthropicAPIKey != "" {
		result.Credentials.AnthropicAPIKey = override.Credentials.AnthropicAPIKey
	}
	if override.Credentials.GithubToken != "" {
		result.Credentials.GithubToken = override.Credentials.GithubToken
	}

	// Worktree
	if override.Worktree.BranchPrefix != "" {
		result.Worktree.BranchPrefix = override.Worktree.BranchPrefix
	}
	if override.Worktree.AutoPush != nil {
		result.Worktree.AutoPush = override.Worktree.AutoPush
	}

	// TUI
	if override.TUI.SidebarWidth != 0 {
		result.TUI.SidebarWidth = override.TUI.SidebarWidth
	}
	if override.TUI.Theme.DangerBg != "" {
		result.TUI.Theme.DangerBg = override.TUI.Theme.DangerBg
	}
	if override.TUI.Theme.DangerFg != "" {
		result.TUI.Theme.DangerFg = override.TUI.Theme.DangerFg
	}

	return &result
}

// mergeMCPServers merges two MCP server lists. If any override element is
// prefixed with "+", those (stripped) elements are appended to base. If no
// element is prefixed and the override list is non-empty, it replaces base.
func mergeMCPServers(base, override []string) []string {
	if len(override) == 0 {
		// Preserve the base slice but don't share the backing array.
		result := make([]string, len(base))
		copy(result, base)
		return result
	}

	hasAppend := false
	for _, s := range override {
		if strings.HasPrefix(s, "+") {
			hasAppend = true
			break
		}
	}

	if !hasAppend {
		// Full replacement.
		result := make([]string, len(override))
		copy(result, override)
		return result
	}

	// Append mode: copy base then append stripped entries.
	result := make([]string, len(base))
	copy(result, base)
	for _, s := range override {
		if strings.HasPrefix(s, "+") {
			result = append(result, strings.TrimPrefix(s, "+"))
		}
	}
	return result
}
