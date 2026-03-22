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

func TestWriteClaudeHooks_MergesIntoExistingSettings(t *testing.T) {
	dir := t.TempDir()

	// Pre-create settings.json with existing hooks and custom fields.
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

	// Stop hook was added with correct matcher+hooks schema.
	stopMatchers, ok := hooks["Stop"].([]any)
	if !ok || len(stopMatchers) == 0 {
		t.Fatal("Stop matchers not added")
	}
	matcher := stopMatchers[0].(map[string]any)
	if matcher["matcher"] != "" {
		t.Errorf("expected empty matcher, got %v", matcher["matcher"])
	}
	hooksList := matcher["hooks"].([]any)
	if len(hooksList) == 0 {
		t.Fatal("hooks array is empty")
	}
	entry := hooksList[0].(map[string]any)
	if entry["command"] != "node .claude/hooks/write-agent-status.js" {
		t.Errorf("unexpected hook command: %v", entry["command"])
	}
}

func TestWriteClaudeHooks_IdempotentMerge(t *testing.T) {
	dir := t.TempDir()

	// Run twice — should not duplicate the matcher entry.
	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("first WriteClaudeHooks() error: %v", err)
	}
	if err := templates.WriteClaudeHooks(dir); err != nil {
		t.Fatalf("second WriteClaudeHooks() error: %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	hooks := parsed["hooks"].(map[string]any)
	stopMatchers := hooks["Stop"].([]any)
	if len(stopMatchers) != 1 {
		t.Errorf("expected 1 Stop matcher entry, got %d", len(stopMatchers))
	}
}
