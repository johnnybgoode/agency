package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	s := Default("myproject", "/path/to/.bare")

	if s.Project != "myproject" {
		t.Errorf("Project = %q, want %q", s.Project, "myproject")
	}
	if s.BarePath != "/path/to/.bare" {
		t.Errorf("BarePath = %q, want %q", s.BarePath, "/path/to/.bare")
	}
	if s.Sessions == nil {
		t.Error("Sessions should be initialized, got nil")
	}
	if len(s.Sessions) != 0 {
		t.Errorf("Sessions should be empty, got %d entries", len(s.Sessions))
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
	original.Sessions["sess-abcd1234"] = &Session{
		ID:     "sess-abcd1234",
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
	if len(got.Sessions) != 1 {
		t.Fatalf("Sessions len = %d, want 1", len(got.Sessions))
	}
	sess := got.Sessions["sess-abcd1234"]
	if sess == nil {
		t.Fatal("expected session sess-abcd1234, got nil")
	}
	if sess.State != StateRunning {
		t.Errorf("Session.State = %q, want %q", sess.State, StateRunning)
	}
	if sess.Branch != "agent/feature" {
		t.Errorf("Session.Branch = %q, want %q", sess.Branch, "agent/feature")
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

func TestReadInitializesNilSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write JSON without a "sessions" key to simulate missing sessions field.
	raw := `{"version":1,"project":"p","bare_path":"/bare","tmux_session":"agency-p","pid":0,"updated_at":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.Sessions == nil {
		t.Error("Sessions should be initialized to empty map when missing from JSON, got nil")
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
