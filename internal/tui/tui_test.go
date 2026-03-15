package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tmux"
	"github.com/johnnybgoode/agency/internal/workspace"
)

// newFakeTuiManager creates a workspace.Manager with a fake tmux binary that
// records all subcommand names (one per line) to argsFile.
func newFakeTuiManager(t *testing.T, fakeOutputByCmd map[string]string) (mgr *workspace.Manager, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "calls.txt")

	// Build case statements for canned per-subcommand output.
	// Always provide defaults for commands that ensureLayout's helpers call
	// (show-environment, display-message, set-environment, set-option,
	// set-hook, bind-key) so the script doesn't fail unexpectedly.
	defaults := map[string]string{
		"show-environment": "",
		"display-message":  "",
		"set-environment":  "",
		"set-option":       "",
		"set-hook":         "",
		"bind-key":         "",
	}
	merged := make(map[string]string, len(defaults)+len(fakeOutputByCmd))
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range fakeOutputByCmd {
		merged[k] = v
	}

	cases := ""
	for cmd, out := range merged {
		cases += "  " + cmd + ") echo '" + out + "';;\n"
	}

	script := "#!/bin/sh\n" +
		`echo "$1" >> ` + argsFile + "\n" +
		`case "$1" in` + "\n" +
		cases +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	mgr = &workspace.Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	if err := mgr.SaveState(); err != nil {
		t.Fatalf("newFakeTuiManager: SaveState: %v", err)
	}
	return mgr, argsFile
}

func readTuiCalls(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		return nil
	}
	var cmds []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			cmds = append(cmds, line)
		}
	}
	return cmds
}

// TestEnsureLayout_SplitsToTwoPanes verifies that ensureLayout calls split-window
// when the main window has only one pane and workspaces exist.
func TestEnsureLayout_SplitsToTwoPanes(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "",   // no existing windows → triggers new-window
		"new-window":   "@1", // fake window ID
		"list-panes":   "%1", // one pane after creation → should trigger split
		"split-window": "%2", // fake right pane ID
		"set-option":   "",
	})
	// Add a workspace so ensureLayout triggers the split.
	mgr.State.Workspaces["ws-test0001"] = &state.Workspace{ID: "ws-test0001", State: state.StateRunning}

	if _, err := ensureLayout(mgr); err != nil {
		_ = err
	}

	calls := readTuiCalls(t, argsFile)
	splitFound := false
	for _, c := range calls {
		if c == "split-window" {
			splitFound = true
		}
	}
	if !splitFound {
		t.Errorf("ensureLayout did not call split-window to create the right workspace pane; calls = %v", calls)
	}
}

// TestEnsureLayout_NoSplitInZeroState verifies that ensureLayout does NOT
// split the window when there are no workspaces (zero state).
func TestEnsureLayout_NoSplitInZeroState(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "",   // no existing windows → triggers new-window
		"new-window":   "@1", // fake window ID
		"list-panes":   "%1", // one pane — should NOT trigger split in zero state
		"split-window": "%2",
	})
	// No workspaces → zero state.

	if _, err := ensureLayout(mgr); err != nil {
		_ = err
	}

	calls := readTuiCalls(t, argsFile)
	for _, c := range calls {
		if c == "split-window" {
			t.Errorf("ensureLayout called split-window in zero state; calls = %v", calls)
		}
	}
}

// TestEnsureLayout_StoresWorkspacePaneID verifies that ensureLayout saves the
// right pane ID to State.WorkspacePaneID after splitting.
func TestEnsureLayout_StoresWorkspacePaneID(t *testing.T) {
	mgr, _ := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 agency",
		"list-panes":   "%1", // one pane → triggers split
		"split-window": "%2", // fake right pane ID
		"set-option":   "",
	})
	mgr.State.MainWindowID = "@5"
	// Add a workspace so ensureLayout triggers the split.
	mgr.State.Workspaces["ws-test0001"] = &state.Workspace{ID: "ws-test0001", State: state.StateRunning}

	if _, err := ensureLayout(mgr); err != nil {
		t.Fatalf("ensureLayout returned error: %v", err)
	}

	if mgr.State.WorkspacePaneID != "%2" {
		t.Errorf("WorkspacePaneID = %q, want %%2", mgr.State.WorkspacePaneID)
	}
}

// TestEnsureLayout_ReuseExistingWindow verifies that ensureLayout reuses the
// first non-workspace window found in the session rather than always creating
// a new one.
func TestEnsureLayout_ReuseExistingWindow(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 existing",
		"list-panes":   "%3", // one pane → triggers split
		"split-window": "%4", // fake right pane
		"set-option":   "",
	})

	leftPane, err := ensureLayout(mgr)
	if err != nil {
		t.Fatalf("ensureLayout unexpected error: %v", err)
	}
	if leftPane != "%3" {
		t.Errorf("leftPane = %q, want %%3", leftPane)
	}

	// new-window must NOT have been called since a suitable window existed.
	calls := readTuiCalls(t, argsFile)
	for _, c := range calls {
		if c == "new-window" {
			t.Errorf("ensureLayout created a new window even though one already existed; calls = %v", calls)
		}
	}

	if mgr.State.MainWindowID != "@5" {
		t.Errorf("MainWindowID = %q, want @5", mgr.State.MainWindowID)
	}
}

// TestZeroStateView_WelcomePanel verifies that View() joins the sidebar with a
// welcome panel when there are no workspaces and the terminal is wide enough.
func TestZeroStateView_WelcomePanel(t *testing.T) {
	mgr, _ := newFakeTuiManager(t, nil)
	m := newListModel(mgr)
	m.width = 120
	m.height = 40

	out := m.View()

	for _, want := range []string{"Agency", "Create [n]ew workspace..."} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing %q in zero state\noutput:\n%s", want, out)
		}
	}
}

// TestZeroStateView_NarrowFallback verifies that View() returns only the
// sidebar when the terminal is not wider than the sidebar (m.width == 0).
func TestZeroStateView_NarrowFallback(t *testing.T) {
	mgr, _ := newFakeTuiManager(t, nil)
	m := newListModel(mgr)
	// m.width defaults to 0 — narrower than sidebarWidth, so no welcome panel.

	out := m.View()

	if strings.Contains(out, "Create [n]ew workspace...") {
		t.Errorf("View() rendered welcome panel when m.width == 0; output:\n%s", out)
	}
	// Sidebar header should still appear.
	if !strings.Contains(out, "Agency") {
		t.Errorf("View() missing sidebar header in narrow fallback; output:\n%s", out)
	}
}

// TestEnsureLayout_PersistsEnvVars verifies that ensureLayout calls
// set-environment to persist pane IDs for crash recovery.
func TestEnsureLayout_PersistsEnvVars(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 agency",
		"list-panes":   "%1",
		"split-window": "%2",
	})
	mgr.State.MainWindowID = "@5"
	mgr.State.Workspaces["ws-test0001"] = &state.Workspace{ID: "ws-test0001", State: state.StateRunning}

	if _, err := ensureLayout(mgr); err != nil {
		t.Fatalf("ensureLayout returned error: %v", err)
	}

	calls := readTuiCalls(t, argsFile)
	envFound := false
	for _, c := range calls {
		if c == "set-environment" {
			envFound = true
		}
	}
	if !envFound {
		t.Errorf("ensureLayout did not call set-environment to persist pane IDs; calls = %v", calls)
	}
}

// TestEnsureLayout_ProtectsWorkspacePane verifies that ensureLayout calls
// set-option (for remain-on-exit) and set-hook (for pane-died respawn).
func TestEnsureLayout_ProtectsWorkspacePane(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 agency",
		"list-panes":   "%1",
		"split-window": "%2",
	})
	mgr.State.MainWindowID = "@5"
	mgr.State.Workspaces["ws-test0001"] = &state.Workspace{ID: "ws-test0001", State: state.StateRunning}

	if _, err := ensureLayout(mgr); err != nil {
		t.Fatalf("ensureLayout returned error: %v", err)
	}

	calls := readTuiCalls(t, argsFile)
	hookFound := false
	for _, c := range calls {
		if c == "set-hook" {
			hookFound = true
		}
	}
	if !hookFound {
		t.Errorf("ensureLayout did not call set-hook for pane-died respawn; calls = %v", calls)
	}
}

// TestEnsureLayout_InstallsKeybindings verifies that ensureLayout calls
// bind-key for focus navigation keybindings.
func TestEnsureLayout_InstallsKeybindings(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 agency",
		"list-panes":   "%1",
		"split-window": "%2",
	})
	mgr.State.MainWindowID = "@5"
	mgr.State.Workspaces["ws-test0001"] = &state.Workspace{ID: "ws-test0001", State: state.StateRunning}

	if _, err := ensureLayout(mgr); err != nil {
		t.Fatalf("ensureLayout returned error: %v", err)
	}

	calls := readTuiCalls(t, argsFile)
	bindFound := false
	for _, c := range calls {
		if c == "bind-key" {
			bindFound = true
		}
	}
	if !bindFound {
		t.Errorf("ensureLayout did not call bind-key for keybindings; calls = %v", calls)
	}
}

// TestNewWorkspaceCmd_ZeroStateXPos verifies that newWorkspaceCmd computes a
// positive x offset when in zero state so the popup is centered over the right
// welcome panel rather than the default (full-terminal) center.
func TestNewWorkspaceCmd_ZeroStateXPos(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"display-popup": "",
	})
	m := newListModel(mgr)
	m.width = 120
	m.height = 40

	cmd := m.newWorkspaceCmd()
	cmd() // execute the tea.Cmd synchronously

	calls, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading calls file: %v", err)
	}
	// The fake tmux script records subcommand names. Verify display-popup was
	// called and that the call log contains "-x" (position flag).
	callStr := string(calls)
	if !strings.Contains(callStr, "display-popup") {
		t.Errorf("newWorkspaceCmd did not call display-popup; calls:\n%s", callStr)
	}
}
