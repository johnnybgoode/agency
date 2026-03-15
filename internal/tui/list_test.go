package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tmux"
	"github.com/johnnybgoode/agency/internal/workspace"
)

func TestFriendlyError(t *testing.T) {
	tests := []struct {
		name        string
		input       error
		wantNil     bool
		wantContain string
	}{
		{
			name:    "nil passthrough",
			input:   nil,
			wantNil: true,
		},
		{
			name:        "active workspace conflict",
			input:       errors.New("already has an active workspace for this branch"),
			wantContain: "already has an active workspace",
		},
		{
			name:        "git already checked out",
			input:       errors.New("fatal: 'refs/heads/feature' is already checked out at '/path'"),
			wantContain: "already has an active worktree",
		},
		{
			name:        "already exists",
			input:       errors.New("path already exists and is not empty"),
			wantContain: "already exists",
		},
		{
			name:        "docker not running",
			input:       errors.New("docker is not available on this system"),
			wantContain: "docker is not running",
		},
		{
			name:        "docker daemon not running",
			input:       errors.New("docker daemon is not running"),
			wantContain: "docker daemon is not reachable",
		},
		{
			name:        "no such image",
			input:       errors.New("No such image: claude-sandbox:latest"),
			wantContain: "sandbox image not found",
		},
		{
			name:        "conflict with container name",
			input:       errors.New("Conflict. The container name \"/my-container\" is already in use"),
			wantContain: "container with that name already exists",
		},
		{
			name:        "unknown error truncation at newline",
			input:       errors.New("some error occurred\nwith extra git output\nthat should be stripped"),
			wantContain: "some error occurred",
		},
		{
			name:        "unknown error without newline returned as-is",
			input:       errors.New("simple unknown error"),
			wantContain: "simple unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyError(tt.input)

			if tt.wantNil {
				if got != nil {
					t.Errorf("friendlyError(nil) = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("friendlyError returned nil, want non-nil")
			}

			if !strings.Contains(got.Error(), tt.wantContain) {
				t.Errorf("friendlyError(%q).Error() = %q, want to contain %q",
					tt.input.Error(), got.Error(), tt.wantContain)
			}

			// Unknown errors should not contain multi-line content.
			if strings.Contains(got.Error(), "\n") {
				t.Errorf("friendlyError result contains newline: %q", got.Error())
			}
		})
	}
}

// ----- Quit state machine -----

// newListModelForTest creates a listModel backed by a minimal test manager.
// The manager has no real docker/tmux/git infrastructure.
func newListModelForTest(t *testing.T) listModel {
	t.Helper()
	dir := t.TempDir()

	s := &state.State{
		Project:    "testproject",
		BarePath:   dir + "/.bare",
		Workspaces: make(map[string]*state.Workspace),
	}
	mgr := &workspace.Manager{
		StatePath:   dir + "/state.json",
		ProjectDir:  dir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
	}
	_ = mgr.SaveState()
	return newListModel(mgr)
}

func TestQuit_QKeyStartsAssessing(t *testing.T) {
	m := newListModelForTest(t)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	lm := next.(listModel)

	if lm.quitStep != quitAssessing {
		t.Errorf("after q: quitStep = %v, want quitAssessing", lm.quitStep)
	}
}

func TestQuit_NoActiveWorkspacesSkipsConfirm(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitAssessing

	// Inject assessed message with no active workspaces.
	next, _ := m.Update(quitAssessedMsg{
		infos: []workspace.QuitInfo{
			{WS: &state.Workspace{ID: "ws-1", State: state.StateDone}, IsActive: false, IsDirty: false},
		},
	})
	lm := next.(listModel)

	// With no active workspaces, should skip confirmation and go straight to quit.
	if !lm.shouldKillSession {
		t.Error("shouldKillSession should be true when no active workspaces")
	}
}

func TestQuit_ActiveWorkspacesShowsConfirm(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitAssessing

	next, _ := m.Update(quitAssessedMsg{
		infos: []workspace.QuitInfo{
			{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
		},
	})
	lm := next.(listModel)

	if lm.quitStep != quitConfirmingQuit {
		t.Errorf("quitStep = %v, want quitConfirmingQuit", lm.quitStep)
	}
}

func TestQuit_ConfirmQuitNoKey_BackToIdle(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingQuit
	m.quitInfos = []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	lm := next.(listModel)

	if lm.quitStep != quitIdle {
		t.Errorf("after n: quitStep = %v, want quitIdle", lm.quitStep)
	}
}

func TestQuit_ConfirmQuitEscKey_BackToIdle(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingQuit
	m.quitInfos = []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	lm := next.(listModel)

	if lm.quitStep != quitIdle {
		t.Errorf("after esc: quitStep = %v, want quitIdle", lm.quitStep)
	}
}

func TestQuit_ConfirmQuitYes_CleanActiveSkipsDirtyConfirm(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingQuit
	m.quitInfos = []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	lm := next.(listModel)

	// Clean active workspaces: no dirty confirm needed, should quit immediately.
	if !lm.shouldKillSession {
		t.Error("shouldKillSession should be true after confirming quit with clean active workspace")
	}
}

func TestQuit_ConfirmQuitYes_DirtyActiveEntersDirtyConfirm(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingQuit
	m.quitInfos = []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", Name: "My WS", State: state.StateRunning}, IsActive: true, IsDirty: true},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	lm := next.(listModel)

	if lm.quitStep != quitConfirmingDirty {
		t.Errorf("quitStep = %v, want quitConfirmingDirty", lm.quitStep)
	}
	if len(lm.dirtyQueue) != 1 {
		t.Errorf("dirtyQueue length = %d, want 1", len(lm.dirtyQueue))
	}
}

func TestQuit_DirtyConfirmNo_AbortsQuit(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingDirty
	m.dirtyQueue = []*state.Workspace{
		{ID: "ws-1", Name: "WS", State: state.StateRunning},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	lm := next.(listModel)

	if lm.quitStep != quitIdle {
		t.Errorf("after n in dirty confirm: quitStep = %v, want quitIdle", lm.quitStep)
	}
	if len(lm.dirtyQueue) != 0 {
		t.Errorf("dirtyQueue not cleared after abort; len = %d", len(lm.dirtyQueue))
	}
}

func TestQuit_DirtyConfirmYes_LastInQueue_Quits(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingDirty
	m.dirtyQueue = []*state.Workspace{
		{ID: "ws-1", Name: "WS", State: state.StateRunning},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	lm := next.(listModel)

	if !lm.shouldKillSession {
		t.Error("shouldKillSession should be true after confirming last dirty workspace")
	}
}

func TestQuit_DirtyConfirmYes_MoreInQueue_StaysInDirtyConfirm(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingDirty
	m.dirtyQueue = []*state.Workspace{
		{ID: "ws-1", Name: "WS1", State: state.StateRunning},
		{ID: "ws-2", Name: "WS2", State: state.StateRunning},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	lm := next.(listModel)

	if lm.quitStep != quitConfirmingDirty {
		t.Errorf("quitStep = %v, want quitConfirmingDirty", lm.quitStep)
	}
	if len(lm.dirtyQueue) != 1 {
		t.Errorf("dirtyQueue length = %d, want 1 after popping first", len(lm.dirtyQueue))
	}
	if lm.dirtyQueue[0].ID != "ws-2" {
		t.Errorf("remaining dirty queue item ID = %q, want ws-2", lm.dirtyQueue[0].ID)
	}
}

func TestQuit_OtherKeysIgnoredDuringQuit(t *testing.T) {
	m := newListModelForTest(t)
	m.quitStep = quitConfirmingQuit
	m.quitInfos = []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1"}, IsActive: true},
	}

	// Pressing 'd' (delete) should be suppressed during quit flow.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	lm := next.(listModel)

	// quitStep should remain unchanged.
	if lm.quitStep != quitConfirmingQuit {
		t.Errorf("quitStep changed unexpectedly to %v; expected quitConfirmingQuit", lm.quitStep)
	}
	// confirming should not be set.
	if lm.confirming {
		t.Error("confirming should not be set during quit flow")
	}
}
