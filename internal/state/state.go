package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SessionState represents the lifecycle state of a session.
type SessionState string

const (
	StateCreating     SessionState = "creating"
	StateProvisioning SessionState = "provisioning"
	StateRunning      SessionState = "running"
	StatePaused       SessionState = "paused"
	StateCompleting   SessionState = "completing"
	StateDone         SessionState = "done"
	StateFailed       SessionState = "failed"
)

// Session holds the runtime state of a single agent session.
type Session struct {
	ID           string       `json:"id"`
	State        SessionState `json:"state"`
	Branch       string       `json:"branch"`
	WorktreePath string       `json:"worktree_path"`
	SandboxID    string       `json:"sandbox_id"`
	TmuxWindow   string       `json:"tmux_window"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	PauseMode    *string      `json:"pause_mode"`
	FailedFrom   *string      `json:"failed_from"`
	Error        *string      `json:"error"`
}

// State holds the persisted state for an agency project.
type State struct {
	Version     int                 `json:"version"`
	Project     string              `json:"project"`
	BarePath    string              `json:"bare_path"`
	TmuxSession string              `json:"tmux_session"`
	PID         int                 `json:"pid"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Sessions    map[string]*Session `json:"sessions"`
}

// Default returns a new State with sensible defaults for the given project.
func Default(project, barePath string) *State {
	return &State{
		Version:     1,
		Project:     project,
		BarePath:    barePath,
		TmuxSession: "agency-" + project,
		Sessions:    make(map[string]*Session),
	}
}

// Read loads State from the JSON file at path.
func Read(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing state file %s: %w", path, err)
	}
	if s.Sessions == nil {
		s.Sessions = make(map[string]*Session)
	}
	return &s, nil
}

// Write atomically persists s to the JSON file at path. The parent directory
// is created if it does not exist.
func Write(path string, s *State) error {
	s.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing state temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming state file: %w", err)
	}

	return nil
}

// IsProcessAlive reports whether the process with the given PID is running.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
