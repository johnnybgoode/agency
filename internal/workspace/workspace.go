// Package workspace manages agent workspaces with worktrees and containers.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/sandbox"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/templates"
	"github.com/johnnybgoode/agency/internal/tmux"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// Manager coordinates workspace lifecycle: worktree creation, sandbox
// provisioning, tmux window management, and state persistence.
type Manager struct {
	StatePath   string
	ProjectDir  string
	ProjectName string
	State       *state.State
	Tmux        *tmux.Client
	Sandbox     *sandbox.Manager
	Cfg         *config.Config
}

// NewManager constructs a Manager for the given project directory. It loads or
// initializes the state file, creates a tmux client, and optionally connects to
// the Docker daemon. A nil Sandbox is not fatal — the TUI can still list
// existing workspaces without Docker.
func NewManager(projectDir string, cfg *config.Config) (*Manager, error) {
	projectName := filepath.Base(projectDir)
	statePath := filepath.Join(projectDir, ".agency", "state.json")

	s, err := state.Read(statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("loading state: %w", err)
		}
		s = state.Default(projectName, filepath.Join(projectDir, ".bare"))
		// Persist immediately so subsequent reads don't re-default.
		_ = state.Write(statePath, s)
	}

	tc := tmux.New("agency-" + projectName)

	var sbm *sandbox.Manager
	if sm, err := sandbox.New(); err == nil {
		sbm = sm
	} else {
		slog.Warn("docker unavailable", "error", err)
	}

	slog.Info("workspace manager initialized", "project", projectName, "workspaces", len(s.Workspaces))
	return &Manager{
		StatePath:   statePath,
		ProjectDir:  projectDir,
		ProjectName: projectName,
		State:       s,
		Tmux:        tc,
		Sandbox:     sbm,
		Cfg:         cfg,
	}, nil
}

// generateID returns a random workspace ID with the "ws-" prefix.
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("workspace: rand.Read: %v", err))
	}
	return "ws-" + hex.EncodeToString(b)
}

// validateCreate checks that name and branch are non-empty and that no active
// workspace already uses the given branch.
func (m *Manager) validateCreate(name, branch string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("workspace name cannot be empty")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("branch name cannot be empty")
	}
	for _, existing := range m.State.Workspaces {
		if existing.Branch == branch && existing.State != state.StateDone && existing.State != state.StateFailed {
			return fmt.Errorf("branch %q already has an active workspace (%s)", branch, existing.ID)
		}
	}
	return nil
}

// provisionWorktree creates the git worktree for ws and updates the workspace
// fields accordingly.
func (m *Manager) provisionWorktree(ws *state.Workspace) error {
	slog.Debug("provisioning worktree", "workspace", ws.ID, "branch", ws.Branch)
	wtPath, err := worktree.Create(m.State.BarePath, m.ProjectName, ws.Branch)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	slog.Debug("worktree created", "workspace", ws.ID, "path", wtPath)
	ws.State = state.StateProvisioning
	ws.WorktreePath = wtPath
	ws.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fmt.Errorf("saving provisioning state: %w", err)
	}
	return nil
}

// provisionContainer creates and starts the Docker container for ws.
func (m *Manager) provisionContainer(ctx context.Context, ws *state.Workspace, cfg *config.Config) error {
	slog.Debug("provisioning container", "workspace", ws.ID)
	if m.Sandbox == nil {
		return errors.New("docker is not available")
	}

	// Build environment variables.
	var env []string
	if key := cfg.Credentials.AnthropicAPIKey; key != "" {
		env = append(env, "ANTHROPIC_API_KEY="+key)
	}
	if tok := cfg.Credentials.GithubToken; tok != "" {
		env = append(env, "GITHUB_TOKEN="+tok)
	}
	if v := os.Getenv("GIT_USER"); v != "" {
		env = append(env, "GIT_USER="+v)
	}
	if v := os.Getenv("GIT_EMAIL"); v != "" {
		env = append(env, "GIT_EMAIL="+v)
	}

	// Determine whether the project config file should be mounted.
	configMount := ""
	cfgPath := config.ProjectConfigPath(m.ProjectDir)
	if _, err := os.Stat(cfgPath); err == nil {
		configMount = cfgPath
	}

	sharedHome := ""
	if candidate := filepath.Join(m.ProjectDir, ".agency", "home"); func() bool {
		info, err := os.Stat(candidate)
		return err == nil && info.IsDir()
	}() {
		sharedHome = candidate
	}

	if err := m.Sandbox.EnsureImage(ctx, cfg.Sandbox.Image, dockerBuildContext(cfg.Sandbox.DockerfileDir)); err != nil {
		return fmt.Errorf("ensuring sandbox image: %w", err)
	}

	containerID, err := m.Sandbox.Create(ctx, &sandbox.CreateOpts{
		Image:           cfg.Sandbox.Image,
		Name:            "agency-sb-" + m.ProjectName + "-" + worktree.Slugify(ws.Branch) + "-" + ws.ID,
		WorktreeMount:   ws.WorktreePath,
		ConfigMount:     configMount,
		SharedHomeMount: sharedHome,
		Env:             env,
	})
	if err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	ws.SandboxID = containerID
	slog.Debug("container created", "workspace", ws.ID, "container", containerID)

	if err := m.Sandbox.Start(ctx, containerID); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}
	return nil
}

// provisionTmux opens a new tmux window for ws, captures the pane ID, and
// launches the agent inside the container.
func (m *Manager) provisionTmux(ws *state.Workspace) error {
	slog.Debug("provisioning tmux window", "workspace", ws.ID, "name", ws.Name)
	windowID, err := m.Tmux.NewWindow(ws.Name)
	if err != nil {
		return fmt.Errorf("creating tmux window: %w", err)
	}
	ws.TmuxWindow = windowID

	// Capture pane ID for the new window.
	if panes, err := m.Tmux.GetWindowPanes(windowID); err == nil && len(panes) > 0 {
		ws.PaneID = panes[0]
	}

	agencyBin, _ := os.Executable()
	// The wrapper bash:
	//  - EXIT trap: runs gc for cleanup when the window is killed (sidebar 'd')
	//  - trap '' INT: ignores SIGINT so ctrl-c passes through the TTY to Claude
	//    inside the container (for cancellation) without killing the wrapper
	//  - while loop condition: checks container existence before each exec so
	//    the loop exits cleanly when Remove() deletes the container, preventing
	//    "No such container" errors from flooding the workspace pane
	trapCmd := fmt.Sprintf(
		`bash -c 'trap "cd %q && %s gc --workspace-id %s" EXIT; trap "" INT; RESUME=""; while docker container inspect %s >/dev/null 2>&1; do docker exec -it %s bash -c "claude $RESUME" || true; RESUME="--continue"; sleep 1; done'`,
		m.ProjectDir, agencyBin, ws.ID, ws.SandboxID, ws.SandboxID,
	)
	if err := m.Tmux.SendKeys(windowID, trapCmd); err != nil {
		return fmt.Errorf("sending keys to tmux window: %w", err)
	}
	return nil
}

// Create provisions a full workspace for the given name and branch: worktree → sandbox →
// tmux window. On any error after the initial state entry is written the
// workspace is marked as failed before returning the error.
func (m *Manager) Create(ctx context.Context, name, branch string) (*state.Workspace, error) {
	slog.Info("creating workspace", "name", name, "branch", branch)
	if err := m.validateCreate(name, branch); err != nil {
		return nil, err
	}

	id := generateID()
	now := time.Now().UTC()
	ws := &state.Workspace{
		ID:        id,
		Name:      name,
		State:     state.StateCreating,
		Branch:    branch,
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.State.Workspaces[id] = ws
	if err := m.SaveState(); err != nil {
		return nil, fmt.Errorf("saving initial state: %w", err)
	}

	fail := func(err error) (*state.Workspace, error) {
		slog.Error("workspace creation failed", "workspace", id, "step", string(ws.State), "error", err)
		msg := err.Error()
		fromState := string(ws.State)
		ws.FailedFrom = &fromState
		ws.State = state.StateFailed
		ws.Error = &msg
		ws.UpdatedAt = time.Now().UTC()
		_ = m.SaveState()
		return ws, err
	}

	// Step 1: create git worktree.
	if err := m.provisionWorktree(ws); err != nil {
		return fail(err)
	}

	// Step 2: load workspace-local config overlay if present.
	cfg := m.Cfg
	if localCfg, err := config.Load(config.WorkspaceConfigPath(ws.WorktreePath)); err == nil {
		cfg = config.Merge(m.Cfg, localCfg)
	}

	// Step 3: create and start container.
	if err := m.provisionContainer(ctx, ws, cfg); err != nil {
		return fail(err)
	}

	// Step 4: open tmux window and launch agent.
	if err := m.provisionTmux(ws); err != nil {
		return fail(err)
	}

	// Step 5: swap new workspace pane into the visible right slot, then mark running.
	_ = m.SwapActivePane(ws.ID)
	ws.State = state.StateRunning
	ws.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fail(fmt.Errorf("saving running state: %w", err))
	}

	// Step 6: focus the main window so sidebar + workspace are both visible.
	if m.State.MainWindowID != "" {
		_ = m.Tmux.SelectWindow(m.State.MainWindowID)
	} else if ws.TmuxWindow != "" {
		_ = m.Tmux.SelectWindow(ws.TmuxWindow)
	}

	slog.Info("workspace created successfully", "workspace", ws.ID, "name", ws.Name, "branch", ws.Branch)
	return ws, nil
}

// Remove tears down a workspace: stops/removes its container, deletes the
// worktree, kills the tmux window, and removes the workspace from state.
func (m *Manager) Remove(ctx context.Context, workspaceID string) error {
	slog.Info("removing workspace", "workspace", workspaceID)
	ws, ok := m.State.Workspaces[workspaceID]
	if !ok {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	// Interrupt the foreground process. Target by pane ID when available so
	// we don't accidentally send keys to the sidebar pane.
	if ws.PaneID != "" {
		_ = m.Tmux.SendKeysToPane(ws.PaneID, "C-c")
	} else if ws.TmuxWindow != "" {
		_ = m.Tmux.SendKeys(ws.TmuxWindow, "C-c")
	}

	// Stop and remove the sandbox container.
	if ws.SandboxID != "" && m.Sandbox != nil {
		_ = m.Sandbox.Stop(ctx, ws.SandboxID, 5)
		_ = m.Sandbox.Remove(ctx, ws.SandboxID)
	}

	// Remove the git worktree.
	if ws.WorktreePath != "" {
		_ = worktree.Remove(m.State.BarePath, ws.WorktreePath)
	}

	// If this workspace's pane is currently visible in the right slot of
	// the main window, swap it back to its own window first so the shell
	// pane is restored in :0.1.
	if m.State.ActiveWorkspaceID == workspaceID && ws.PaneID != "" && m.State.WorkspacePaneID != "" {
		_ = m.Tmux.SwapPane(ws.PaneID, m.State.WorkspacePaneID)
	}

	// Clear active/last-active pointers if they referred to the removed workspace.
	if m.State.ActiveWorkspaceID == workspaceID {
		m.State.ActiveWorkspaceID = ""
	}
	if m.State.LastActiveWorkspaceID == workspaceID {
		m.State.LastActiveWorkspaceID = ""
	}

	// Re-read state from disk before saving to pick up any concurrent
	// changes (e.g., a popup process creating a new workspace). Then apply
	// our deletion to the fresh state so we don't overwrite those changes.
	tmuxWindow := ws.TmuxWindow
	if freshState, readErr := state.Read(m.StatePath); readErr == nil {
		// Preserve our pointer cleanups on the fresh state.
		if freshState.ActiveWorkspaceID == workspaceID {
			freshState.ActiveWorkspaceID = ""
		}
		if freshState.LastActiveWorkspaceID == workspaceID {
			freshState.LastActiveWorkspaceID = ""
		}
		delete(freshState.Workspaces, workspaceID)
		m.State = freshState
	} else {
		delete(m.State.Workspaces, workspaceID)
	}

	// Persist BEFORE killing the tmux window. When the window is killed,
	// the EXIT trap fires gc. gc reads state, finds the workspace already
	// removed, and exits cleanly — no race.
	if err := m.SaveState(); err != nil {
		return err
	}

	if tmuxWindow != "" {
		_ = m.Tmux.KillWindow(tmuxWindow)
	}
	slog.Info("workspace removed successfully", "workspace", workspaceID)
	return nil
}

// reconcileResult holds the three parallel query results used by Reconcile.
type reconcileResult struct {
	windows    []tmux.Window
	containers []sandbox.ContainerInfo
	worktrees  []worktree.Info
	windowsErr error
	contsErr   error
	wtErr      error
}

// gatherReconcileResources queries tmux, Docker, and git worktrees in parallel
// and returns the combined result.
func (m *Manager) gatherReconcileResources(ctx context.Context) reconcileResult {
	var res reconcileResult
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.windows, res.windowsErr = m.Tmux.ListWindows()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if m.Sandbox != nil {
			res.containers, res.contsErr = m.Sandbox.ListByProject(ctx, m.ContainerPrefix())
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.worktrees, res.wtErr = worktree.List(m.State.BarePath)
	}()

	wg.Wait()
	return res
}

// cleanupActiveWorkspaceID clears ActiveWorkspaceID and LastActiveWorkspaceID
// if they refer to workspaces that no longer exist or are not running.
// Also verifies that referenced panes actually exist in tmux.
// Returns true if a change was made.
func (m *Manager) cleanupActiveWorkspaceID() bool {
	changed := false
	if m.State.ActiveWorkspaceID != "" {
		activeWS, ok := m.State.Workspaces[m.State.ActiveWorkspaceID]
		if !ok || activeWS.State != state.StateRunning {
			m.State.ActiveWorkspaceID = ""
			changed = true
		} else if activeWS.PaneID != "" && !m.Tmux.PaneExists(activeWS.PaneID) {
			activeWS.PaneID = ""
			m.State.ActiveWorkspaceID = ""
			changed = true
		}
	}
	if m.State.LastActiveWorkspaceID != "" {
		lastWS, ok := m.State.Workspaces[m.State.LastActiveWorkspaceID]
		if !ok || lastWS.State != state.StateRunning {
			m.State.LastActiveWorkspaceID = ""
			changed = true
		}
	}
	// Verify WorkspacePaneID (shell pane) is still alive.
	if m.State.WorkspacePaneID != "" && !m.Tmux.PaneExists(m.State.WorkspacePaneID) {
		m.State.WorkspacePaneID = ""
		changed = true
	}
	return changed
}

// buildLookupSets constructs boolean lookup maps for windows, container IDs, and
// worktree paths from the reconcile result. Maps are only populated when the
// corresponding query succeeded to avoid destructive changes on partial data.
func buildLookupSets(res *reconcileResult) (windowSet, containerIDSet, worktreeSet map[string]bool) {
	windowSet = make(map[string]bool, len(res.windows))
	if res.windowsErr == nil {
		for _, w := range res.windows {
			windowSet[w.ID] = true
		}
	}

	containerIDSet = make(map[string]bool, len(res.containers))
	if res.contsErr == nil {
		for _, c := range res.containers {
			containerIDSet[c.ID] = true
		}
	}

	worktreeSet = make(map[string]bool, len(res.worktrees))
	if res.wtErr == nil {
		for _, wt := range res.worktrees {
			worktreeSet[wt.Path] = true
		}
	}
	return windowSet, containerIDSet, worktreeSet
}

// Reconcile queries tmux, Docker, and git worktrees in parallel then corrects
// workspace states that have drifted from reality.
func (m *Manager) Reconcile(ctx context.Context) error {
	res := m.gatherReconcileResources(ctx)
	slog.Debug("reconcile resources gathered", "windows", len(res.windows), "containers", len(res.containers), "worktrees", len(res.worktrees))

	windowSet, containerIDSet, worktreeSet := buildLookupSets(&res)
	toDelete, changed := m.reconcileWorkspaces(&res, windowSet, containerIDSet, worktreeSet)

	for _, id := range toDelete {
		delete(m.State.Workspaces, id)
	}

	if m.cleanupActiveWorkspaceID() {
		changed = true
	}

	if changed {
		if err := m.SaveState(); err != nil {
			return err
		}
	}

	return nil
}

// reconcileOneWorkspace handles the reconcile logic for a single workspace.
// Returns (shouldDelete bool, changed bool).
func (m *Manager) reconcileOneWorkspace(
	ws *state.Workspace,
	res *reconcileResult,
	windowSet, containerIDSet, worktreeSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (shouldDelete, changed bool) {
	switch ws.State {
	case state.StateRunning:
		return m.reconcileRunning(ws, res, windowSet, containerIDSet, markFailed)

	case state.StateProvisioning:
		return false, m.reconcileProvisioning(ws, res, containerIDSet, markFailed)

	case state.StateCreating:
		if ws.WorktreePath == "" {
			markFailed(ws, "workspace stuck in creating state")
			return false, true
		}

	case state.StateCompleting:
		if res.contsErr == nil && (ws.SandboxID == "" || !containerIDSet[ws.SandboxID]) {
			ws.State = state.StateDone
			ws.UpdatedAt = time.Now().UTC()
			return false, true
		}

	case state.StatePaused:
		if res.wtErr == nil && ws.WorktreePath != "" && !worktreeSet[ws.WorktreePath] {
			markFailed(ws, "worktree disappeared while paused")
			return false, true
		}

	case state.StateDone:
		if res.wtErr == nil && ws.WorktreePath != "" && !worktreeSet[ws.WorktreePath] {
			return true, true
		}
	}

	return false, false
}

// reconcileRunning handles reconcile logic for a workspace in StateRunning.
func (m *Manager) reconcileRunning(
	ws *state.Workspace,
	res *reconcileResult,
	windowSet, containerIDSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (shouldDelete, changed bool) {
	if res.contsErr == nil && ws.SandboxID != "" && !containerIDSet[ws.SandboxID] {
		markFailed(ws, "sandbox disappeared")
		return false, true
	}
	if res.windowsErr == nil && ws.TmuxWindow != "" && !windowSet[ws.TmuxWindow] {
		slog.Warn("tmux window disappeared, recreating", "workspace", ws.ID, "old_window", ws.TmuxWindow)
		if newWin, err := m.Tmux.NewWindow(worktree.Slugify(ws.Branch)); err == nil {
			_ = m.Tmux.SendKeys(newWin, fmt.Sprintf("docker exec -it %s bash", ws.SandboxID))
			ws.TmuxWindow = newWin
			// Capture new pane ID.
			if panes, err := m.Tmux.GetWindowPanes(newWin); err == nil && len(panes) > 0 {
				ws.PaneID = panes[0]
			}
			ws.UpdatedAt = time.Now().UTC()
			return false, true
		}
	} else if res.windowsErr == nil && ws.TmuxWindow != "" && windowSet[ws.TmuxWindow] && ws.PaneID != "" {
		// Verify pane still exists in the window; if not, re-capture.
		if panes, err := m.Tmux.GetWindowPanes(ws.TmuxWindow); err == nil {
			paneFound := false
			for _, p := range panes {
				if p == ws.PaneID {
					paneFound = true
					break
				}
			}
			if !paneFound && len(panes) > 0 {
				ws.PaneID = panes[0]
				ws.UpdatedAt = time.Now().UTC()
				return false, true
			}
		}
	}
	return false, false
}

// reconcileProvisioning handles reconcile logic for a workspace in StateProvisioning.
func (m *Manager) reconcileProvisioning(
	ws *state.Workspace,
	res *reconcileResult,
	containerIDSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (changed bool) {
	if res.contsErr == nil && ws.SandboxID != "" {
		if containerIDSet[ws.SandboxID] {
			ws.State = state.StateRunning
			ws.UpdatedAt = time.Now().UTC()
			return true
		}
		markFailed(ws, "sandbox not found during reconciliation")
		return true
	} else if res.contsErr == nil {
		markFailed(ws, "sandbox not found during reconciliation")
		return true
	}
	return false
}

// reconcileWorkspaces processes all workspaces and handles state transitions based
// on the current tmux, docker, and worktree state. Returns workspaces to delete
// and whether any changes were made.
func (m *Manager) reconcileWorkspaces(res *reconcileResult, windowSet, containerIDSet, worktreeSet map[string]bool) ([]string, bool) {
	markFailed := func(ws *state.Workspace, reason string) {
		fromState := string(ws.State)
		ws.FailedFrom = &fromState
		ws.State = state.StateFailed
		ws.Error = &reason
		ws.UpdatedAt = time.Now().UTC()
	}

	changed := false
	var toDelete []string

	for id, ws := range m.State.Workspaces {
		del, chg := m.reconcileOneWorkspace(ws, res, windowSet, containerIDSet, worktreeSet, markFailed)
		if chg {
			changed = true
		}
		if del {
			toDelete = append(toDelete, id)
		}
	}

	return toDelete, changed
}

// ContainerPrefix returns the Docker container name prefix used to scope
// sandbox operations to this project. It ends with "-" so that Docker's
// substring --filter does not accidentally match containers belonging to a
// project whose name starts with the same characters.
func (m *Manager) ContainerPrefix() string {
	return "agency-sb-" + m.ProjectName + "-"
}

// SidebarWidthPercent returns the configured sidebar width percentage.
func (m *Manager) SidebarWidthPercent() int {
	pct := m.Cfg.TUI.SidebarWidth
	if pct <= 0 {
		pct = config.DefaultSidebarWidth
	}
	return pct
}

// DefaultWorkspaceSplitPercent is the percentage of window width given to the
// workspace pane when splitting the main window horizontally.
const DefaultWorkspaceSplitPercent = 68

// MinSidebarColumns is the minimum sidebar width in columns.
const MinSidebarColumns = 25

// SidebarColumns computes the sidebar width in columns for the given terminal
// width, applying the configured percentage and enforcing a minimum of 25 columns.
func (m *Manager) SidebarColumns(termWidth int) int {
	pct := m.SidebarWidthPercent()
	cols := termWidth * pct / 100
	if cols < MinSidebarColumns {
		cols = MinSidebarColumns
	}
	return cols
}

// resizeSidebarPane resizes the left (sidebar) pane of the main window to the
// percentage-based sidebar width.
func (m *Manager) resizeSidebarPane() {
	if m.State.MainWindowID == "" {
		return
	}
	panes, err := m.Tmux.GetWindowPanes(m.State.MainWindowID)
	if err != nil || len(panes) == 0 {
		return
	}
	tw, err := m.Tmux.WindowWidth(m.State.MainWindowID)
	if err != nil {
		return
	}
	_ = m.Tmux.ResizePane(panes[0], m.SidebarColumns(tw))
}

// SwapActivePane swaps the given workspace's pane into the visible right slot
// of the main window (:0.1) using tmux swap-pane. If another workspace is
// currently active, its pane is swapped back to its own window first.
// No-op when ws.PaneID is empty. If WorkspacePaneID is empty (first workspace,
// zero-state → split), the right pane is created on demand.
func (m *Manager) SwapActivePane(wsID string) error {
	slog.Debug("swapping active pane", "workspace", wsID)
	ws, ok := m.State.Workspaces[wsID]
	if !ok || ws.PaneID == "" {
		return nil
	}

	// Create the right-side split on demand if it doesn't exist yet
	// (first workspace in zero state). Re-check the actual pane count first:
	// the sidebar's ensureSplitOnFirstWorkspace may have already created the
	// split concurrently, in which case we reuse the existing right pane
	// rather than creating a second split.
	if m.State.WorkspacePaneID == "" && m.State.MainWindowID != "" {
		existingPanes, pErr := m.Tmux.GetWindowPanes(m.State.MainWindowID)
		if pErr == nil && len(existingPanes) >= 2 {
			// Split already exists — adopt the right pane.
			m.State.WorkspacePaneID = existingPanes[1]
		} else {
			rightPaneID, err := m.Tmux.SplitWindowHorizontalPercent(m.State.MainWindowID, DefaultWorkspaceSplitPercent)
			if err != nil {
				return fmt.Errorf("creating workspace pane split: %w", err)
			}
			m.State.WorkspacePaneID = rightPaneID
		}
		// Resize the left pane to the sidebar width now that we have 2 panes.
		m.resizeSidebarPane()
		_ = m.SaveState()
	}

	if m.State.WorkspacePaneID == "" {
		return nil
	}

	// Verify WorkspacePaneID (shell pane) exists.
	if !m.Tmux.PaneExists(m.State.WorkspacePaneID) {
		m.State.WorkspacePaneID = ""
		_ = m.SaveState()
		return fmt.Errorf("workspace shell pane is dead; layout will be recreated")
	}

	// Verify the target workspace's pane exists.
	if !m.Tmux.PaneExists(ws.PaneID) {
		ws.PaneID = ""
		_ = m.SaveState()
		return fmt.Errorf("workspace %s pane is dead", wsID)
	}

	// If another workspace is currently visible, swap it back first.
	if m.State.ActiveWorkspaceID != "" && m.State.ActiveWorkspaceID != wsID {
		m.State.LastActiveWorkspaceID = m.State.ActiveWorkspaceID
		if activeWS, ok2 := m.State.Workspaces[m.State.ActiveWorkspaceID]; ok2 && activeWS.PaneID != "" {
			// Verify the active workspace's pane is still alive before swapping back.
			if m.Tmux.PaneExists(activeWS.PaneID) {
				if err := m.Tmux.SwapPane(activeWS.PaneID, m.State.WorkspacePaneID); err != nil {
					return err
				}
			} else {
				// Active pane is dead — just clear the reference.
				activeWS.PaneID = ""
				m.State.ActiveWorkspaceID = ""
			}
		}
	}

	// Swap workspace pane into :0.1 (shell goes to workspace's window).
	if err := m.Tmux.SwapPane(m.State.WorkspacePaneID, ws.PaneID); err != nil {
		return err
	}
	m.State.ActiveWorkspaceID = wsID
	return m.SaveState()
}

// SwapBackToShell swaps the active workspace pane back to its own window,
// restoring the shell pane to :0.1. No-op when no workspace is active.
func (m *Manager) SwapBackToShell() error {
	if m.State.ActiveWorkspaceID == "" || m.State.WorkspacePaneID == "" {
		return nil
	}
	ws, ok := m.State.Workspaces[m.State.ActiveWorkspaceID]
	if !ok || ws.PaneID == "" {
		m.State.ActiveWorkspaceID = ""
		return m.SaveState()
	}

	// Verify both panes exist before swapping.
	if !m.Tmux.PaneExists(ws.PaneID) {
		ws.PaneID = ""
		m.State.ActiveWorkspaceID = ""
		return m.SaveState()
	}
	if !m.Tmux.PaneExists(m.State.WorkspacePaneID) {
		m.State.WorkspacePaneID = ""
		m.State.ActiveWorkspaceID = ""
		return m.SaveState()
	}

	// ws.PaneID is currently in :0.1; WorkspacePaneID (shell) is in ws's window.
	// Swap: ws pane goes back to its window, shell comes to :0.1.
	if err := m.Tmux.SwapPane(ws.PaneID, m.State.WorkspacePaneID); err != nil {
		return err
	}
	m.State.ActiveWorkspaceID = ""
	return m.SaveState()
}

// SwitchToLastActive switches the active pane to the last active workspace.
// Returns true if the switch was performed, false if there was no suitable
// last-active workspace (e.g. it was removed or is not in a running state).
func (m *Manager) SwitchToLastActive() bool {
	id := m.State.LastActiveWorkspaceID
	if id == "" {
		return false
	}
	ws, ok := m.State.Workspaces[id]
	if !ok || ws.PaneID == "" || ws.State != state.StateRunning {
		return false
	}
	return m.SwapActivePane(id) == nil
}

// FindOrphanWorktrees returns worktrees registered in the project's bare repo
// that are not tracked in state. The initial development worktree
// (<projectDir>/<projectName>-main) is always excluded.
func (m *Manager) FindOrphanWorktrees() ([]worktree.Info, error) {
	mainWorktreePath := filepath.Join(m.ProjectDir, m.ProjectName+"-main")

	known := make(map[string]bool, len(m.State.Workspaces))
	for _, ws := range m.State.Workspaces {
		if ws.WorktreePath != "" {
			known[ws.WorktreePath] = true
		}
	}

	all, err := worktree.List(m.State.BarePath)
	if err != nil {
		return nil, err
	}

	var orphans []worktree.Info
	for _, wt := range all {
		if wt.Path == mainWorktreePath {
			continue
		}
		if !known[wt.Path] {
			orphans = append(orphans, wt)
		}
	}
	return orphans, nil
}

// List returns all workspaces sorted by creation time (oldest first).
func (m *Manager) List() []*state.Workspace {
	workspaces := make([]*state.Workspace, 0, len(m.State.Workspaces))
	for _, ws := range m.State.Workspaces {
		workspaces = append(workspaces, ws)
	}
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].CreatedAt.Before(workspaces[j].CreatedAt)
	})
	return workspaces
}

// dockerBuildContext returns the fs.FS to use as the Docker build context when
// building the sandbox image. The embedded build context (Dockerfile +
// entrypoint.sh compiled into the binary) is the default, so the image
// can be built on any machine with Docker — no local copy of the agency source
// is required. An on-disk override is honored when dockerfile_dir is set in
// config, allowing custom images.
func dockerBuildContext(configuredDir string) fs.FS {
	if configuredDir != "" {
		if _, err := os.Stat(filepath.Join(configuredDir, "Dockerfile")); err == nil {
			slog.Debug("using configured dockerfile_dir for image build", "dir", configuredDir)
			return os.DirFS(configuredDir)
		}
		slog.Warn("configured dockerfile_dir has no Dockerfile, falling back to embedded context", "dir", configuredDir)
	}
	sub, err := templates.Sub("docker")
	if err != nil {
		panic("embedded docker template missing: " + err.Error())
	}
	return sub
}

// SaveState persists current state to disk, updating the PID field first.
func (m *Manager) SaveState() error {
	m.State.PID = os.Getpid()
	if err := state.Write(m.StatePath, m.State); err != nil {
		slog.Error("failed to save state", "path", m.StatePath, "error", err)
		return err
	}
	return nil
}

// QuitInfo holds quit assessment data for a single workspace.
type QuitInfo struct {
	WS       *state.Workspace
	IsActive bool
	IsDirty  bool
}

// isActiveState reports whether a workspace state is considered "active" for
// quit purposes (i.e. has a running or provisioning container that needs stopping).
func isActiveState(s state.WorkspaceState) bool {
	switch s {
	case state.StateCreating, state.StateProvisioning, state.StateRunning, state.StatePaused:
		return true
	}
	return false
}

// AssessQuitStatuses returns quit info for all workspaces (git status checked in parallel).
func (m *Manager) AssessQuitStatuses(ctx context.Context) ([]QuitInfo, error) {
	workspaces := m.List()
	infos := make([]QuitInfo, len(workspaces))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, ws := range workspaces {
		wg.Add(1)
		go func(i int, ws *state.Workspace) {
			defer wg.Done()
			info := QuitInfo{
				WS:       ws,
				IsActive: isActiveState(ws.State),
			}
			if ws.WorktreePath != "" {
				dirty, err := worktree.IsDirty(ws.WorktreePath)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					dirty = true // assume dirty on error (safe default)
				}
				info.IsDirty = dirty
			} else {
				info.IsDirty = true // no worktree = treat as dirty
			}
			infos[i] = info
		}(i, ws)
	}
	wg.Wait()
	return infos, firstErr
}

// StopWorkspace stops the sandbox container and transitions ws to StatePaused.
// No-op if no container is running. Does not remove the worktree or state.
func (m *Manager) StopWorkspace(ctx context.Context, ws *state.Workspace) error {
	slog.Info("stopping workspace", "workspace", ws.ID)
	if ws.SandboxID != "" && m.Sandbox != nil {
		_ = m.Sandbox.Stop(ctx, ws.SandboxID, 10)
	}
	ws.State = state.StatePaused
	ws.UpdatedAt = time.Now().UTC()
	return m.SaveState()
}

// StopWorkspaceBackground fires a non-blocking docker stop and immediately
// transitions ws to StatePaused. The docker daemon handles the actual shutdown
// independently; the agency process need not wait for it.
func (m *Manager) StopWorkspaceBackground(ctx context.Context, ws *state.Workspace) error {
	if ws.SandboxID != "" && m.Sandbox != nil {
		_ = m.Sandbox.StopBackground(ctx, ws.SandboxID, 10)
	}
	ws.State = state.StatePaused
	ws.UpdatedAt = time.Now().UTC()
	return m.SaveState()
}

// CleanupDoneWorkspace removes the worktree, kills the tmux window, and purges
// the workspace from state. Only call for INACTIVE+CLEAN workspaces.
func (m *Manager) CleanupDoneWorkspace(ctx context.Context, ws *state.Workspace) error {
	slog.Info("cleaning up done workspace", "workspace", ws.ID)
	if ws.WorktreePath != "" {
		_ = worktree.Remove(m.State.BarePath, ws.WorktreePath)
	}
	if ws.TmuxWindow != "" {
		_ = m.Tmux.KillWindow(ws.TmuxWindow)
	}
	delete(m.State.Workspaces, ws.ID)
	return m.SaveState()
}
