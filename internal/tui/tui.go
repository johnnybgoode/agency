package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/session"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// RunPopup runs just the create form (for use in a tmux popup). It finds the
// project directory, loads config, creates a session manager, presents the
// two-field form, and submits the session on enter.
func RunPopup() error {
	projectDir, err := findProjectDir()
	if err != nil {
		return err
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	mgr, err := session.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing session manager: %w", err)
	}

	form := newCreateModel(mgr.ProjectName)
	p := tea.NewProgram(popupWrapper{form: form, mgr: mgr})
	_, err = p.Run()
	return err
}

// popupWrapper is a thin bubbletea model that wraps the create form for popup mode.
type popupWrapper struct {
	form createModel
	mgr  *session.Manager
	done bool
	err  error
}

func (pw popupWrapper) Init() tea.Cmd { return pw.form.Init() }

func (pw popupWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (pw popupWrapper) View() string {
	if pw.done {
		if pw.err != nil {
			return errorStyle.Render("Error: "+pw.err.Error()) + "\n"
		}
		return ""
	}
	return pw.form.View()
}

// popupDoneMsg is sent when the async create call completes in popup mode.
type popupDoneMsg struct{ err error }

// Run is the entrypoint for the interactive TUI. It locates the project
// directory, acquires an exclusive lock, reconciles session state, then hands
// control to Bubble Tea running the sidebar (no alt-screen).
func Run() error {
	projectDir, err := findProjectDir()
	if err != nil {
		return err
	}

	// Auto-init .tool/ if missing (e.g. bare repo exists but tool dir was never created).
	if !isDir(filepath.Join(projectDir, ".tool")) {
		if err := worktree.Init(projectDir, ""); err != nil {
			return fmt.Errorf("initializing project: %w", err)
		}
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	lockPath := filepath.Join(projectDir, ".tool", "lock")
	lock, err := state.AcquireLock(lockPath)
	if err != nil {
		// Check whether the process that holds the lock is still alive.
		statePath := filepath.Join(projectDir, ".tool", "state.json")
		if s, readErr := state.Read(statePath); readErr == nil && s.PID > 0 {
			if state.IsProcessAlive(s.PID) {
				return fmt.Errorf("agency is already running (pid %d); use tmux to attach", s.PID)
			}
			// PID is dead — stale lock. Force-remove and retry.
			os.Remove(lockPath) //nolint:errcheck
			lock, err = state.AcquireLock(lockPath)
			if err != nil {
				return fmt.Errorf("acquiring lock after stale cleanup: %w", err)
			}
		} else {
			return fmt.Errorf("acquiring lock: %w", err)
		}
	}
	defer lock.Release() //nolint:errcheck // lock cleanup on shutdown

	mgr, err := session.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing session manager: %w", err)
	}

	if err := mgr.Tmux.EnsureSession(); err != nil {
		// Non-fatal: tmux may not be installed or we may be in a test env.
		fmt.Fprintf(os.Stderr, "warning: could not ensure tmux session: %v\n", err)
	}

	if err := mgr.Reconcile(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile failed: %v\n", err)
	}

	// Ensure the main window is set up (sidebar lives here).
	if err := ensureMainWindow(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set up main window: %v\n", err)
	}

	// If there is an active session, join its pane into the main window only if
	// it is not already there (guards against double-join after an unclean exit).
	if mgr.State.ActiveSessionID != "" && mgr.State.MainWindowID != "" {
		if sess, ok := mgr.State.Sessions[mgr.State.ActiveSessionID]; ok && sess.PaneID != "" {
			existingPanes, _ := mgr.Tmux.GetWindowPanes(mgr.State.MainWindowID)
			if !paneInWindow(existingPanes, sess.PaneID) {
				_ = mgr.Tmux.JoinPane(sess.PaneID, mgr.State.MainWindowID)
			}
		}
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

// ensureMainWindow verifies that State.MainWindowID points to a real tmux window.
// If it is empty or the window is gone, a new window is created, split
// vertically, and the state is saved.
func ensureMainWindow(mgr *session.Manager) error {
	mainID := mgr.State.MainWindowID

	if mainID != "" {
		// Verify it still exists.
		windows, err := mgr.Tmux.ListWindows()
		if err == nil {
			for _, w := range windows {
				if w.ID == mainID {
					// Already good.
					return nil
				}
			}
		}
	}

	// Create a new window to serve as the main (sidebar) window.
	winID, err := mgr.Tmux.NewWindow("agency")
	if err != nil {
		return fmt.Errorf("creating main window: %w", err)
	}
	mgr.State.MainWindowID = winID

	// Split it vertically — the right pane starts empty.
	// We discard the new right pane ID; it will be replaced when the user selects a session.
	_, _ = mgr.Tmux.SplitWindowVertical(winID)

	// Resize the left pane (original/first pane) to the configured sidebar width.
	if panes, err := mgr.Tmux.GetWindowPanes(winID); err == nil && len(panes) > 0 {
		w := mgr.Cfg.TUI.SidebarWidth
		if w <= 0 {
			w = 24
		}
		_ = mgr.Tmux.ResizePane(panes[0], w)
	}

	return mgr.SaveState()
}

// findProjectDir walks up from the current working directory looking for a
// .tool/ or .bare/ directory, which marks an agency project root.
func findProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	dir := cwd
	for {
		if isDir(filepath.Join(dir, ".tool")) || isDir(filepath.Join(dir, ".bare")) {
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
		"not in an agency project (no .tool/ or .bare/ found); run 'agency init' first",
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
