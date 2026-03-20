package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/project"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tmux"
	"github.com/johnnybgoode/agency/internal/workspace"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// newManager finds the project directory, loads config, and creates a workspace
// Manager. Returns an error if any step fails.
func newManager() (*workspace.Manager, *config.Config, error) {
	projectDir, err := project.FindProjectDir()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initializing workspace manager: %w", err)
	}
	return mgr, cfg, nil
}

// RunPopup runs just the create form (for use in a tmux popup). It finds the
// project directory, loads config, creates a workspace manager, presents the
// two-field form, and submits the workspace on enter.
func RunPopup() error {
	mgr, _, err := newManager()
	if err != nil {
		return err
	}
	form := newCreateModel(mgr.ProjectName)
	p := tea.NewProgram(popupWrapper{form: form, mgr: mgr})
	_, err = p.Run()
	return err
}

// QuitResultFile is the filename written by the quit popup to communicate results
// back to the sidebar process.
const QuitResultFile = "quit-result.json"

// QuitResultData is the JSON structure written by the quit popup.
// Infos is populated in-process after re-assessment and is never serialized.
type QuitResultData struct {
	Confirmed bool                 `json:"confirmed"`
	Infos     []workspace.QuitInfo `json:"-"` // not serialized; used in-process
}

// RunQuitPopup runs the quit confirmation flow as a standalone bubbletea program
// (for use inside a tmux popup). It assesses workspace statuses, presents the
// confirmation dialog, and writes the result to .agency/quit-result.json.
func RunQuitPopup() error {
	mgr, cfg, err := newManager()
	if err != nil {
		return err
	}

	// Assess workspace statuses synchronously (fast local git checks).
	infos, err := mgr.AssessQuitStatuses(context.Background())
	if err != nil {
		return fmt.Errorf("assessing quit statuses: %w", err)
	}

	model := newQuitPopupModel(infos, cfg.TUI.Theme)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("quit popup TUI error: %w", err)
	}

	qm := finalModel.(quitPopupModel)
	resultPath := filepath.Join(mgr.ProjectDir, ".agency", QuitResultFile)
	data, _ := json.Marshal(QuitResultData{Confirmed: qm.result.Confirmed})
	return os.WriteFile(resultPath, data, 0o600)
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
	projectDir, err := project.FindProjectDir()
	if err != nil {
		return err
	}
	slog.Info("TUI starting", "projectDir", projectDir)

	if os.Getenv("TMUX") == "" {
		return runAndAttach(projectDir)
	}
	return runSidebar(projectDir)
}

// runAndAttach sets up the tmux session, launches the sidebar inside a pane,
// then attaches the current terminal to the session. It intentionally does NOT
// split the window — runSidebar handles the 2-pane layout after the terminal is
// attached so that tmux applies percentages against the real terminal size.
// It does not acquire the lock — the sidebar process inside the pane does that.
func runAndAttach(projectDir string) error {
	slog.Info("bootstrapping tmux session", "projectDir", projectDir)
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
		leftPaneID, findErr := findSidebarPane(mgr)
		if findErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not find pane for sidebar: %v\n", findErr)
		} else {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				exe = "agency"
			}
			// Loop restarts agency on non-zero exit (crash). Exit 0 (graceful quit) breaks the loop.
			cmd := fmt.Sprintf("cd %q && while ! %q; do true; done", projectDir, exe)
			if err := mgr.Tmux.SendKeysToPane(leftPaneID, cmd); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not start sidebar in pane: %v\n", err)
			}
			// Focus the left pane so the user lands there after attach.
			_ = mgr.Tmux.SelectPane(leftPaneID)
		}
	}

	return mgr.Tmux.Attach()
}

// firstNonWorkspaceWindow returns the ID of the first window that does not
// belong to a workspace. Returns ("", false) if no such window exists.
func firstNonWorkspaceWindow(windows []tmux.Window, workspaces map[string]*state.Workspace) (string, bool) {
	wsWins := make(map[string]bool, len(workspaces))
	for _, ws := range workspaces {
		if ws.TmuxWindow != "" {
			wsWins[ws.TmuxWindow] = true
		}
	}
	for _, w := range windows {
		if !wsWins[w.ID] {
			return w.ID, true
		}
	}
	return "", false
}

// findSidebarPane finds or creates a single-pane window suitable for the
// sidebar. It reuses the first non-workspace window, or creates a new one.
// Does NOT split — the split is deferred to runSidebar's ensureLayout call.
func findSidebarPane(mgr *workspace.Manager) (string, error) {
	windows, err := mgr.Tmux.ListWindows()
	if err != nil {
		return "", err
	}

	winID, _ := firstNonWorkspaceWindow(windows, mgr.State.Workspaces)

	if winID == "" {
		winID, err = mgr.Tmux.NewWindow("agency")
		if err != nil {
			return "", fmt.Errorf("creating window: %w", err)
		}
	}

	panes, err := mgr.Tmux.GetWindowPanes(winID)
	if err != nil || len(panes) == 0 {
		return "", fmt.Errorf("getting panes: %w", err)
	}

	return panes[0], nil
}

// acquireSidebarLock acquires the agency flock, handling the stale-lock case
// where the previous holder's PID is dead or was reused by another process.
func acquireSidebarLock(lockPath, statePath string) (*state.Lock, error) {
	lock, err := state.AcquireLock(lockPath)
	if err == nil {
		slog.Debug("lock acquired", "path", lockPath)
		return lock, nil
	}

	// Check whether the process that holds the lock is still alive.
	s, readErr := state.Read(statePath)
	if readErr != nil || s.PID <= 0 {
		return nil, fmt.Errorf("acquiring lock: %w", err)
	}

	// Nonce cross-check: read the nonce currently in the lock file and
	// compare it against the nonce stored in state.json. A mismatch means
	// the lock file was rewritten by a different process instance (PID
	// reuse), so we treat it as stale regardless of IsProcessAlive.
	lockFileNonce := ""
	if data, readNonceErr := os.ReadFile(lockPath); readNonceErr == nil {
		lockFileNonce = strings.TrimSpace(string(data))
	}
	nonceMismatch := s.LockNonce != "" && lockFileNonce != "" && lockFileNonce != s.LockNonce

	if !nonceMismatch && state.IsProcessAlive(s.PID) {
		return nil, fmt.Errorf("agency is already running (pid %d); use tmux to attach", s.PID)
	}

	// Either the PID is dead or the nonce doesn't match (PID was reused by
	// a different process). The flock was auto-released when the dead
	// process's file descriptor closed on exit; simply retry acquisition on
	// the same file. Removing the file first would open a TOCTOU window
	// where another process could steal the lock between Remove and OpenFile.
	if nonceMismatch {
		slog.Warn("stale lock detected via nonce mismatch (PID reuse)", "stalePID", s.PID, "stateNonce", s.LockNonce, "lockNonce", lockFileNonce)
	} else {
		slog.Warn("stale lock detected, retrying acquisition", "stalePID", s.PID)
	}
	lock, err = state.AcquireLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock after stale process detected: %w", err)
	}
	slog.Debug("lock acquired", "path", lockPath)
	return lock, nil
}

// runSidebar is the full sidebar TUI flow, run when already inside tmux.
func runSidebar(projectDir string) error {
	slog.Info("running sidebar", "projectDir", projectDir)
	// Auto-init .agency/ if missing (e.g. bare repo exists but tool dir was never created).
	if !project.IsDir(filepath.Join(projectDir, ".agency")) {
		if err := worktree.Init(projectDir, ""); err != nil {
			return fmt.Errorf("initializing project: %w", err)
		}
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	lockPath := filepath.Join(projectDir, ".agency", "lock")
	statePath := filepath.Join(projectDir, ".agency", "state.json")
	lock, err := acquireSidebarLock(lockPath, statePath)
	if err != nil {
		return err
	}
	defer lock.Release() //nolint:errcheck // lock cleanup on shutdown

	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing workspace manager: %w", err)
	}

	// Persist the lock nonce so that the next startup can cross-check it
	// against the lock file to detect PID reuse in stale-lock detection.
	mgr.State.LockNonce = lock.Nonce()
	if err := mgr.SaveState(); err != nil {
		return fmt.Errorf("persisting lock nonce to state: %w", err)
	}

	// Ensure the 2-pane layout is set up (sidebar left, workspace shell right).
	leftPaneID, winErr := ensureLayout(mgr)
	if winErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set up main window: %v\n", winErr)
	}

	// Apply the configured sidebar width.
	// In zero state (no workspaces), skip resizing so the TUI occupies full terminal width
	// and can render the welcome panel.
	if leftPaneID != "" && len(mgr.List()) > 0 {
		if tw, twErr := mgr.Tmux.WindowWidth(mgr.State.MainWindowID); twErr == nil {
			_ = mgr.Tmux.ResizePane(leftPaneID, mgr.SidebarColumns(tw))
		}
	}

	// Clear the terminal immediately to mask the shell echo of the launch command.
	fmt.Print("\033[2J\033[H")

	// Run the sidebar TUI without alt-screen so it renders in its own pane.
	model := newListModel(mgr)
	p := tea.NewProgram(model, tea.WithMouseCellMotion())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	if lm, ok := finalModel.(listModel); ok && lm.shouldKillSession {
		doQuitCleanup(mgr, lm.quitInfos)
		// Clear SessionStartedAt so next launch gets a fresh session log.
		mgr.State.SessionStartedAt = nil
		_ = mgr.SaveState()
		_ = mgr.Tmux.KillSession()
	}

	return nil
}

// recoverLayoutFromEnv attempts to recover pane IDs from tmux session
// environment variables. Returns (navPane, workspacePane, mainWindow, ok).
func recoverLayoutFromEnv(mgr *workspace.Manager) (navPane, workspacePane, mainWindow string, ok bool) {
	navPane, _ = mgr.Tmux.GetEnvironment(tmux.EnvNavPane)
	workspacePane, _ = mgr.Tmux.GetEnvironment(tmux.EnvWorkspacePane)
	mainWindow, _ = mgr.Tmux.GetEnvironment(tmux.EnvMainWindow)

	if navPane == "" || workspacePane == "" || mainWindow == "" {
		return "", "", "", false
	}

	// Verify both panes actually exist in tmux.
	if !mgr.Tmux.PaneExists(navPane) || !mgr.Tmux.PaneExists(workspacePane) {
		return "", "", "", false
	}

	slog.Info("layout recovered from env vars", "navPane", navPane, "workspacePane", workspacePane, "mainWindow", mainWindow)
	return navPane, workspacePane, mainWindow, true
}

// persistLayoutEnv writes pane IDs to tmux session environment variables
// for crash-resilient rediscovery.
func persistLayoutEnv(mgr *workspace.Manager, navPane, wsPane, mainWin string) {
	_ = mgr.Tmux.SetEnvironment(tmux.EnvNavPane, navPane)
	_ = mgr.Tmux.SetEnvironment(tmux.EnvWorkspacePane, wsPane)
	_ = mgr.Tmux.SetEnvironment(tmux.EnvMainWindow, mainWin)
}

// protectWorkspacePane sets remain-on-exit and installs a pane-died hook
// so that ctrl+d in the right pane respawns a fresh shell instead of killing it.
func protectWorkspacePane(mgr *workspace.Manager, rightPaneID string) {
	_ = mgr.Tmux.SetPaneOption(rightPaneID, "remain-on-exit", "on")
	hookCmd := fmt.Sprintf(
		"if-shell -F '#{==:#{pane_id},%s}' 'respawn-pane -t %s'",
		rightPaneID, rightPaneID,
	)
	_ = mgr.Tmux.SetHook("respawn-workspace", "pane-died", hookCmd)
}

// ensureLayout verifies that State.MainWindowID points to a real tmux window.
// When workspaces exist, it ensures a 2-pane horizontal split (left = sidebar
// TUI, right = workspace shell). In zero state (no workspaces), the window is
// kept as a single pane so the welcome panel can use the full terminal width.
// The right pane is created on demand by SwapActivePane when the first workspace
// is created. Returns the left pane ID.
func ensureLayout(mgr *workspace.Manager) (string, error) {
	hasWorkspaces := len(mgr.State.Workspaces) > 0

	// Attempt crash recovery from tmux env vars first (only when workspaces exist).
	if hasWorkspaces {
		if navPane, wsPane, mainWin, ok := recoverLayoutFromEnv(mgr); ok {
			mgr.State.MainWindowID = mainWin
			mgr.State.WorkspacePaneID = wsPane
			_ = mgr.SaveState()
			finalizeLayout(mgr, wsPane)
			return navPane, nil
		}
	}

	winID, err := resolveMainWindow(mgr)
	if err != nil {
		return "", err
	}
	mgr.State.MainWindowID = winID

	panes, err := mgr.Tmux.GetWindowPanes(winID)
	if err != nil || len(panes) == 0 {
		_ = mgr.SaveState()
		return "", fmt.Errorf("getting window panes: %w", err)
	}

	// Only split when workspaces exist — in zero state the TUI occupies the
	// full window and renders a welcome panel.
	if hasWorkspaces {
		ensureRightPane(mgr, winID, panes)
	}

	finalizeLayout(mgr, mgr.State.WorkspacePaneID)
	if mgr.State.WorkspacePaneID != "" {
		persistLayoutEnv(mgr, panes[0], mgr.State.WorkspacePaneID, winID)
	}

	return panes[0], mgr.SaveState()
}

// resolveMainWindow returns the window ID for the main Agency window.
// It verifies State.MainWindowID is still valid, then falls back to reusing an
// existing non-workspace window, and finally creates a new one.
func resolveMainWindow(mgr *workspace.Manager) (string, error) {
	mainID := mgr.State.MainWindowID
	windows, listErr := mgr.Tmux.ListWindows()

	// Check if the saved MainWindowID still exists.
	if mainID != "" && listErr == nil {
		for _, w := range windows {
			if w.ID == mainID {
				return mainID, nil
			}
		}
	}

	// Reuse the first non-workspace window if available.
	if listErr == nil {
		if winID, ok := firstNonWorkspaceWindow(windows, mgr.State.Workspaces); ok {
			return winID, nil
		}
	}

	winID, err := mgr.Tmux.NewWindow("agency")
	if err != nil {
		return "", fmt.Errorf("creating main window: %w", err)
	}
	return winID, nil
}

// ensureRightPane splits the window if only one pane exists, or records an
// existing right pane if WorkspacePaneID is unset.
func ensureRightPane(mgr *workspace.Manager, winID string, panes []string) {
	if len(panes) == 1 {
		rightPaneID, splitErr := mgr.Tmux.SplitWindowHorizontalPercent(winID, workspace.DefaultWorkspaceSplitPercent)
		if splitErr == nil && rightPaneID != "" {
			mgr.State.WorkspacePaneID = rightPaneID
		}
	} else if mgr.State.WorkspacePaneID == "" {
		mgr.State.WorkspacePaneID = panes[1]
	}
}

// verifyLayoutIntegrity checks that pane IDs in state still correspond to
// live panes in the correct tmux window. Clears stale references so that
// ensureSplitOnFirstWorkspace can recreate the layout on the next tick.
func verifyLayoutIntegrity(mgr *workspace.Manager) {
	if mgr.State.MainWindowID == "" {
		return
	}
	changed := false

	// Collapse back to zero state when all workspaces are gone.
	if len(mgr.State.Workspaces) == 0 && mgr.State.WorkspacePaneID != "" {
		_ = mgr.Tmux.KillPane(mgr.State.WorkspacePaneID)
		mgr.State.WorkspacePaneID = ""
		mgr.State.ActiveWorkspaceID = ""
		_ = mgr.SaveState()
		return
	}

	// Verify WorkspacePaneID is alive and in the main window.
	if mgr.State.WorkspacePaneID != "" {
		panes, err := mgr.Tmux.GetWindowPanes(mgr.State.MainWindowID)
		if err == nil && !slices.Contains(panes, mgr.State.WorkspacePaneID) {
			mgr.State.WorkspacePaneID = ""
			changed = true
		}
	}

	// Verify active workspace's pane is still alive.
	if mgr.State.ActiveWorkspaceID != "" {
		if ws, ok := mgr.State.Workspaces[mgr.State.ActiveWorkspaceID]; ok {
			if ws.PaneID != "" && !mgr.Tmux.PaneExists(ws.PaneID) {
				ws.PaneID = ""
				mgr.State.ActiveWorkspaceID = ""
				changed = true
			}
		} else {
			mgr.State.ActiveWorkspaceID = ""
			changed = true
		}
	}

	if changed {
		_ = mgr.SaveState()
	}
}

// ensureSplitOnFirstWorkspace creates the right-pane split when workspaces
// exist but no split has been created yet. Called from the sidebar's tick
// handler as a safety net for the popup-initiated create flow.
func ensureSplitOnFirstWorkspace(mgr *workspace.Manager) {
	if mgr.State.WorkspacePaneID != "" || len(mgr.State.Workspaces) == 0 || mgr.State.MainWindowID == "" {
		return
	}

	panes, err := mgr.Tmux.GetWindowPanes(mgr.State.MainWindowID)
	if err != nil || len(panes) == 0 {
		return
	}

	ensureRightPane(mgr, mgr.State.MainWindowID, panes)
	if mgr.State.WorkspacePaneID != "" {
		// Resize the left pane to sidebar width.
		if tw, twErr := mgr.Tmux.WindowWidth(mgr.State.MainWindowID); twErr == nil {
			_ = mgr.Tmux.ResizePane(panes[0], mgr.SidebarColumns(tw))
		}
		protectWorkspacePane(mgr, mgr.State.WorkspacePaneID)
		persistLayoutEnv(mgr, panes[0], mgr.State.WorkspacePaneID, mgr.State.MainWindowID)
		_ = mgr.SaveState()
	}
}

// finalizeLayout applies common layout configuration: status bar, pane
// protection, keybindings, and the custom status bar.
func finalizeLayout(mgr *workspace.Manager, rightPaneID string) {
	_ = mgr.Tmux.SetOption("status", "on")
	_ = mgr.Tmux.SetOption("status-position", "top")
	if rightPaneID != "" {
		protectWorkspacePane(mgr, rightPaneID)
	}
	installKeybindings(mgr)
	applyStatusBar(mgr)
}

// installKeybindings sets up session-scoped key bindings for pane navigation.
func installKeybindings(mgr *workspace.Manager) {
	// C-Space: toggle last pane (quick switch between nav and workspace).
	_ = mgr.Tmux.BindKey("C-Space", "last-pane")
}

// applyStatusBar configures the tmux status bar with project and workspace info.
func applyStatusBar(mgr *workspace.Manager) {
	wsCount := len(mgr.State.Workspaces)

	activeName := ""
	if mgr.State.ActiveWorkspaceID != "" {
		if ws, ok := mgr.State.Workspaces[mgr.State.ActiveWorkspaceID]; ok {
			activeName = ws.DisplayName()
		}
	}

	left := fmt.Sprintf(" agency · %s ", mgr.ProjectName)

	right := fmt.Sprintf(" %d workspace(s) ", wsCount)
	if activeName != "" {
		right = fmt.Sprintf(" %s · %d workspace(s) ", activeName, wsCount)
	}

	_ = mgr.Tmux.SetOption("status-style", "bg=default,fg=default")
	_ = mgr.Tmux.SetOption("status-left-length", "60")
	_ = mgr.Tmux.SetOption("status-right-length", "60")
	_ = mgr.Tmux.SetOption("status-left", left)
	_ = mgr.Tmux.SetOption("status-right", right)
}

// doQuitCleanup stops the project sandbox and cleans up finished worktrees.
func doQuitCleanup(mgr *workspace.Manager, infos []workspace.QuitInfo) {
	slog.Info("quit cleanup starting", "workspaces", len(infos))
	ctx := context.Background()

	// Stop the shared project sandbox once (not per-workspace).
	if mgr.Sandbox != nil {
		_ = mgr.StopProjectSandbox(ctx)
	}

	for _, info := range infos {
		slog.Info("quit cleanup workspace", "workspace", info.WS.ID, "active", info.IsActive, "dirty", info.IsDirty)
		// info.WS points into mgr.State.Workspaces (via List()), so
		// mutating it here updates the authoritative state before SaveState.
		if info.IsActive {
			info.WS.State = state.StatePaused
			info.WS.UpdatedAt = time.Now().UTC()
		}

		if !info.IsDirty {
			// CLEAN: remove worktree, kill tmux window, purge state.
			_ = mgr.CleanupDoneWorkspace(ctx, info.WS)
		}
	}
	_ = mgr.SaveState()
}
