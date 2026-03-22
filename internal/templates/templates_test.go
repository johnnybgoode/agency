package templates_test

import (
	"encoding/json"
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
	hookPath := filepath.Join(dir, ".claude", "hooks", "write-agent-status.cjs")
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

func TestWriteClaudeHooks_AddsStatuslineToExistingSettings(t *testing.T) {
	dir := t.TempDir()

	// Pre-create settings.json with existing hooks but no statusline.
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{
  "custom": true,
  "hooks": {
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo post"}]}]
  }
}`)
	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("WriteClaudeHooks() error: %v", err)
	}

	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	// Custom field preserved.
	if parsed["custom"] != true {
		t.Error("custom field was lost")
	}

	// Existing hooks preserved.
	hooks := parsed["hooks"].(map[string]any)
	if hooks["PostToolUse"] == nil {
		t.Error("PostToolUse hooks were lost")
	}

	// Statusline was added.
	sl, ok := parsed["statusline"].(map[string]any)
	if !ok {
		t.Fatal("statusline not added")
	}
	if sl["command"] != "node .claude/hooks/write-agent-status.cjs" {
		t.Errorf("unexpected statusline command: %v", sl["command"])
	}
}

func TestWriteClaudeHooks_DoesNotOverwriteExistingStatusline(t *testing.T) {
	dir := t.TempDir()

	// Pre-create settings.json with an existing statusline.
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"statusline": {"command": "node my-custom-statusline.js"}}`)
	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("WriteClaudeHooks() error: %v", err)
	}

	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// File should be unchanged — existing statusline preserved.
	if string(got) != string(existing) {
		t.Errorf("settings.json was modified: got %q, want %q", got, existing)
	}
}

func TestWriteClaudeHooks_IdempotentMerge(t *testing.T) {
	dir := t.TempDir()

	// Run twice — settings should be identical after both runs.
	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("first WriteClaudeHooks() error: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))

	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("second WriteClaudeHooks() error: %v", err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))

	if string(first) != string(second) {
		t.Errorf("settings.json changed on second run:\nfirst:  %s\nsecond: %s", first, second)
	}
}
