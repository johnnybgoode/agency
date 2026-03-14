package config

import (
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestMergeMCPServers(t *testing.T) {
	tests := []struct {
		name     string
		base     []string
		override []string
		want     []string
	}{
		{
			name:     "empty override preserves base",
			base:     []string{"server-a", "server-b"},
			override: []string{},
			want:     []string{"server-a", "server-b"},
		},
		{
			name:     "nil override preserves base",
			base:     []string{"server-a"},
			override: nil,
			want:     []string{"server-a"},
		},
		{
			name:     "replacement when no plus prefix",
			base:     []string{"server-a", "server-b"},
			override: []string{"server-c"},
			want:     []string{"server-c"},
		},
		{
			name:     "append with plus prefix",
			base:     []string{"server-a", "server-b"},
			override: []string{"+server-c"},
			want:     []string{"server-a", "server-b", "server-c"},
		},
		{
			name:     "mixed plus and non-plus all treated as append mode",
			base:     []string{"server-a"},
			override: []string{"+server-b", "+server-c"},
			want:     []string{"server-a", "server-b", "server-c"},
		},
		{
			name:     "empty base with replacement",
			base:     []string{},
			override: []string{"server-x"},
			want:     []string{"server-x"},
		},
		{
			name:     "empty base with append",
			base:     []string{},
			override: []string{"+server-x"},
			want:     []string{"server-x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeMCPServers(tt.base, tt.override)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeMCPServers(%v, %v) = %v, want %v", tt.base, tt.override, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mergeMCPServers index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

//nolint:gocyclo // table-driven test; complexity from coverage breadth, not nested logic
func TestMerge(t *testing.T) {
	t.Run("scalar override non-zero replaces base", func(t *testing.T) {
		base := &Config{
			Agent: AgentConfig{
				Default:     "claude",
				Permissions: "base-perms",
				Model:       "base-model",
			},
			Sandbox: SandboxConfig{
				Type:   "docker",
				Image:  "base-image",
				Memory: "4g",
				CPUs:   2,
			},
			Credentials: CredentialsConfig{
				AnthropicAPIKey: "base-key",
				GithubToken:     "base-token",
			},
			Worktree: WorktreeConfig{
				BranchPrefix: "agent/",
			},
		}
		override := &Config{
			Agent: AgentConfig{
				Default:     "gpt",
				Permissions: "override-perms",
				Model:       "override-model",
			},
			Sandbox: SandboxConfig{
				Type:   "podman",
				Image:  "override-image",
				Memory: "8g",
				CPUs:   4,
			},
			Credentials: CredentialsConfig{
				AnthropicAPIKey: "override-key",
				GithubToken:     "override-token",
			},
			Worktree: WorktreeConfig{
				BranchPrefix: "dev/",
			},
		}

		result := Merge(base, override)

		if result.Agent.Default != "gpt" {
			t.Errorf("Agent.Default = %q, want %q", result.Agent.Default, "gpt")
		}
		if result.Agent.Permissions != "override-perms" {
			t.Errorf("Agent.Permissions = %q, want %q", result.Agent.Permissions, "override-perms")
		}
		if result.Agent.Model != "override-model" {
			t.Errorf("Agent.Model = %q, want %q", result.Agent.Model, "override-model")
		}
		if result.Sandbox.Type != "podman" {
			t.Errorf("Sandbox.Type = %q, want %q", result.Sandbox.Type, "podman")
		}
		if result.Sandbox.Image != "override-image" {
			t.Errorf("Sandbox.Image = %q, want %q", result.Sandbox.Image, "override-image")
		}
		if result.Sandbox.Memory != "8g" {
			t.Errorf("Sandbox.Memory = %q, want %q", result.Sandbox.Memory, "8g")
		}
		if result.Sandbox.CPUs != 4 {
			t.Errorf("Sandbox.CPUs = %d, want %d", result.Sandbox.CPUs, 4)
		}
		if result.Credentials.AnthropicAPIKey != "override-key" {
			t.Errorf("Credentials.AnthropicAPIKey = %q, want %q", result.Credentials.AnthropicAPIKey, "override-key")
		}
		if result.Worktree.BranchPrefix != "dev/" {
			t.Errorf("Worktree.BranchPrefix = %q, want %q", result.Worktree.BranchPrefix, "dev/")
		}
	})

	t.Run("scalar zero values preserve base", func(t *testing.T) {
		base := &Config{
			Agent: AgentConfig{
				Default: "claude",
				Model:   "base-model",
			},
			Sandbox: SandboxConfig{
				Type:  "docker",
				CPUs:  2,
				Image: "base-image",
			},
		}
		override := &Config{}

		result := Merge(base, override)

		if result.Agent.Default != "claude" {
			t.Errorf("Agent.Default = %q, want %q", result.Agent.Default, "claude")
		}
		if result.Agent.Model != "base-model" {
			t.Errorf("Agent.Model = %q, want %q", result.Agent.Model, "base-model")
		}
		if result.Sandbox.Type != "docker" {
			t.Errorf("Sandbox.Type = %q, want %q", result.Sandbox.Type, "docker")
		}
		if result.Sandbox.CPUs != 2 {
			t.Errorf("Sandbox.CPUs = %d, want %d", result.Sandbox.CPUs, 2)
		}
	})

	t.Run("AutoPush nil means no override", func(t *testing.T) {
		trueVal := true
		base := &Config{
			Worktree: WorktreeConfig{
				AutoPush: &trueVal,
			},
		}
		override := &Config{
			Worktree: WorktreeConfig{
				AutoPush: nil,
			},
		}

		result := Merge(base, override)

		if result.Worktree.AutoPush == nil {
			t.Fatal("AutoPush should not be nil")
		}
		if *result.Worktree.AutoPush != true {
			t.Errorf("AutoPush = %v, want true", *result.Worktree.AutoPush)
		}
	})

	t.Run("AutoPush &false overrides &true", func(t *testing.T) {
		base := &Config{
			Worktree: WorktreeConfig{
				AutoPush: boolPtr(true),
			},
		}
		override := &Config{
			Worktree: WorktreeConfig{
				AutoPush: boolPtr(false),
			},
		}

		result := Merge(base, override)

		if result.Worktree.AutoPush == nil {
			t.Fatal("AutoPush should not be nil")
		}
		if *result.Worktree.AutoPush != false {
			t.Errorf("AutoPush = %v, want false", *result.Worktree.AutoPush)
		}
	})

	t.Run("nested MCPServers delegation append", func(t *testing.T) {
		base := &Config{
			Agent: AgentConfig{
				MCPServers: []string{"server-a"},
			},
		}
		override := &Config{
			Agent: AgentConfig{
				MCPServers: []string{"+server-b"},
			},
		}

		result := Merge(base, override)

		if len(result.Agent.MCPServers) != 2 {
			t.Fatalf("MCPServers len = %d, want 2", len(result.Agent.MCPServers))
		}
		if result.Agent.MCPServers[0] != "server-a" {
			t.Errorf("MCPServers[0] = %q, want %q", result.Agent.MCPServers[0], "server-a")
		}
		if result.Agent.MCPServers[1] != "server-b" {
			t.Errorf("MCPServers[1] = %q, want %q", result.Agent.MCPServers[1], "server-b")
		}
	})

	t.Run("nested MCPServers delegation replacement", func(t *testing.T) {
		base := &Config{
			Agent: AgentConfig{
				MCPServers: []string{"server-a", "server-b"},
			},
		}
		override := &Config{
			Agent: AgentConfig{
				MCPServers: []string{"server-c"},
			},
		}

		result := Merge(base, override)

		if len(result.Agent.MCPServers) != 1 {
			t.Fatalf("MCPServers len = %d, want 1", len(result.Agent.MCPServers))
		}
		if result.Agent.MCPServers[0] != "server-c" {
			t.Errorf("MCPServers[0] = %q, want %q", result.Agent.MCPServers[0], "server-c")
		}
	})

	t.Run("merge does not mutate base", func(t *testing.T) {
		base := &Config{
			Agent: AgentConfig{
				Default: "claude",
			},
		}
		override := &Config{
			Agent: AgentConfig{
				Default: "gpt",
			},
		}

		_ = Merge(base, override)

		if base.Agent.Default != "claude" {
			t.Errorf("base mutated: Agent.Default = %q, want %q", base.Agent.Default, "claude")
		}
	})
}
