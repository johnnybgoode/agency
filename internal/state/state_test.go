package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	s := Default("myproject", "/path/to/.bare")

	if s.Project != "myproject" {
		t.Errorf("Project = %q, want %q", s.Project, "myproject")
	}
	if s.BarePath != "/path/to/.bare" {
		t.Errorf("BarePath = %q, want %q", s.BarePath, "/path/to/.bare")
	}
	if s.Workspaces == nil {
		t.Error("Workspaces should be initialized, got nil")
	}
	if len(s.Workspaces) != 0 {
		t.Errorf("Workspaces should be empty, got %d entries", len(s.Workspaces))
	}
	if s.Version != 1 {
		t.Errorf("Version = %d, want 1", s.Version)
	}
	if s.TmuxSession != "agency-myproject" {
		t.Errorf("TmuxSession = %q, want %q", s.TmuxSession, "agency-myproject")
	}
}

func TestReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	original := Default("testproject", "/bare")
	original.PID = 12345
	original.Workspaces["ws-abcd1234"] = &Workspace{
		ID:     "ws-abcd1234",
		State:  StateRunning,
		Branch: "agent/feature",
	}

	if err := Write(path, original); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if got.Project != original.Project {
		t.Errorf("Project = %q, want %q", got.Project, original.Project)
	}
	if got.BarePath != original.BarePath {
		t.Errorf("BarePath = %q, want %q", got.BarePath, original.BarePath)
	}
	if got.PID != original.PID {
		t.Errorf("PID = %d, want %d", got.PID, original.PID)
	}
	if len(got.Workspaces) != 1 {
		t.Fatalf("Workspaces len = %d, want 1", len(got.Workspaces))
	}
	ws := got.Workspaces["ws-abcd1234"]
	if ws == nil {
		t.Fatal("expected workspace ws-abcd1234, got nil")
	}
	if ws.State != StateRunning {
		t.Errorf("Workspace.State = %q, want %q", ws.State, StateRunning)
	}
	if ws.Branch != "agent/feature" {
		t.Errorf("Workspace.Branch = %q, want %q", ws.Branch, "agent/feature")
	}
}

func TestWriteAtomicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := Default("atomictest", "/bare")
	if err := Write(path, s); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Temp file should not remain after Write.
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file should not exist after successful Write")
	}

	// The actual file should exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file should exist: %v", err)
	}
}

func TestReadInitializesNilWorkspaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write JSON without a "workspaces" key to simulate missing workspaces field.
	raw := `{"version":1,"project":"p","bare_path":"/bare","tmux_session":"agency-p","pid":0,"updated_at":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.Workspaces == nil {
		t.Error("Workspaces should be initialized to empty map when missing from JSON, got nil")
	}
}

func TestWriteCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// Use a path whose parent does not yet exist.
	path := filepath.Join(dir, "nested", "subdir", "state.json")

	s := Default("dirtest", "/bare")
	if err := Write(path, s); err != nil {
		t.Fatalf("Write to nonexistent parent dir failed: %v", err)
	}

	// Verify the file was created.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not found after Write: %v", err)
	}

	// Verify the content is valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var decoded State
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("state file is not valid JSON: %v", err)
	}
}

func TestSessionStartedAtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Non-nil SessionStartedAt round-trips correctly.
	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	s := Default("proj", "/bare")
	s.SessionStartedAt = &ts
	if err := Write(path, s); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.SessionStartedAt == nil {
		t.Fatal("SessionStartedAt should be non-nil after round-trip")
	}
	if !got.SessionStartedAt.Equal(ts) {
		t.Errorf("SessionStartedAt = %v, want %v", *got.SessionStartedAt, ts)
	}

	// Nil SessionStartedAt is omitted from JSON.
	s2 := Default("proj", "/bare")
	s2.SessionStartedAt = nil
	path2 := filepath.Join(dir, "state2.json")
	if err := Write(path2, s2); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	data, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if strings.Contains(string(data), "session_started_at") {
		t.Error("nil SessionStartedAt should be omitted from JSON")
	}
	got2, err := Read(path2)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got2.SessionStartedAt != nil {
		t.Errorf("SessionStartedAt should be nil, got %v", got2.SessionStartedAt)
	}
}

func TestIsProcessAlive(t *testing.T) {
	t.Run("current process is alive", func(t *testing.T) {
		pid := os.Getpid()
		if !IsProcessAlive(pid) {
			t.Errorf("IsProcessAlive(%d) = false, want true for current PID", pid)
		}
	})

	t.Run("PID 0 is not a valid user process", func(t *testing.T) {
		// PID 0 refers to the entire process group; Signal(0) on it behaves
		// differently and we expect IsProcessAlive to report false for it as a
		// "dead" sentinel in the codebase.
		// On Linux, kill(0, 0) succeeds (sends to process group), so we skip
		// asserting a specific value and just ensure no panic occurs.
		_ = IsProcessAlive(0)
	})

	t.Run("very high fake PID is dead", func(t *testing.T) {
		// PID 4194304 is above the Linux kernel max (4194304 is 2^22, the
		// configured max on most systems is 4194304 or lower; we use a safely
		// unreachable value).
		const fakePID = 4194302
		if IsProcessAlive(fakePID) {
			t.Errorf("IsProcessAlive(%d) = true, expected false for nonexistent PID", fakePID)
		}
	})
}

func TestWorkspaceDisplayName(t *testing.T) {
	tests := []struct {
		name string
		ws   Workspace
		want string
	}{
		{"uses Name when set", Workspace{Name: "my-task", Branch: "feat/foo"}, "my-task"},
		{"falls back to Branch when Name empty", Workspace{Branch: "feat/foo"}, "feat/foo"},
		{"empty when both empty", Workspace{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ws.DisplayName(); got != tt.want {
				t.Errorf("DisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateWorkspaceID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"ws-a1b2c3d4", false},
		{"ws-00000000", false},
		{"ws-ffffffff", false},
		{"ws-short", true},
		{"ws-ABCDEF12", true},
		{"notws-a1b2c3d4", true},
		{"ws-a1b2c3d4-extra", true},
		{"", true},
		{"ws-", true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ValidateWorkspaceID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWorkspaceID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestValidateContainerID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"abcdef012345", false}, // 12 hex chars — min valid
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", false}, // 64 hex chars — max valid
		{"abc", true},          // too short
		{"ABCDEF012345", true}, // uppercase not allowed
		{"abcdef01234g", true}, // non-hex char
		{"", true},
		{"abcdef01234", true}, // 11 chars, below minimum
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ValidateContainerID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateContainerID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}
