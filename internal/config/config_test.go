package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn,
// returning whatever was written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	orig := os.Stderr
	os.Stderr = w

	fn()

	os.Stderr = orig
	w.Close()

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, readErr := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	return buf.String()
}

func TestSessionConfigPath(t *testing.T) {
	worktreePath := "/some/project/my-project-abc123"
	got := SessionConfigPath(worktreePath)
	want := filepath.Join(worktreePath, ".tool", "config.toml")
	if got != want {
		t.Errorf("SessionConfigPath(%q) = %q, want %q", worktreePath, got, want)
	}
}

func TestSessionConfigPathIsInsideTool(t *testing.T) {
	// Verify the path ends with .tool/config.toml regardless of OS separator.
	worktreePath := "/a/b/c"
	got := SessionConfigPath(worktreePath)
	suffix := filepath.Join(".tool", "config.toml")
	if !strings.HasSuffix(got, suffix) {
		t.Errorf("SessionConfigPath(%q) = %q, want suffix %q", worktreePath, got, suffix)
	}
}

func TestLoadPermissionWarning(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Write a minimal valid config with world-readable permissions (0o644).
	content := []byte("[agent]\ndefault = \"claude\"\n")
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var loadErr error
	stderr := captureStderr(t, func() {
		_, loadErr = Load(cfgPath)
	})

	if loadErr != nil {
		t.Fatalf("Load returned unexpected error: %v", loadErr)
	}

	if !strings.Contains(stderr, "warning") {
		t.Errorf("expected a warning on stderr for 0o644 permissions, got: %q", stderr)
	}
	if !strings.Contains(stderr, "0o644") {
		t.Errorf("expected stderr to mention the actual permission 0o644, got: %q", stderr)
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
	stderr := captureStderr(t, func() {
		_, loadErr = Load(cfgPath)
	})

	if loadErr != nil {
		t.Fatalf("Load returned unexpected error: %v", loadErr)
	}

	if strings.Contains(stderr, "warning") {
		t.Errorf("unexpected warning on stderr for 0o600 permissions: %q", stderr)
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
