package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Issue 1 & 7: ValidateContainerID ---

func TestValidateContainerID_Valid(t *testing.T) {
	valid := []string{
		"abc123def456", // 12 chars
		"abc123def456abc123def456abc123def456abc123def456abc123de", // 56 chars
		strings.Repeat("a", 64), // 64 chars
		"0000000000000000000000000000000000000000000000000000000000000000", // 64 zeros
	}
	for _, id := range valid {
		if err := ValidateContainerID(id); err != nil {
			t.Errorf("ValidateContainerID(%q) unexpected error: %v", id, err)
		}
	}
}

func TestValidateContainerID_Invalid(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"too short (11 chars)", "abc123def45"},
		{"too long (65 chars)", strings.Repeat("a", 65)},
		{"uppercase letters", "ABC123DEF456"},
		{"contains dash", "abc123-def456"},
		{"contains underscore", "abc123_def456"},
		{"contains g (non-hex)", "abc123defg56"},
		{"empty string", ""},
		{"whitespace", "   "},
		{"shell injection attempt", "abc123; rm -rf /"},
		{"path traversal attempt", "../../../etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateContainerID(tt.id); err == nil {
				t.Errorf("ValidateContainerID(%q) expected error, got nil", tt.id)
			}
		})
	}
}

// --- Issue 3: redactArgs credential log redaction ---

func TestRedactArgs_RedactsAPIKey(t *testing.T) {
	args := []string{"create", "-e", "ANTHROPIC_API_KEY=sk-ant-secret123"}
	got := redactArgs(args)
	if strings.Contains(strings.Join(got, " "), "sk-ant-secret123") {
		t.Errorf("redactArgs leaked credential: %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "ANTHROPIC_API_KEY=REDACTED") {
		t.Errorf("redactArgs should replace value with REDACTED, got: %v", got)
	}
}

func TestRedactArgs_RedactsGithubToken(t *testing.T) {
	args := []string{"create", "-e", "GITHUB_TOKEN=ghp_supersecrettoken"}
	got := redactArgs(args)
	if strings.Contains(strings.Join(got, " "), "ghp_supersecrettoken") {
		t.Errorf("redactArgs leaked token: %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "GITHUB_TOKEN=REDACTED") {
		t.Errorf("redactArgs should replace value with REDACTED, got: %v", got)
	}
}

func TestRedactArgs_RedactsSecret(t *testing.T) {
	args := []string{"-e", "MY_SECRET=hunter2"}
	got := redactArgs(args)
	if strings.Contains(strings.Join(got, " "), "hunter2") {
		t.Errorf("redactArgs leaked secret: %v", got)
	}
}

func TestRedactArgs_PreservesNonSensitiveEnvVars(t *testing.T) {
	args := []string{"create", "-e", "GIT_USER=alice", "-e", "GIT_EMAIL=alice@example.com"}
	got := redactArgs(args)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "GIT_USER=alice") {
		t.Errorf("redactArgs incorrectly redacted GIT_USER: %v", got)
	}
	if !strings.Contains(joined, "GIT_EMAIL=alice@example.com") {
		t.Errorf("redactArgs incorrectly redacted GIT_EMAIL: %v", got)
	}
}

func TestRedactArgs_DoesNotMutateOriginal(t *testing.T) {
	args := []string{"-e", "ANTHROPIC_API_KEY=sk-secret"}
	original := make([]string, len(args))
	copy(original, args)
	_ = redactArgs(args)
	for i, a := range args {
		if a != original[i] {
			t.Errorf("redactArgs mutated original slice at index %d: %q -> %q", i, original[i], a)
		}
	}
}

func TestRedactArgs_HandlesNoDashE(t *testing.T) {
	args := []string{"ps", "-a", "--filter", "name=prefix"}
	got := redactArgs(args)
	if strings.Join(got, " ") != strings.Join(args, " ") {
		t.Errorf("redactArgs changed args that had no -e flags: %v", got)
	}
}

// --- Issue 9: NET_RAW capability removed ---

func TestDefaultCapAdd_DoesNotContainNetRaw(t *testing.T) {
	for _, cap := range defaultCapAdd {
		if strings.EqualFold(cap, "NET_RAW") {
			t.Error("defaultCapAdd must not contain NET_RAW: allows raw sockets for ARP spoofing/ICMP attacks")
		}
	}
}

// --- Issue 10: Resource limits applied to docker create ---

func TestCreate_AppliesMemoryLimit(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:         "agency:latest",
		Name:          "test-mem",
		WorktreeMount: "/app",
		Memory:        "2g",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if !strings.Contains(log, "--memory 2g") {
		t.Errorf("expected --memory 2g in docker create args, got:\n%s", log)
	}
}

func TestCreate_AppliesCPULimit(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:         "agency:latest",
		Name:          "test-cpu",
		WorktreeMount: "/app",
		CPUs:          2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if !strings.Contains(log, "--cpus 2") {
		t.Errorf("expected --cpus 2 in docker create args, got:\n%s", log)
	}
}

func TestCreate_OmitsResourceArgsWhenZero(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:         "agency:latest",
		Name:          "test-nolimits",
		WorktreeMount: "/app",
		Memory:        "",
		CPUs:          0,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if strings.Contains(log, "--memory") {
		t.Errorf("expected no --memory arg when Memory is empty, got:\n%s", log)
	}
	if strings.Contains(log, "--cpus") {
		t.Errorf("expected no --cpus arg when CPUs is 0, got:\n%s", log)
	}
}

// --- Issue 2: Credentials via env-file, not -e flags ---

func TestCreate_SensitiveEnvViaEnvFile(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	// Write a temp env file to simulate the workspace provisionContainer path.
	dir := t.TempDir()
	envFile := filepath.Join(dir, "credentials")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=sk-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:         "agency:latest",
		Name:          "test-envfile",
		WorktreeMount: "/app",
		EnvFile:       envFile,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	log := readArgsLog(t, argsFile)
	// The env-file path should appear in the args.
	if !strings.Contains(log, "--env-file") {
		t.Errorf("expected --env-file in docker create args when EnvFile is set, got:\n%s", log)
	}
	// The raw credential must NOT appear as a -e argument.
	if strings.Contains(log, "sk-secret") {
		t.Errorf("credential value must not appear inline in docker create args, got:\n%s", log)
	}
}

func TestCreate_NoEnvFileWhenEmpty(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:         "agency:latest",
		Name:          "test-no-envfile",
		WorktreeMount: "/app",
		EnvFile:       "",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if strings.Contains(log, "--env-file") {
		t.Errorf("expected no --env-file when EnvFile is empty, got:\n%s", log)
	}
}
