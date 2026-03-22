package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnnybgoode/agency/internal/templates"
)

func TestSub_DockerContainsDockerfile(t *testing.T) {
	sub, err := templates.Sub("docker")
	if err != nil {
		t.Fatalf("Sub(\"docker\") error: %v", err)
	}
	data, err := fs.ReadFile(sub, "Dockerfile")
	if err != nil {
		t.Fatalf("Dockerfile not found in docker sub-FS: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded Dockerfile is empty")
	}
}

func TestSub_DockerContainsEntrypoint(t *testing.T) {
	sub, err := templates.Sub("docker")
	if err != nil {
		t.Fatalf("Sub(\"docker\") error: %v", err)
	}
	data, err := fs.ReadFile(sub, "entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not found in docker sub-FS: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded entrypoint.sh is empty")
	}
}

func TestSub_UnknownDirReturnsError(t *testing.T) {
	sub, err := templates.Sub("nonexistent")
	if err != nil {
		// fs.Sub itself doesn't error on missing dirs in embed.FS;
		// the error surfaces on first read.
		return
	}
	_, err = fs.ReadFile(sub, "anything")
	if err == nil {
		t.Error("expected error reading from nonexistent subdirectory, got nil")
	}
}

func TestWriteClaudeHooks_CreatesFiles(t *testing.T) {
	dir := t.TempDir()

	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("WriteClaudeHooks() error: %v", err)
	}

	// Hook script should exist.
	hookPath := filepath.Join(dir, ".claude", "hooks", "write-agent-status.js")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook script not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("hook script is empty")
	}

	// Settings file should exist.
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("settings.json is empty")
	}
}

func TestWriteClaudeHooks_DoesNotOverwriteExistingSettings(t *testing.T) {
	dir := t.TempDir()

	// Pre-create settings.json with custom content.
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"custom": true}`)
	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("WriteClaudeHooks() error: %v", err)
	}

	// Settings should be unchanged.
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(existing) {
		t.Errorf("settings.json was overwritten: got %q, want %q", got, existing)
	}

	// Hook script should still be written.
	hookPath := filepath.Join(dir, ".claude", "hooks", "write-agent-status.js")
	if _, err := os.ReadFile(hookPath); err != nil {
		t.Fatalf("hook script not written: %v", err)
	}
}
