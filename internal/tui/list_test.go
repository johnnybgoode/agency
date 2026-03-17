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

// ----- Test helpers -----

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
		Cfg:         config.DefaultConfig(),
	}
	_ = mgr.SaveState()
	return newListModel(mgr)
}

// ----- sidebarWidth -----

func TestSidebarWidth_ZeroState(t *testing.T) {
	tests := []struct {
		name      string
		termWidth int
		cfgPct    int
		want      int
	}{
		{"15 pct of 200 clamped to 30", 200, 15, 30},
		{"15 pct of 400 clamped to max 50", 400, 15, 50},
		{"narrow terminal clamps to min 25", 80, 10, 25},
		{"25 pct of 200 = 50 (max)", 200, 25, 50},
		{"30 pct of 200 = 60 clamped to 50", 200, 30, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newListModelForTest(t)
			m.manager.Cfg.TUI.SidebarWidth = tt.cfgPct
			m.width = tt.termWidth
			// No workspaces → zero state.

			got := m.sidebarWidth()
			if got != tt.want {
				t.Errorf("sidebarWidth() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSidebarWidth_SidebarMode(t *testing.T) {
	tests := []struct {
		name      string
		paneWidth int
		want      int
	}{
		{"fills pane at 40 cols", 40, 40},
		{"fills pane at 60 cols", 60, 60},
		{"narrow pane clamps to min 25", 15, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newListModelForTest(t)
			m.width = tt.paneWidth
			// Add a workspace so we're in sidebar mode.
			m.workspaces = []*state.Workspace{{ID: "ws-1", State: state.StateRunning}}

			got := m.sidebarWidth()
			if got != tt.want {
				t.Errorf("sidebarWidth() = %d, want %d", got, tt.want)
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
		Cfg:         config.DefaultConfig(),
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

// ----- Cursor follows active workspace -----

func TestCursorFollowsActive_OnTick(t *testing.T) {
	// Simulate: 3 workspaces, cursor at 0, active is the 3rd workspace.
	// After a tick reloads state, cursor should move to the active workspace.
	m := newListModelForTest(t)

	ws1 := &state.Workspace{ID: "ws-1", Name: "first", State: state.StateRunning, Branch: "b1"}
	ws2 := &state.Workspace{ID: "ws-2", Name: "second", State: state.StateRunning, Branch: "b2"}
	ws3 := &state.Workspace{ID: "ws-3", Name: "third", State: state.StateRunning, Branch: "b3"}
	m.manager.State.Workspaces = map[string]*state.Workspace{
		"ws-1": ws1, "ws-2": ws2, "ws-3": ws3,
	}
	m.manager.State.ActiveWorkspaceID = "ws-3"
	_ = m.manager.SaveState()

	m.workspaces = m.manager.List()
	m.cursor = 0 // cursor stuck at first item

	// Simulate tick: reload state from disk.
	next, _ := m.Update(tickMsg{})
	lm := next.(listModel)

	// Find the index of ws-3 in the refreshed list.
	activeIdx := -1
	for i, ws := range lm.workspaces {
		if ws.ID == "ws-3" {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatal("active workspace ws-3 not found in list")
	}
	if lm.cursor != activeIdx {
		t.Errorf("cursor = %d, want %d (index of active workspace ws-3)", lm.cursor, activeIdx)
	}
}

func TestCursorFollowsActive_OnWorkspaceRemoved(t *testing.T) {
	// Simulate: 3 workspaces, active switches to ws-1 after ws-2 is removed.
	// Cursor should follow the new active workspace.
	m := newListModelForTest(t)

	ws1 := &state.Workspace{ID: "ws-1", Name: "first", State: state.StateRunning, Branch: "b1"}
	ws3 := &state.Workspace{ID: "ws-3", Name: "third", State: state.StateRunning, Branch: "b3"}
	// ws-2 already removed from state; active switched to ws-1.
	m.manager.State.Workspaces = map[string]*state.Workspace{
		"ws-1": ws1, "ws-3": ws3,
	}
	m.manager.State.ActiveWorkspaceID = "ws-1"
	_ = m.manager.SaveState()

	m.workspaces = m.manager.List()
	m.cursor = 1 // cursor was on ws-3

	next, _ := m.Update(workspaceRemovedMsg{id: "ws-2", err: nil})
	lm := next.(listModel)

	activeIdx := -1
	for i, ws := range lm.workspaces {
		if ws.ID == "ws-1" {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatal("active workspace ws-1 not found in list")
	}
	if lm.cursor != activeIdx {
		t.Errorf("cursor = %d, want %d (index of active workspace ws-1)", lm.cursor, activeIdx)
	}
}

func TestCursorFollowsActive_OnWorkspaceCreated(t *testing.T) {
	// Simulate: workspace created, active is the new workspace.
	// Cursor should move to the new active workspace.
	m := newListModelForTest(t)

	ws1 := &state.Workspace{ID: "ws-1", Name: "first", State: state.StateRunning, Branch: "b1"}
	ws2 := &state.Workspace{ID: "ws-2", Name: "second", State: state.StateRunning, Branch: "b2"}
	m.manager.State.Workspaces = map[string]*state.Workspace{
		"ws-1": ws1, "ws-2": ws2,
	}
	m.manager.State.ActiveWorkspaceID = "ws-2"
	m.workspaces = m.manager.List()
	m.cursor = 0 // cursor at first item

	next, _ := m.Update(workspaceCreatedMsg{err: nil})
	lm := next.(listModel)

	activeIdx := -1
	for i, ws := range lm.workspaces {
		if ws.ID == "ws-2" {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatal("active workspace ws-2 not found in list")
	}
	if lm.cursor != activeIdx {
		t.Errorf("cursor = %d, want %d (index of active workspace ws-2)", lm.cursor, activeIdx)
	}
}

// ----- Installer -----

// TestListModel_SKeybinding_ReturnsCommandForRunningWorkspace verifies that
// pressing "S" on a running workspace with a SandboxID returns a non-nil tea.Cmd.
func TestListModel_SKeybinding_ReturnsCommandForRunningWorkspace(t *testing.T) {
	mgr := &workspace.Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{
				"ws-1": {
					ID:        "ws-1",
					Name:      "test",
					State:     state.StateRunning,
					SandboxID: "sha256:abc",
				},
			},
		},
		// Sandbox is nil — SyncHome will return an error, but the cmd should still be non-nil.
	}
	ws := mgr.State.Workspaces["ws-1"]
	m := listModel{
		manager:    mgr,
		workspaces: []*state.Workspace{ws},
		cursor:     0,
	}

	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")}
	_, cmd := m.handleNormalKey(key)
	if cmd == nil {
		t.Error("expected non-nil command for S keybinding on running workspace with SandboxID")
	}
}

// TestListModel_SKeybinding_ReturnsNilCommandWhenNoSandboxID verifies that
// pressing "S" on a workspace with no SandboxID returns a nil tea.Cmd.
func TestListModel_SKeybinding_ReturnsNilCommandWhenNoSandboxID(t *testing.T) {
	mgr := &workspace.Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{
				"ws-1": {
					ID:        "ws-1",
					Name:      "test",
					State:     state.StateRunning,
					SandboxID: "",
				},
			},
		},
	}
	ws := mgr.State.Workspaces["ws-1"]
	m := listModel{
		manager:    mgr,
		workspaces: []*state.Workspace{ws},
		cursor:     0,
	}

	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")}
	_, cmd := m.handleNormalKey(key)
	if cmd != nil {
		t.Error("expected nil command for S keybinding on workspace with no SandboxID")
	}
}

// TestListModel_SKeybinding_ReturnsNilCommandForEmptyList verifies that
// pressing "S" with no workspaces returns a nil tea.Cmd.
func TestListModel_SKeybinding_ReturnsNilCommandForEmptyList(t *testing.T) {
	mgr := &workspace.Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{},
		},
	}
	m := listModel{
		manager:    mgr,
		workspaces: []*state.Workspace{},
		cursor:     0,
	}

	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")}
	_, cmd := m.handleNormalKey(key)
	if cmd != nil {
		t.Error("expected nil command for S keybinding on empty workspace list")
	}
}

// TestInstallerCmdFor verifies that the installer command wraps the script path
// in a bash -c '...' invocation with single quotes so that ~ is NOT expanded by
// the host shell before reaching the container.
// Without this, tmux runs the command via /bin/sh which expands ~ to the host
// home directory — a path that doesn't exist inside the container — causing the
// popup to exit immediately.
func TestInstallerCmdFor(t *testing.T) {
	got := installerCmdFor("abc123")
	const wantPrefix = "docker exec -it abc123 "
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("installerCmdFor = %q, want prefix %q", got, wantPrefix)
	}
	// Must use bash -c with single quotes so ~ is NOT expanded by host shell.
	const wantSubstr = `bash -c 'bash ~/subagents/install-agents.sh`
	if !strings.Contains(got, wantSubstr) {
		t.Errorf("installerCmdFor = %q\nwant to contain %q\n(tilde must be inside single quotes to avoid host shell expansion)", got, wantSubstr)
	}
	// Must NOT have a bare tilde directly after 'docker exec ... bash '.
	after := strings.TrimPrefix(got, wantPrefix)
	if strings.HasPrefix(after, "bash ~/") {
		t.Errorf("installerCmdFor has bare tilde that would be host-expanded: %q", got)
	}
}
