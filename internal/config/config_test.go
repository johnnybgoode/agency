package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceConfigPath(t *testing.T) {
	worktreePath := "/some/project/my-project-abc123"
	got := WorkspaceConfigPath(worktreePath)
	want := filepath.Join(worktreePath, ".agency", "config.toml")
	if got != want {
		t.Errorf("WorkspaceConfigPath(%q) = %q, want %q", worktreePath, got, want)
	}
}

func TestWorkspaceConfigPathIsInsideTool(t *testing.T) {
	// Verify the path ends with .agency/config.toml regardless of OS separator.
	worktreePath := "/a/b/c"
	got := WorkspaceConfigPath(worktreePath)
	suffix := filepath.Join(".agency", "config.toml")
	if !strings.HasSuffix(got, suffix) {
		t.Errorf("WorkspaceConfigPath(%q) = %q, want suffix %q", worktreePath, got, suffix)
	}
}

func TestLoadPermissionOK(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte("[agent]\ndefault = \"claude\"\n")
	if err := os.WriteFile(cfgPath, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Agent.Default != "claude" {
		t.Errorf("Agent.Default = %q, want %q", cfg.Agent.Default, "claude")
	}
}

func TestLoadMissingPathIsSkipped(t *testing.T) {
	// Nonexistent path should be silently skipped; defaults should be returned.
	cfg, err := Load("/nonexistent/path/that/does/not/exist.toml")
	if err != nil {
		t.Fatalf("Load of nonexistent path returned error: %v", err)
	}
	def := DefaultConfig()
	if cfg.Agent.Default != def.Agent.Default {
		t.Errorf("Agent.Default = %q, want default %q", cfg.Agent.Default, def.Agent.Default)
	}
}

func TestEnforceGlobalConfigPerms(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := EnforceGlobalConfigPerms(cfgPath); err != nil {
		t.Fatalf("EnforceGlobalConfigPerms returned error: %v", err)
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("Stat after enforce: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %04o, want 0o600", got)
	}
}

func TestEnforceGlobalConfigPermsNonexistent(t *testing.T) {
	err := EnforceGlobalConfigPerms("/nonexistent/path/does/not/exist.toml")
	if err != nil {
		t.Errorf("EnforceGlobalConfigPerms on nonexistent path returned error: %v", err)
	}
}

func TestEnforceGlobalConfigPermsAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Should be a no-op and return nil.
	if err := EnforceGlobalConfigPerms(cfgPath); err != nil {
		t.Fatalf("EnforceGlobalConfigPerms returned error: %v", err)
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %04o, want 0o600", got)
	}
}
