package config

import (
	"bytes"
	"log/slog"
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

// captureSlog redirects slog to a buffer for the duration of fn, returning whatever was logged.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	fn()
	return buf.String()
}

func TestLoadPermissionWarning(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Write a config with credentials and world-readable permissions (0o644).
	// EnforceGlobalConfigPerms is called by Load for the first path, but we
	// set permissions after the chmod attempt by creating the file then
	// explicitly chmod-ing it back to 0o644 to simulate a race or second path.
	content := []byte("[credentials]\nanthropic_api_key = \"sk-test\"\n")
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Force back to insecure perms to bypass the auto-fix (simulates path[1]).
	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	// Use a second path slot so EnforceGlobalConfigPerms does not fix it.
	placeholderPath := filepath.Join(dir, "placeholder.toml")

	var loadErr error
	logOutput := captureSlog(t, func() {
		_, loadErr = Load(placeholderPath, cfgPath)
	})

	if loadErr != nil {
		t.Fatalf("Load returned unexpected error: %v", loadErr)
	}

	if !strings.Contains(logOutput, "refusing") {
		t.Errorf("expected a refusal log for 0o644 permissions with credentials, got: %q", logOutput)
	}
	if !strings.Contains(logOutput, "0o644") {
		t.Errorf("expected log to mention the actual permission 0o644, got: %q", logOutput)
	}
}

func TestLoadPermissionOK(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte("[agent]\ndefault = \"claude\"\n")
	if err := os.WriteFile(cfgPath, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var loadErr error
	logOutput := captureSlog(t, func() {
		_, loadErr = Load(cfgPath)
	})

	if loadErr != nil {
		t.Fatalf("Load returned unexpected error: %v", loadErr)
	}

	if strings.Contains(logOutput, "insecure") {
		t.Errorf("unexpected insecure warning for 0o600 permissions: %q", logOutput)
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

// TestLoadCredentialsRefusedOnInsecurePerms verifies that credentials are NOT
// loaded from a config file with 0o644 permissions (group/other-readable).
func TestLoadCredentialsRefusedOnInsecurePerms(t *testing.T) {
	dir := t.TempDir()
	// Use a second slot so EnforceGlobalConfigPerms does not auto-fix perms.
	placeholder := filepath.Join(dir, "placeholder.toml")
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte("[credentials]\nanthropic_api_key = \"sk-insecure\"\ngithub_token = \"gh-insecure\"\n")
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var cfg *Config
	logOutput := captureSlog(t, func() {
		var err error
		cfg, err = Load(placeholder, cfgPath)
		if err != nil {
			t.Fatalf("Load returned unexpected error: %v", err)
		}
	})

	if cfg.Credentials.AnthropicAPIKey != "" {
		t.Errorf("AnthropicAPIKey should be empty when file has 0o644 perms, got %q", cfg.Credentials.AnthropicAPIKey)
	}
	if cfg.Credentials.GithubToken != "" {
		t.Errorf("GithubToken should be empty when file has 0o644 perms, got %q", cfg.Credentials.GithubToken)
	}
	if !strings.Contains(logOutput, "refusing") {
		t.Errorf("expected refusal log entry, got: %q", logOutput)
	}
}

// TestLoadCredentialsLoadedOnSecurePerms verifies that credentials ARE loaded
// from a config file with 0o600 permissions (owner-only).
func TestLoadCredentialsLoadedOnSecurePerms(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte("[credentials]\nanthropic_api_key = \"sk-secure\"\ngithub_token = \"gh-secure\"\n")
	if err := os.WriteFile(cfgPath, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Credentials.AnthropicAPIKey != "sk-secure" {
		t.Errorf("AnthropicAPIKey = %q, want %q", cfg.Credentials.AnthropicAPIKey, "sk-secure")
	}
	if cfg.Credentials.GithubToken != "gh-secure" {
		t.Errorf("GithubToken = %q, want %q", cfg.Credentials.GithubToken, "gh-secure")
	}
}

// TestLoadNonCredentialValuesLoadedFromInsecureFile verifies that non-credential
// config values are still applied from files with insecure permissions.
func TestLoadNonCredentialValuesLoadedFromInsecureFile(t *testing.T) {
	dir := t.TempDir()
	// Use a second slot so EnforceGlobalConfigPerms does not auto-fix perms.
	placeholder := filepath.Join(dir, "placeholder.toml")
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte("[agent]\ndefault = \"my-agent\"\n\n[credentials]\nanthropic_api_key = \"sk-should-be-stripped\"\n")
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(placeholder, cfgPath)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	// Non-credential field should be loaded.
	if cfg.Agent.Default != "my-agent" {
		t.Errorf("Agent.Default = %q, want %q", cfg.Agent.Default, "my-agent")
	}
	// Credential field must be stripped.
	if cfg.Credentials.AnthropicAPIKey != "" {
		t.Errorf("AnthropicAPIKey should be empty, got %q", cfg.Credentials.AnthropicAPIKey)
	}
}
