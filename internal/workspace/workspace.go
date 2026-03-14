// Package workspace manages agent workspaces with worktrees and containers.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/sandbox"
	"github.com/johnnybgoode/agency/internal/state"
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
	}
	// Docker unavailable is non-fatal; sbm stays nil.

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
		return fmt.Errorf("workspace name cannot be empty")
	}
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("branch name cannot be empty")
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
	wtPath, err := worktree.Create(m.State.BarePath, m.ProjectName, ws.Branch)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
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
	if m.Sandbox == nil {
		return fmt.Errorf("docker is not available")
	}

	// Build environment variables.
	env := []string{}
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

	containerID, err := m.Sandbox.Create(ctx, &sandbox.CreateOpts{
		Image:         cfg.Sandbox.Image,
		Name:          "claude-sb-" + m.ProjectName + "-" + worktree.Slugify(ws.Branch) + "-" + ws.ID,
		WorktreeMount: ws.WorktreePath,
		ConfigMount:   configMount,
		Env:           env,
	})
	if err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	ws.SandboxID = containerID

	if err := m.Sandbox.Start(ctx, containerID); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}
	return nil
}

// provisionTmux opens a new tmux window for ws, captures the pane ID, and
// launches the agent inside the container.
func (m *Manager) provisionTmux(ws *state.Workspace) error {
	windowID, err := m.Tmux.NewWindow(worktree.Slugify(ws.Branch))
	if err != nil {
		return fmt.Errorf("creating tmux window: %w", err)
	}
	ws.TmuxWindow = windowID

	// Capture pane ID for the new window.
	if panes, err := m.Tmux.GetWindowPanes(windowID); err == nil && len(panes) > 0 {
		ws.PaneID = panes[0]
	}

	if err := m.Tmux.SendKeys(windowID, fmt.Sprintf("docker exec -it %s bash -c claude", ws.SandboxID)); err != nil {
		return fmt.Errorf("sending keys to tmux window: %w", err)
	}
	return nil
}

// Create provisions a full workspace for the given name and branch: worktree → sandbox →
// tmux window. On any error after the initial state entry is written the
// workspace is marked as failed before returning the error.
func (m *Manager) Create(ctx context.Context, name, branch string) (*state.Workspace, error) {
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

	// Step 5: join new pane into main window so sidebar stays visible, then mark running.
	m.SwitchActivePane(ws)
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

	return ws, nil
}

// Remove tears down a workspace: stops/removes its container, deletes the
// worktree, kills the tmux window, and removes the workspace from state.
func (m *Manager) Remove(ctx context.Context, workspaceID string) error {
	ws, ok := m.State.Workspaces[workspaceID]
	if !ok {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	// Interrupt the foreground process so it can clean up.
	if ws.TmuxWindow != "" {
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

	// Kill the tmux window.
	if ws.TmuxWindow != "" {
		_ = m.Tmux.KillWindow(ws.TmuxWindow)
	}

	delete(m.State.Workspaces, workspaceID)
	return m.SaveState()
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

// cleanupActiveWorkspaceID clears ActiveWorkspaceID if it refers to a workspace
// that no longer exists or is not running. Returns true if a change was made.
func (m *Manager) cleanupActiveWorkspaceID() bool {
	if m.State.ActiveWorkspaceID == "" {
		return false
	}
	activeWS, ok := m.State.Workspaces[m.State.ActiveWorkspaceID]
	if !ok || activeWS.State != state.StateRunning {
		m.State.ActiveWorkspaceID = ""
		return true
	}
	return false
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

// logOrphans is a no-op placeholder retained for future structured logging.
func (m *Manager) logOrphans(_ *reconcileResult) {}

// Reconcile queries tmux, Docker, and git worktrees in parallel then corrects
// workspace states that have drifted from reality.
func (m *Manager) Reconcile(ctx context.Context) error {
	res := m.gatherReconcileResources(ctx)

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

	m.logOrphans(&res)
	return nil
}

// reconcileOneWorkspace handles the reconcile logic for a single workspace.
// Returns (shouldDelete bool, changed bool).
func (m *Manager) reconcileOneWorkspace(
	id string,
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

	_ = id
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
	toDelete := []string{}

	for id, ws := range m.State.Workspaces {
		del, chg := m.reconcileOneWorkspace(id, ws, res, windowSet, containerIDSet, worktreeSet, markFailed)
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
	return "claude-sb-" + m.ProjectName + "-"
}

// SwitchActivePane updates the main window so ws's pane occupies the right
// slot. If another workspace pane is currently joined, it is broken back to
// its own detached window first to maintain the single-right-pane invariant.
// No-op when MainWindowID or ws.PaneID is empty.
func (m *Manager) SwitchActivePane(ws *state.Workspace) {
	if m.State.MainWindowID == "" || ws.PaneID == "" {
		return
	}
	if m.State.ActiveWorkspaceID != "" {
		if activeWS, ok := m.State.Workspaces[m.State.ActiveWorkspaceID]; ok && activeWS.PaneID != "" {
			if newWinID, err := m.Tmux.BreakPane(m.State.MainWindowID, activeWS.PaneID); err == nil {
				activeWS.TmuxWindow = newWinID
			}
		}
	}
	_ = m.Tmux.JoinPane(ws.PaneID, m.State.MainWindowID)
	ws.TmuxWindow = m.State.MainWindowID
	m.State.ActiveWorkspaceID = ws.ID
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

// SaveState persists current state to disk, updating the PID field first.
func (m *Manager) SaveState() error {
	m.State.PID = os.Getpid()
	return state.Write(m.StatePath, m.State)
}
