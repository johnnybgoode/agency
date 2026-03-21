// Package state manages persistent application state and file locking.
package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
	SessionID    string         `json:"session_id,omitempty"` // Claude session UUID for --resume
	TmuxWindow   string         `json:"tmux_window"`          // window ID (e.g. "@3")
	PaneID       string         `json:"pane_id"`              // pane ID within that window (e.g. "%5")
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	PauseMode    *string        `json:"pause_mode"`
	FailedFrom   *string        `json:"failed_from"`
	Error        *string        `json:"error"`
}

// DisplayName returns the workspace's display name: Name if set, Branch otherwise.
func (ws *Workspace) DisplayName() string {
	if ws.Name != "" {
		return ws.Name
	}
	return ws.Branch
}

// State holds the persisted state for an agency project.
type State struct {
	Version               int    `json:"version"`
	Project               string `json:"project"`
	BarePath              string `json:"bare_path"`
	TmuxSession           string `json:"tmux_session"`
	MainWindowID          string `json:"main_window_id"`              // ID of the Agency main window (sidebar lives here)
	WorkspacePaneID       string `json:"workspace_pane_id,omitempty"` // pane ID of the shell pane that lives in :0.1 when idle
	ActiveWorkspaceID     string `json:"active_workspace_id"`         // workspace ID whose pane is currently swapped into the right slot
	LastActiveWorkspaceID string `json:"last_active_workspace_id"`    // previously active workspace, for fallback on removal
	PID                   int    `json:"pid"`
	// LockNonce is the random nonce written to the lock file when the lock was
	// acquired. On startup, if a stale lock is suspected, the nonce stored here
	// is compared against the nonce in the lock file to detect PID reuse: if
	// they differ the lock belongs to a different process instance (recycled PID).
	LockNonce        string                `json:"lock_nonce,omitempty"`
	SandboxID        string                `json:"sandbox_id,omitempty"` // Docker sandbox name (project-level)
	UpdatedAt        time.Time             `json:"updated_at"`
	SessionStartedAt *time.Time            `json:"session_started_at,omitempty"`
	Workspaces       map[string]*Workspace `json:"workspaces"`
}

// workspaceIDRe matches ws-<8 hex chars>.
var workspaceIDRe = regexp.MustCompile(`^ws-[a-f0-9]{8}$`)

// sandboxNameRe matches Docker sandbox names: starts with alphanumeric, followed by up to 127
// alphanumeric or ._+- characters, for a maximum total length of 128.
var sandboxNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+\-]{0,127}$`)

// ValidateWorkspaceID returns an error if id does not match the expected
// workspace ID format (ws-<8hex>).
func ValidateWorkspaceID(id string) error {
	if !workspaceIDRe.MatchString(id) {
		return fmt.Errorf("invalid workspace ID %q: must match ws-[a-f0-9]{8}", id)
	}
	return nil
}

// ValidateSandboxName returns an error if name is not a valid Docker sandbox name.
// Valid names start with an alphanumeric character and contain only alphanumeric
// characters or ._+- characters, with a maximum length of 128.
func ValidateSandboxName(name string) error {
	if !sandboxNameRe.MatchString(name) {
		return fmt.Errorf("invalid sandbox name %q: must start with alphanumeric and contain only [a-zA-Z0-9._+-], max 128 chars", name)
	}
	return nil
}

// Validate checks that all workspace IDs and sandbox IDs in the state
// conform to expected formats. This prevents command injection via
// tampered state files.
func (s *State) Validate() error {
	if s.SandboxID != "" {
		if err := ValidateSandboxName(s.SandboxID); err != nil {
			return fmt.Errorf("state file: %w", err)
		}
	}
	for id, ws := range s.Workspaces {
		// Validate the map key matches expected workspace ID format.
		if err := ValidateWorkspaceID(id); err != nil {
			return fmt.Errorf("state file: %w", err)
		}
		// Validate the workspace's own ID field matches the map key.
		if ws.ID != id {
			return fmt.Errorf("workspace ID mismatch: key=%q, field=%q", id, ws.ID)
		}
	}
	return nil
}

// Default returns a new State with sensible defaults for the given project.
func Default(project, barePath string) *State {
	return &State{
		Version:     2,
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
	// Migrate v1 → v2: clear per-workspace SandboxIDs (they were Docker
	// container IDs, not sandbox names) and leave the project-level SandboxID
	// empty so the caller can provision a fresh sandbox.
	if s.Version < 2 {
		for _, ws := range s.Workspaces {
			ws.SandboxID = ""
			ws.SessionID = ""
		}
		s.SandboxID = ""
		s.Version = 2
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("validating state file %s: %w", path, err)
	}
	slog.Debug("state read", "path", path, "workspaces", len(s.Workspaces))
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

	slog.Debug("state written", "path", path)
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
