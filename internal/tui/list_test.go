package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/config"
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
			input:       errors.New("No such image: agency:latest"),
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

// ----- Quit popup state machine -----

func TestQuitPopup_NoActiveWorkspaces_AutoConfirms(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateDone}, IsActive: false, IsDirty: false},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{DangerBg: "9", DangerFg: "15"})

	if !m.result.Confirmed {
		t.Error("with no active workspaces, result should be auto-confirmed")
	}
}

func TestQuitPopup_ActiveWorkspaces_ShowsConfirm(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{DangerBg: "9", DangerFg: "15"})

	if m.step != quitConfirmingQuit {
		t.Errorf("step = %v, want quitConfirmingQuit", m.step)
	}
}

func TestQuitPopup_ConfirmQuit_NoKey_Cancels(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	qm := next.(quitPopupModel)

	if qm.result.Confirmed {
		t.Error("pressing n should cancel")
	}
}

func TestQuitPopup_ConfirmQuit_EscKey_Cancels(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	qm := next.(quitPopupModel)

	if qm.result.Confirmed {
		t.Error("pressing esc should cancel")
	}
}

func TestQuitPopup_ConfirmQuitYes_CleanActive_Quits(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", State: state.StateRunning}, IsActive: true, IsDirty: false},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	qm := next.(quitPopupModel)

	if !qm.result.Confirmed {
		t.Error("pressing y with clean active workspace should confirm quit")
	}
}

func TestQuitPopup_ConfirmQuitYes_DirtyActive_EntersDirtyConfirm(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", Name: "My WS", State: state.StateRunning}, IsActive: true, IsDirty: true},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	qm := next.(quitPopupModel)

	if qm.step != quitConfirmingDirty {
		t.Errorf("step = %v, want quitConfirmingDirty", qm.step)
	}
	if len(qm.dirtyQueue) != 1 {
		t.Errorf("dirtyQueue length = %d, want 1", len(qm.dirtyQueue))
	}
}

func TestQuitPopup_DirtyConfirmNo_Cancels(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", Name: "WS", State: state.StateRunning}, IsActive: true, IsDirty: true},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})
	// Advance to dirty confirm.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(quitPopupModel)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	qm := next.(quitPopupModel)

	if qm.result.Confirmed {
		t.Error("pressing n in dirty confirm should cancel")
	}
}

func TestQuitPopup_DirtyConfirmYes_LastInQueue_Quits(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", Name: "WS", State: state.StateRunning}, IsActive: true, IsDirty: true},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})
	// Advance to dirty confirm.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(quitPopupModel)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	qm := next.(quitPopupModel)

	if !qm.result.Confirmed {
		t.Error("confirming last dirty workspace should result in confirmed quit")
	}
}

func TestQuitPopup_DirtyConfirmYes_MoreInQueue_StaysInDirtyConfirm(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1", Name: "WS1", State: state.StateRunning}, IsActive: true, IsDirty: true},
		{WS: &state.Workspace{ID: "ws-2", Name: "WS2", State: state.StateRunning}, IsActive: true, IsDirty: true},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})
	// Advance to dirty confirm.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(quitPopupModel)

	// Confirm first dirty workspace.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	qm := next.(quitPopupModel)

	if qm.step != quitConfirmingDirty {
		t.Errorf("step = %v, want quitConfirmingDirty", qm.step)
	}
	if len(qm.dirtyQueue) != 1 {
		t.Errorf("dirtyQueue length = %d, want 1 after popping first", len(qm.dirtyQueue))
	}
	if qm.dirtyQueue[0].ID != "ws-2" {
		t.Errorf("remaining dirty queue item ID = %q, want ws-2", qm.dirtyQueue[0].ID)
	}
}

func TestQuitPopup_OtherKeysIgnored(t *testing.T) {
	infos := []workspace.QuitInfo{
		{WS: &state.Workspace{ID: "ws-1"}, IsActive: true},
	}
	m := newQuitPopupModel(infos, config.ThemeConfig{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	qm := next.(quitPopupModel)

	if qm.step != quitConfirmingQuit {
		t.Errorf("step changed unexpectedly to %v; expected quitConfirmingQuit", qm.step)
	}
	if qm.result.Confirmed {
		t.Error("result should not be confirmed after pressing irrelevant key")
	}
}

// TestQuitPopupDoneMsg_Confirmed tests that the sidebar handles a confirmed popup result.
func TestQuitPopupDoneMsg_Confirmed(t *testing.T) {
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
	m := newListModel(mgr)

	next, _ := m.Update(quitPopupDoneMsg{
		confirmed: true,
		infos: []workspace.QuitInfo{
			{WS: &state.Workspace{ID: "ws-1"}, IsActive: true},
		},
	})
	lm := next.(listModel)

	if !lm.shouldKillSession {
		t.Error("shouldKillSession should be true when popup confirms quit")
	}
}

// TestQuitPopupDoneMsg_Canceled tests that the sidebar resumes normally on cancel.
func TestQuitPopupDoneMsg_Canceled(t *testing.T) {
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
	m := newListModel(mgr)

	next, _ := m.Update(quitPopupDoneMsg{confirmed: false})
	lm := next.(listModel)

	if lm.shouldKillSession {
		t.Error("shouldKillSession should be false when popup is canceled")
	}
}
