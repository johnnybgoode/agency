package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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
	// Simulate: 3 workspaces, cursor at 0, active changes to ws-3.
	// After a tick reloads state, cursor should move to the active workspace.
	m := newListModelForTest(t)

	ws1 := &state.Workspace{ID: "ws-aabbcc01", Name: "first", State: state.StateRunning, Branch: "b1"}
	ws2 := &state.Workspace{ID: "ws-aabbcc02", Name: "second", State: state.StateRunning, Branch: "b2"}
	ws3 := &state.Workspace{ID: "ws-aabbcc03", Name: "third", State: state.StateRunning, Branch: "b3"}
	m.manager.State.Workspaces = map[string]*state.Workspace{
		"ws-aabbcc01": ws1, "ws-aabbcc02": ws2, "ws-aabbcc03": ws3,
	}
	m.manager.State.ActiveWorkspaceID = "ws-aabbcc03"
	_ = m.manager.SaveState()

	m.workspaces = m.manager.List()
	m.cursor = 0        // cursor stuck at first item
	m.lastActiveID = "" // active changed from nothing to ws-aabbcc03

	// Simulate tick: reload state from disk.
	next, _ := m.Update(tickMsg{})
	lm := next.(listModel)

	// Find the index of ws-aabbcc03 in the refreshed list.
	activeIdx := -1
	for i, ws := range lm.workspaces {
		if ws.ID == "ws-aabbcc03" {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatal("active workspace ws-aabbcc03 not found in list")
	}
	if lm.cursor != activeIdx {
		t.Errorf("cursor = %d, want %d (index of active workspace ws-aabbcc03)", lm.cursor, activeIdx)
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
	// Persist state so the handler's disk re-read picks it up.
	_ = m.manager.SaveState()
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

func TestCursorStaysAfterManualMove_OnSubsequentTick(t *testing.T) {
	// After the cursor syncs to the active workspace, the user moves it
	// manually. A subsequent tick (with the same active ID) must NOT snap
	// the cursor back.
	m := newListModelForTest(t)

	ws1 := &state.Workspace{ID: "ws-aabbcc01", Name: "first", State: state.StateRunning, Branch: "b1"}
	ws2 := &state.Workspace{ID: "ws-aabbcc02", Name: "second", State: state.StateRunning, Branch: "b2"}
	ws3 := &state.Workspace{ID: "ws-aabbcc03", Name: "third", State: state.StateRunning, Branch: "b3"}
	m.manager.State.Workspaces = map[string]*state.Workspace{
		"ws-aabbcc01": ws1, "ws-aabbcc02": ws2, "ws-aabbcc03": ws3,
	}
	m.manager.State.ActiveWorkspaceID = "ws-aabbcc03"
	_ = m.manager.SaveState()

	m.workspaces = m.manager.List()
	m.cursor = 0

	// First tick: cursor syncs to active workspace.
	next, _ := m.Update(tickMsg{})
	lm := next.(listModel)

	activeIdx := -1
	for i, ws := range lm.workspaces {
		if ws.ID == "ws-aabbcc03" {
			activeIdx = i
			break
		}
	}
	if lm.cursor != activeIdx {
		t.Fatalf("initial sync failed: cursor = %d, want %d", lm.cursor, activeIdx)
	}

	// User moves cursor up manually.
	lm.cursor = 0

	// Second tick with same active — cursor must stay where the user put it.
	next, _ = lm.Update(tickMsg{})
	lm = next.(listModel)

	if lm.cursor != 0 {
		t.Errorf("cursor = %d after second tick, want 0 (user's manual position)", lm.cursor)
	}
}

// ----- Installer Security -----

// TestInstallAgentsCmd_RejectsInvalidContainerID verifies that installAgentsCmd
// refuses to build a shell command when the workspace SandboxID is not a valid
// Docker sandbox name. Security regression test for audit finding #1.
func TestInstallAgentsCmd_RejectsInvalidContainerID(t *testing.T) {
	maliciousIDs := []string{
		"$(rm -rf /)",
		"; cat /etc/passwd",
		"abc123 && echo pwned",
		"abc|cat /etc/shadow",
		"invalid name with spaces",
		"",
		"has/slash", // slashes not valid
	}

	for _, badID := range maliciousIDs {
		t.Run(badID, func(t *testing.T) {
			m := newListModelForTest(t)
			m.sleepFn = func(time.Duration) {}

			called := false
			m.installerCmd = func(sandboxName string) string {
				called = true
				return "docker sandbox exec -it " + sandboxName + " bash"
			}

			ws := &state.Workspace{
				ID:           "ws-aabbccdd",
				SandboxID:    badID,
				State:        state.StateRunning,
				PaneID:       "%42",
				WorktreePath: t.TempDir(),
			}

			cmd := m.installAgentsCmd(ws)
			if cmd == nil {
				t.Fatal("installAgentsCmd returned nil cmd, expected error-returning cmd")
			}

			msg := cmd()
			if _, ok := msg.(reconcileDoneMsg); !ok {
				t.Fatalf("expected reconcileDoneMsg, got %T", msg)
			}
			if called {
				t.Error("installerCmd was called with an invalid container ID — shell injection possible")
			}
		})
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

// TestSyncDoneMsgUpdatesStatusMsg verifies that syncDoneMsg sets statusMsg on
// success and sets err (clearing statusMsg) on failure.
func TestSyncDoneMsgUpdatesStatusMsg(t *testing.T) {
	m := newListModelForTest(t)

	// Success case: statusMsg set, err cleared.
	result := &workspace.SyncResult{
		Copied:  []string{"file1.txt", "file2.txt"},
		Skipped: nil,
	}
	updated, _ := m.Update(syncDoneMsg{workspaceName: "my-ws", result: result})
	lm := updated.(listModel)
	if lm.statusMsg == "" {
		t.Error("expected statusMsg to be set on sync success")
	}
	if lm.err != nil {
		t.Errorf("expected err to be nil on sync success, got %v", lm.err)
	}

	// Error case: err set, statusMsg cleared.
	lm.statusMsg = "stale success"
	updated2, _ := lm.Update(syncDoneMsg{workspaceName: "my-ws", err: fmt.Errorf("sync failed")})
	lm2 := updated2.(listModel)
	if lm2.err == nil {
		t.Error("expected err to be set on sync failure")
	}
	if lm2.statusMsg != "" {
		t.Errorf("expected statusMsg cleared on sync failure, got %q", lm2.statusMsg)
	}
}

// TestHandleNormalKeyClearsStatusMsg verifies that any keypress in normal mode
// clears the status message. This ensures that sync success messages like
// "synced 5 file(s) from my-ws" don't persist indefinitely in the sidebar.
func TestHandleNormalKeyClearsStatusMsg(t *testing.T) {
	m := newListModelForTest(t)
	m.statusMsg = "synced 3 file(s) from my-ws"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	lm := updated.(listModel)
	if lm.statusMsg != "" {
		t.Errorf("expected statusMsg cleared after keypress, got %q", lm.statusMsg)
	}
}

// TestInstallerCmdFor verifies that the installer command wraps the script path
// in a bash -c '...' invocation with single quotes so that ~ is NOT expanded by
// the host shell before reaching the sandbox.
// Without this, tmux runs the command via /bin/sh which expands ~ to the host
// home directory — a path that doesn't exist inside the sandbox — causing the
// popup to exit immediately.
func TestInstallerCmdFor(t *testing.T) {
	got := installerCmdFor("abc123")
	const wantPrefix = "docker sandbox exec -it abc123 "
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("installerCmdFor = %q, want prefix %q", got, wantPrefix)
	}
	// Must use bash -c with single quotes so ~ is NOT expanded by host shell.
	const wantSubstr = `bash -c 'bash ~/subagents/install-agents.sh`
	if !strings.Contains(got, wantSubstr) {
		t.Errorf("installerCmdFor = %q\nwant to contain %q\n(tilde must be inside single quotes to avoid host shell expansion)", got, wantSubstr)
	}
	// Must NOT have a bare tilde directly after 'docker sandbox exec ... bash '.
	after := strings.TrimPrefix(got, wantPrefix)
	if strings.HasPrefix(after, "bash ~/") {
		t.Errorf("installerCmdFor has bare tilde that would be host-expanded: %q", got)
	}
}

// ----- refreshCursorPosition -----

func TestRefreshCursorPosition(t *testing.T) {
	ws1 := &state.Workspace{ID: "ws-aaaaaaaa", Name: "first"}
	ws2 := &state.Workspace{ID: "ws-bbbbbbbb", Name: "second"}

	makeModel := func(workspaces []*state.Workspace, activeID string, cursor int) listModel {
		s := &state.State{
			ActiveWorkspaceID: activeID,
			Workspaces:        make(map[string]*state.Workspace),
		}
		for _, ws := range workspaces {
			s.Workspaces[ws.ID] = ws
		}
		return listModel{
			manager:    &workspace.Manager{State: s},
			workspaces: workspaces,
			cursor:     cursor,
			removing:   make(map[string]bool),
		}
	}

	tests := []struct {
		name       string
		workspaces []*state.Workspace
		activeID   string
		cursor     int
		wantCursor int
	}{
		{
			name:       "clamps cursor when list shrinks",
			workspaces: []*state.Workspace{ws1},
			activeID:   "",
			cursor:     5,
			wantCursor: 0,
		},
		{
			name:       "syncs cursor to active workspace",
			workspaces: []*state.Workspace{ws1, ws2},
			activeID:   "ws-bbbbbbbb",
			cursor:     0,
			wantCursor: 1,
		},
		{
			name:       "no-op when list is empty",
			workspaces: []*state.Workspace{},
			activeID:   "",
			cursor:     0,
			wantCursor: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := makeModel(tt.workspaces, tt.activeID, tt.cursor)
			m = m.refreshCursorPosition()
			if m.cursor != tt.wantCursor {
				t.Errorf("cursor = %d, want %d", m.cursor, tt.wantCursor)
			}
			if m.lastActiveID != tt.activeID {
				t.Errorf("lastActiveID = %q, want %q", m.lastActiveID, tt.activeID)
			}
		})
	}
}

// ----- classifyStatus -----

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		name    string
		current string
		prev    string
		want    AgentStatus
	}{
		{
			name:    "idle: last line is >",
			current: "some output\n> ",
			prev:    "some output\n> ",
			want:    AgentStatusIdle,
		},
		{
			name:    "idle: empty pane",
			current: "",
			prev:    "",
			want:    AgentStatusIdle,
		},
		{
			name:    "idle: bare > with trailing space",
			current: "Task done.\n> ",
			prev:    "anything",
			want:    AgentStatusIdle,
		},
		{
			name:    "idle: unicode prompt ❯",
			current: "Task done.\n❯",
			prev:    "Task done.\n❯",
			want:    AgentStatusIdle,
		},
		{
			name:    "idle: unicode prompt ❯ with trailing space",
			current: "Task done.\n❯ ",
			prev:    "anything",
			want:    AgentStatusIdle,
		},
		{
			name:    "working: content changed and not at prompt",
			current: "Running tests...\nStep 2",
			prev:    "Running tests...",
			want:    AgentStatusWorking,
		},
		{
			name:    "working: first content after empty prev",
			current: "Starting...",
			prev:    "",
			want:    AgentStatusWorking,
		},
		{
			name:    "waiting: content unchanged and not at prompt",
			current: "Do you want to proceed?\n❯ Yes\n  No",
			prev:    "Do you want to proceed?\n❯ Yes\n  No",
			want:    AgentStatusWaiting,
		},
		{
			name:    "waiting: dialog pattern with selection cursor",
			current: "Allow bash command?\n❯ Yes\n  No\n  Always allow",
			prev:    "some different content",
			want:    AgentStatusWaiting,
		},
		{
			name:    "waiting: dialog pattern with [Y/n]",
			current: "Overwrite existing file? [Y/n]",
			prev:    "other content",
			want:    AgentStatusWaiting,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyStatus(tt.current, tt.prev)
			if got != tt.want {
				t.Errorf("classifyStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ----- workspaceStatusRow -----

func intPtr(v int) *int { return &v }

func TestWorkspaceStatusRow(t *testing.T) {
	tests := []struct {
		name           string
		ws             *state.Workspace
		status         AgentStatus
		ctxPct         *int
		wantContain    string
		wantNotContain string
	}{
		{
			name:        "running idle shows idle glyph",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:      AgentStatusIdle,
			wantContain: "·",
		},
		{
			name:        "running working shows working glyph",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:      AgentStatusWorking,
			wantContain: "●",
		},
		{
			name:        "running waiting shows waiting glyph",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:      AgentStatusWaiting,
			wantContain: "⚠",
		},
		{
			name:        "paused workspace shows paused label",
			ws:          &state.Workspace{ID: "ws-1", State: state.StatePaused},
			status:      AgentStatusUnknown,
			wantContain: "paused",
		},
		{
			name:        "creating workspace shows creating label",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateCreating},
			status:      AgentStatusUnknown,
			wantContain: "creating",
		},
		{
			name:        "status row is indented with 3 spaces",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:      AgentStatusIdle,
			wantContain: "   ",
		},
		{
			name:        "running with context shows bar",
			ws:          &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:      AgentStatusWorking,
			ctxPct:      intPtr(32),
			wantContain: "32%",
		},
		{
			name:           "running without context shows no bar",
			ws:             &state.Workspace{ID: "ws-1", State: state.StateRunning},
			status:         AgentStatusWorking,
			wantNotContain: "▓",
		},
		{
			name:           "paused ignores context data",
			ws:             &state.Workspace{ID: "ws-1", State: state.StatePaused},
			status:         AgentStatusUnknown,
			ctxPct:         intPtr(50),
			wantContain:    "paused",
			wantNotContain: "▓",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workspaceStatusRow(tt.ws, tt.status, tt.ctxPct)
			if tt.wantContain != "" && !strings.Contains(got, tt.wantContain) {
				t.Errorf("workspaceStatusRow() = %q, want to contain %q", got, tt.wantContain)
			}
			if tt.wantNotContain != "" && strings.Contains(got, tt.wantNotContain) {
				t.Errorf("workspaceStatusRow() = %q, want NOT to contain %q", got, tt.wantNotContain)
			}
		})
	}
}

// ----- contextBar -----

func TestContextBar(t *testing.T) {
	tests := []struct {
		name        string
		pct         int
		wantFilled  int
		wantContain string
	}{
		{"0 pct", 0, 0, "0%"},
		{"32 pct", 32, 1, "32%"},
		{"50 pct", 50, 2, "50%"},
		{"80 pct", 80, 4, "80%"},
		{"100 pct", 100, 5, "100%"},
		{"negative clamped", -5, 0, "0%"},
		{"over 100 clamped", 150, 5, "100%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contextBar(tt.pct)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("contextBar(%d) = %q, want to contain %q", tt.pct, got, tt.wantContain)
			}
			filled := strings.Count(got, "▓")
			if filled != tt.wantFilled {
				t.Errorf("contextBar(%d) has %d filled chars, want %d", tt.pct, filled, tt.wantFilled)
			}
		})
	}
}

// ----- pollAgentStatuses reads status files -----

func TestPollAgentStatuses_ReadsStatusFile(t *testing.T) {
	dir := t.TempDir()

	// Write a fresh status file.
	statusJSON := fmt.Sprintf(`{
		"session_id": "test-session",
		"context_window": {"used_percentage": 42},
		"rate_limits": {
			"five_hour": {"used_percentage": 10},
			"seven_day": {"used_percentage": 3}
		},
		"updated_at": %q
	}`, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(dir+"/.agency-status.json", []byte(statusJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	m := listModel{
		workspaces: []*state.Workspace{
			{ID: "ws-1", State: state.StateRunning, WorktreePath: dir, PaneID: ""},
		},
		prevPaneContent:  make(map[string]string),
		agentStatus:      make(map[string]AgentStatus),
		agentContextData: make(map[string]*agentStatusFile),
	}
	m = m.pollAgentStatuses()

	sf := m.agentContextData["ws-1"]
	if sf == nil {
		t.Fatal("expected agentContextData for ws-1, got nil")
	}
	if sf.ContextWindow.UsedPercentage != 42 {
		t.Errorf("context pct = %d, want 42", sf.ContextWindow.UsedPercentage)
	}
	if sf.RateLimits.FiveHour.UsedPercentage != 10 {
		t.Errorf("five_hour pct = %d, want 10", sf.RateLimits.FiveHour.UsedPercentage)
	}
}

func TestPollAgentStatuses_DiscardsStaleFile(t *testing.T) {
	dir := t.TempDir()

	staleTime := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	statusJSON := fmt.Sprintf(`{
		"session_id": "test-session",
		"context_window": {"used_percentage": 42},
		"rate_limits": {},
		"updated_at": %q
	}`, staleTime)
	if err := os.WriteFile(dir+"/.agency-status.json", []byte(statusJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	m := listModel{
		workspaces: []*state.Workspace{
			{ID: "ws-1", State: state.StateRunning, WorktreePath: dir, PaneID: ""},
		},
		prevPaneContent:  make(map[string]string),
		agentStatus:      make(map[string]AgentStatus),
		agentContextData: make(map[string]*agentStatusFile),
	}
	m = m.pollAgentStatuses()

	if m.agentContextData["ws-1"] != nil {
		t.Error("expected stale data to be discarded, got non-nil")
	}
}

// ----- selectedWorkspace -----

func TestSelectedWorkspace(t *testing.T) {
	ws1 := &state.Workspace{ID: "ws-aaaaaaaa", Name: "first"}
	ws2 := &state.Workspace{ID: "ws-bbbbbbbb", Name: "second"}

	tests := []struct {
		name       string
		workspaces []*state.Workspace
		cursor     int
		wantNil    bool
		wantID     string
	}{
		{"returns workspace at cursor", []*state.Workspace{ws1, ws2}, 1, false, "ws-bbbbbbbb"},
		{"returns nil when list empty", []*state.Workspace{}, 0, true, ""},
		{"returns nil when cursor out of bounds", []*state.Workspace{ws1}, 5, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := listModel{workspaces: tt.workspaces, cursor: tt.cursor, removing: make(map[string]bool)}
			got := m.selectedWorkspace()
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil workspace")
			}
			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}
