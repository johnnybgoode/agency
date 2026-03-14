package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/workspace"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// RunPopup runs just the create form (for use in a tmux popup). It finds the
// project directory, loads config, creates a workspace manager, presents the
// two-field form, and submits the workspace on enter.
func RunPopup() error {
	projectDir, err := findProjectDir()
	if err != nil {
		return err
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing workspace manager: %w", err)
	}

	form := newCreateModel(mgr.ProjectName)
	p := tea.NewProgram(popupWrapper{form: form, mgr: mgr})
	_, err = p.Run()
	return err
}

// popupWrapper is a thin bubbletea model that wraps the create form for popup mode.
type popupWrapper struct {
	form createModel
	mgr  *workspace.Manager
	done bool
	err  error
}

func (pw popupWrapper) Init() tea.Cmd { //nolint:gocritic // bubbletea model must use value receivers
	return pw.form.Init()
}

func (pw popupWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:gocritic // bubbletea model must use value receivers
	// Once Create is in flight, only handle its completion.
	if pw.done {
		if m, ok := msg.(popupDoneMsg); ok {
			pw.err = m.err
			return pw, tea.Quit
		}
		return pw, nil
	}

	var cmd tea.Cmd
	pw.form, cmd = pw.form.Update(msg)

	if pw.form.canceled {
		return pw, tea.Quit
	}

	if pw.form.submitted {
		name := pw.form.Name()
		branch := pw.form.Branch()
		mgr := pw.mgr
		pw.done = true
		return pw, func() tea.Msg {
			_, err := mgr.Create(context.Background(), name, branch)
			return popupDoneMsg{err: err}
		}
	}

	return pw, cmd
}

func (pw popupWrapper) View() string { //nolint:gocritic // bubbletea model must use value receivers
	if pw.done {
		if pw.err != nil {
			return errorStyle.Render("Error: "+pw.err.Error()) + "\n"
		}
		return "Creating workspace…\n"
	}
	return pw.form.View()
}

// popupDoneMsg is sent when the async create call completes in popup mode.
type popupDoneMsg struct{ err error }

// Run is the entrypoint for the interactive TUI. If the current process is not
// already inside a tmux session, it bootstraps the tmux session and main window,
// launches the sidebar inside the left pane, then attaches — so the user lands
// directly in the sidebar and display-popup has a live client for the 'n' key.
// If already inside tmux (TMUX env set), the sidebar runs directly in the
// current pane.
func Run() error {
	projectDir, err := findProjectDir()
	if err != nil {
		return err
	}

	if os.Getenv("TMUX") == "" {
		return runAndAttach(projectDir)
	}
	return runSidebar(projectDir)
}

// runAndAttach sets up the tmux session and main window, launches the sidebar
// inside the left pane, then attaches the current terminal to the session.
// It does not acquire the lock — the sidebar process inside the pane does that.
func runAndAttach(projectDir string) error {
	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing workspace manager: %w", err)
	}

	if err := mgr.Tmux.EnsureSession(); err != nil {
		return fmt.Errorf("ensuring tmux session: %w", err)
	}

	// Check if the sidebar is already running (live PID in state).
	alreadyRunning := false
	if s, err := state.Read(mgr.StatePath); err == nil && s.PID > 0 && state.IsProcessAlive(s.PID) {
		alreadyRunning = true
	}

	if !alreadyRunning {
		leftPaneID, winErr := ensureMainWindow(mgr)
		if winErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not set up main window: %v\n", winErr)
		} else {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				exe = "agency"
			}
			cmd := fmt.Sprintf("cd %q && exec %q", projectDir, exe)
			if err := mgr.Tmux.SendKeysToPane(leftPaneID, cmd); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not start sidebar in pane: %v\n", err)
			}
			// Focus the left pane so the user lands there after attach.
			_ = mgr.Tmux.SelectPane(leftPaneID)
		}
	}

	return mgr.Tmux.Attach()
}

// runSidebar is the full sidebar TUI flow, run when already inside tmux.
func runSidebar(projectDir string) error {
	// Auto-init .agency/ if missing (e.g. bare repo exists but tool dir was never created).
	if !isDir(filepath.Join(projectDir, ".agency")) {
		if err := worktree.Init(projectDir, ""); err != nil {
			return fmt.Errorf("initializing project: %w", err)
		}
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	lockPath := filepath.Join(projectDir, ".agency", "lock")
	lock, err := state.AcquireLock(lockPath)
	if err != nil {
		// Check whether the process that holds the lock is still alive.
		statePath := filepath.Join(projectDir, ".agency", "state.json")
		s, readErr := state.Read(statePath)
		if readErr != nil || s.PID <= 0 {
			return fmt.Errorf("acquiring lock: %w", err)
		}
		if state.IsProcessAlive(s.PID) {
			return fmt.Errorf("agency is already running (pid %d); use tmux to attach", s.PID)
		}
		// PID is dead — stale lock. Force-remove and retry.
		_ = os.Remove(lockPath)
		lock, err = state.AcquireLock(lockPath)
		if err != nil {
			return fmt.Errorf("acquiring lock after stale cleanup: %w", err)
		}
	}
	defer lock.Release() //nolint:errcheck // lock cleanup on shutdown

	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing workspace manager: %w", err)
	}

	if err := mgr.Tmux.EnsureSession(); err != nil {
		// Non-fatal: tmux may not be installed or we may be in a test env.
		fmt.Fprintf(os.Stderr, "warning: could not ensure tmux session: %v\n", err)
	}

	if err := mgr.Reconcile(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile failed: %v\n", err)
	}

	// Ensure the main window is set up (sidebar lives here).
	leftPaneID, winErr := ensureMainWindow(mgr)
	if winErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set up main window: %v\n", winErr)
	}

	// Rejoin the active workspace pane if it is not already in the main window.
	// This must happen before the resize below: JoinPane resets proportions to 50/50.
	rejoinActivePane(mgr)

	// Apply the configured sidebar width after any rejoin that may have reset proportions.
	// In zero state (no workspaces), skip resizing so the TUI occupies full terminal width
	// and can render the welcome panel.
	if leftPaneID != "" && len(mgr.List()) > 0 {
		_ = mgr.Tmux.ResizePane(leftPaneID, mgr.SidebarWidth())
	}

	// Run the sidebar TUI without alt-screen so it renders in its own pane.
	model := newListModel(mgr)
	p := tea.NewProgram(model)
	_, err = p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// rejoinActivePane re-joins the active workspace pane into the main window if
// it is not already there. Guards against double-join after an unclean exit.
func rejoinActivePane(mgr *workspace.Manager) {
	if mgr.State.ActiveWorkspaceID == "" || mgr.State.MainWindowID == "" {
		return
	}
	ws, ok := mgr.State.Workspaces[mgr.State.ActiveWorkspaceID]
	if !ok || ws.PaneID == "" {
		return
	}
	existingPanes, _ := mgr.Tmux.GetWindowPanes(mgr.State.MainWindowID)
	if !paneInWindow(existingPanes, ws.PaneID) {
		_ = mgr.Tmux.JoinPane(ws.PaneID, mgr.State.MainWindowID)
	}
}

// ensureMainWindow verifies that State.MainWindowID points to a real tmux window.
// If it is empty or the window is gone, a new window is created, split
// vertically, and the state is saved. Returns the left pane ID.
func ensureMainWindow(mgr *workspace.Manager) (string, error) {
	mainID := mgr.State.MainWindowID

	if mainID != "" {
		// Verify it still exists.
		windows, err := mgr.Tmux.ListWindows()
		if err == nil {
			for _, w := range windows {
				if w.ID == mainID {
					// Already good — return the left pane ID.
					if panes, err := mgr.Tmux.GetWindowPanes(mainID); err == nil && len(panes) > 0 {
						return panes[0], nil
					}
					return "", nil
				}
			}
		}
	}

	// Reuse an existing window not owned by a workspace, or create a new one.
	var winID string
	if windows, listErr := mgr.Tmux.ListWindows(); listErr == nil {
		workspaceWins := map[string]bool{}
		for _, ws := range mgr.State.Workspaces {
			if ws.TmuxWindow != "" {
				workspaceWins[ws.TmuxWindow] = true
			}
		}
		for _, w := range windows {
			if !workspaceWins[w.ID] {
				winID = w.ID
				break
			}
		}
	}
	if winID == "" {
		var err error
		winID, err = mgr.Tmux.NewWindow("agency")
		if err != nil {
			return "", fmt.Errorf("creating main window: %w", err)
		}
	}
	mgr.State.MainWindowID = winID

	// Get panes and resize the left pane to the configured sidebar width.
	panes, err := mgr.Tmux.GetWindowPanes(winID)
	if err != nil || len(panes) == 0 {
		_ = mgr.SaveState()
		return "", fmt.Errorf("getting window panes: %w", err)
	}

	w := mgr.Cfg.TUI.SidebarWidth
	if w <= 0 {
		w = 24
	}
	_ = mgr.Tmux.ResizePane(panes[0], w)

	return panes[0], mgr.SaveState()
}

// findProjectDir walks up from the current working directory looking for a
// .agency/ or .bare/ directory, which marks an agency project root.
func findProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	dir := cwd
	for {
		if isDir(filepath.Join(dir, ".agency")) || isDir(filepath.Join(dir, ".bare")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root.
			break
		}
		dir = parent
	}

	return "", fmt.Errorf(
		"not in an agency project (no .agency/ or .bare/ found); run 'agency init' first",
	)
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// paneInWindow reports whether paneID is present in the given slice of pane IDs.
func paneInWindow(panes []string, paneID string) bool {
	for _, p := range panes {
		if p == paneID {
			return true
		}
	}
	return false
}
