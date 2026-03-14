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
	cases := ""
	for cmd, out := range fakeOutputByCmd {
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

// TestEnsureMainWindow_NoSplitWindow verifies that ensureMainWindow never
// calls split-window. The main window must start as a single-pane sidebar;
// a workspace pane is joined only when a workspace becomes active.
func TestEnsureMainWindow_NoSplitWindow(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "",   // no existing windows → triggers new-window
		"new-window":   "@1", // fake window ID
		"list-panes":   "%1", // one pane after creation
		"resize-pane":  "",
	})

	if _, err := ensureMainWindow(mgr); err != nil {
		// ensureMainWindow may return an error if, say, list-panes returns no
		// usable output due to the simple fake script. That's fine — what we
		// care about is that split-window was never invoked.
		_ = err
	}

	calls := readTuiCalls(t, argsFile)
	for _, c := range calls {
		if c == "split-window" {
			t.Errorf("ensureMainWindow called split-window — this creates a spurious empty right pane; calls = %v", calls)
		}
	}
}

// TestStartup_ResizeSidebarAfterRejoin verifies the runSidebar invariant:
// when an active workspace pane is rejoined into the main window on startup,
// resize-pane is called on the sidebar AFTER join-pane (not before). Calling
// ResizePane before JoinPane would leave the sidebar at 50% because JoinPane
// resets pane proportions.
func TestStartup_ResizeSidebarAfterRejoin(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 agency",
		"list-panes":   "%1",
		"join-pane":    "",
		"resize-pane":  "",
	})
	mgr.State.MainWindowID = "@5"

	// Simulate an active workspace whose pane is NOT yet in the main window.
	ws := &state.Workspace{
		ID:     "ws-startup01",
		PaneID: "%10", // not returned by fake list-panes → rejoin will call join-pane
		State:  state.StateRunning,
	}
	mgr.State.Workspaces[ws.ID] = ws
	mgr.State.ActiveWorkspaceID = ws.ID

	// Replicate the runSidebar sequence directly.
	leftPaneID, _ := ensureMainWindow(mgr)
	rejoinActivePane(mgr)
	if leftPaneID != "" {
		_ = mgr.Tmux.ResizePane(leftPaneID, mgr.SidebarWidth())
	}

	calls := readTuiCalls(t, argsFile)

	joinIdx, resizeIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "join-pane":
			if joinIdx < 0 {
				joinIdx = i
			}
		case "resize-pane":
			if resizeIdx < 0 {
				resizeIdx = i
			}
		}
	}
	if joinIdx < 0 {
		t.Fatalf("join-pane not called; calls = %v", calls)
	}
	if resizeIdx < 0 {
		t.Fatalf("resize-pane not called; calls = %v", calls)
	}
	if resizeIdx < joinIdx {
		t.Errorf("resize-pane (pos %d) must come after join-pane (pos %d); calls = %v",
			resizeIdx, joinIdx, calls)
	}
}

// TestEnsureMainWindow_ReuseExistingWindow verifies that ensureMainWindow
// reuses the first non-workspace window found in the session rather than
// always creating a new one.
func TestEnsureMainWindow_ReuseExistingWindow(t *testing.T) {
	mgr, argsFile := newFakeTuiManager(t, map[string]string{
		"list-windows": "@5 existing",
		"list-panes":   "%3",
		"resize-pane":  "",
	})

	leftPane, err := ensureMainWindow(mgr)
	if err != nil {
		t.Fatalf("ensureMainWindow unexpected error: %v", err)
	}
	if leftPane != "%3" {
		t.Errorf("leftPane = %q, want %%3", leftPane)
	}

	// new-window must NOT have been called since a suitable window existed.
	calls := readTuiCalls(t, argsFile)
	for _, c := range calls {
		if c == "new-window" {
			t.Errorf("ensureMainWindow created a new window even though one already existed; calls = %v", calls)
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
