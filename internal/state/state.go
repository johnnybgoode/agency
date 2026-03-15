// Package state manages persistent application state and file locking.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WorkspaceState represents the lifecycle state of a workspace.
type WorkspaceState string

// Workspace state constants represent the lifecycle of a workspace.
const (
	StateCreating     WorkspaceState = "creating"
	StateProvisioning WorkspaceState = "provisioning"
	StateRunning      WorkspaceState = "running"
	StatePaused       WorkspaceState = "paused"
	StateCompleting   WorkspaceState = "completing"
	StateDone         WorkspaceState = "done"
	StateFailed       WorkspaceState = "failed"
)

// Workspace holds the runtime state of a single agent workspace.
type Workspace struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"` // user-defined display name
	State        WorkspaceState `json:"state"`
	Branch       string         `json:"branch"`
	WorktreePath string         `json:"worktree_path"`
	SandboxID    string         `json:"sandbox_id"`
	TmuxWindow   string         `json:"tmux_window"` // window ID (e.g. "@3")
	PaneID       string         `json:"pane_id"`     // pane ID within that window (e.g. "%5")
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	PauseMode    *string        `json:"pause_mode"`
	FailedFrom   *string        `json:"failed_from"`
	Error        *string        `json:"error"`
}

// State holds the persisted state for an agency project.
type State struct {
	Version               int                   `json:"version"`
	Project               string                `json:"project"`
	BarePath              string                `json:"bare_path"`
	TmuxSession           string                `json:"tmux_session"`
	MainWindowID          string                `json:"main_window_id"`              // ID of the Agency main window (sidebar lives here)
	WorkspacePaneID       string                `json:"workspace_pane_id,omitempty"` // pane ID of the shell pane that lives in :0.1 when idle
	ActiveWorkspaceID     string                `json:"active_workspace_id"`         // workspace ID whose pane is currently swapped into the right slot
	LastActiveWorkspaceID string                `json:"last_active_workspace_id"`    // previously active workspace, for fallback on removal
	PID                   int                   `json:"pid"`
	UpdatedAt             time.Time             `json:"updated_at"`
	Workspaces            map[string]*Workspace `json:"workspaces"`
}

// Default returns a new State with sensible defaults for the given project.
func Default(project, barePath string) *State {
	return &State{
		Version:     1,
		Project:     project,
		BarePath:    barePath,
		TmuxSession: "agency-" + project,
		Workspaces:  make(map[string]*Workspace),
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
	if s.Workspaces == nil {
		s.Workspaces = make(map[string]*Workspace)
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

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
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
