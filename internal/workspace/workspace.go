// Package workspace manages agent workspaces with worktrees and sandboxes.
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
	"slices"
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

	confPath, _ := templates.WriteTmuxConf(filepath.Join(projectDir, ".agency"))
	tc := tmux.NewWithSocket("agency-"+projectName, "agency-"+projectName, confPath)

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

// generateSessionID generates a UUID v4 string using crypto/rand.
// Format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("workspace: rand.Read for session ID: %v", err))
	}
	// Set version 4
	b[6] = (b[6] & 0x0f) | 0x40
	// Set variant bits
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidateWorkspaceID returns an error if id does not match the workspace ID format.
func ValidateWorkspaceID(id string) error {
	return state.ValidateWorkspaceID(id)
}

// SandboxName returns the Docker sandbox name used for this project.
// All workspaces in the project share a single sandbox.
func (m *Manager) SandboxName() string {
	return "agency-" + m.ProjectName
}

// validateCreate checks that name and branch are non-empty, that neither
// starts with '-' (which would be interpreted as a flag by git), and that no
// active workspace already uses the given branch.
func (m *Manager) validateCreate(name, branch string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("workspace name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("workspace name cannot start with '-'")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("branch name cannot be empty")
	}
	if strings.HasPrefix(branch, "-") {
		return errors.New("branch name cannot start with '-'")
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

// EnsureProjectSandbox ensures the shared project sandbox is running.
// If the sandbox is already tracked in state and still running, it returns
// early. Otherwise it builds the image if needed and creates/starts the sandbox.
func (m *Manager) EnsureProjectSandbox(ctx context.Context) error {
	cfg := m.Cfg
	name := m.SandboxName()

	// If we already have a sandbox ID tracked, check if it's still running.
	if m.State.SandboxID != "" {
		if m.Sandbox == nil {
			return errors.New("docker is not available")
		}
		info, err := m.Sandbox.FindByName(ctx, m.State.SandboxID)
		if err == nil && info != nil && info.IsRunning() {
			slog.Debug("project sandbox already running", "sandbox", m.State.SandboxID)
			return nil
		}
		// Sandbox is gone or stopped — recreate it below.
		slog.Info("project sandbox not running, recreating", "sandbox", m.State.SandboxID)
	}

	if m.Sandbox == nil {
		return errors.New("docker is not available")
	}

	if err := m.Sandbox.EnsureImage(ctx, cfg.Sandbox.Image, dockerBuildContext(cfg.Sandbox.DockerfileDir)); err != nil {
		return fmt.Errorf("ensuring sandbox image: %w", err)
	}

	sandboxName, err := m.Sandbox.Ensure(ctx, name, m.ProjectDir, cfg.Sandbox.Image)
	if err != nil {
		return fmt.Errorf("ensuring project sandbox: %w", err)
	}

	m.State.SandboxID = sandboxName
	if err := m.SaveState(); err != nil {
		return fmt.Errorf("saving sandbox state: %w", err)
	}
	slog.Info("project sandbox ready", "sandbox", sandboxName)
	return nil
}

// StopProjectSandboxBackground fires the sandbox stop without waiting.
func (m *Manager) StopProjectSandboxBackground(ctx context.Context) error {
	if m.State.SandboxID == "" || m.Sandbox == nil {
		return nil
	}
	slog.Info("stopping sandbox in background", "sandbox", m.State.SandboxID)
	return m.Sandbox.StopBackground(ctx, m.State.SandboxID)
}

// ensureSandbox ensures the shared project sandbox exists and assigns its ID
// to the workspace. Also generates a session UUID for the workspace if not set.
func (m *Manager) ensureSandbox(ctx context.Context, ws *state.Workspace) error {
	slog.Debug("ensuring project sandbox", "workspace", ws.ID)

	if m.State.SandboxID == "" {
		if err := m.EnsureProjectSandbox(ctx); err != nil {
			return fmt.Errorf("ensuring project sandbox: %w", err)
		}
	}

	// Reference the shared sandbox.
	ws.SandboxID = m.State.SandboxID

	// Generate a session UUID if not already set.
	if ws.SessionID == "" {
		ws.SessionID = generateSessionID()
	}

	return nil
}

// shellEscapeDouble returns s with characters that are special inside
// double-quoted shell strings escaped with backslashes. This is safe for
// interpolating arbitrary values into "..." shell strings: backslash,
// double-quote, dollar sign, and backtick are all neutralized.
func shellEscapeDouble(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
	)
	return r.Replace(s)
}

// buildTrapScript constructs the bash script for the tmux window.
// When resume is true, Claude starts with --resume <sessionID> to pick up
// where it left off. Otherwise it starts with --session-id <sessionID> to
// begin a new session that can be resumed later.
// Returns an error if the workspace's SandboxID or ID fails validation.
func (m *Manager) buildTrapScript(ws *state.Workspace, resume bool) (string, error) {
	if err := sandbox.ValidateSandboxName(ws.SandboxID); err != nil {
		return "", fmt.Errorf("buildTrapScript: %w", err)
	}
	if err := ValidateWorkspaceID(ws.ID); err != nil {
		return "", fmt.Errorf("buildTrapScript: %w", err)
	}
	if err := state.ValidateSessionID(ws.SessionID); err != nil {
		return "", fmt.Errorf("buildTrapCmd: %w", err)
	}
	agencyBin, _ := os.Executable()

	// First iteration uses --session-id to start a new session with our UUID.
	// After first exit (and on resume=true), use --resume to resume that session.
	cmd := fmt.Sprintf("--session-id %s", ws.SessionID)
	if resume {
		cmd = fmt.Sprintf("--resume %s", ws.SessionID)
	}
	return fmt.Sprintf(
		`clear; trap "cd \"%s\" && %s gc --workspace-id %s >/dev/null 2>&1" EXIT; `+
			// Wait for the sandbox to appear in listings AND accept exec (VM may still be booting).
			// Loop unconditionally — if we exit early, the EXIT trap gc's the workspace.
			`for i in $(seq 1 120); do docker sandbox ls -q 2>/dev/null | grep -qx %s && docker sandbox exec %s true >/dev/null 2>&1 && break; sleep 1; done; `+
			// Main loop: exec Claude inside the sandbox, restarting on crash.
			// Use an unconditional loop with an inner sandbox-liveness check so we
			// don't fall through and trigger gc if the sandbox briefly disappears.
			// If --resume fails (non-zero exit), fall back to --session-id so a
			// fresh session is started instead of looping on a dead session ID.
			`CMD="%s"; while true; do docker sandbox ls -q 2>/dev/null | grep -qx %s || { sleep 2; continue; }; docker sandbox exec -it -w "%s" %s claude $CMD; RC=$?; if [ $RC -ne 0 ] && [ "$CMD" = "--resume %s" ]; then CMD="--session-id %s"; else CMD="--resume %s"; fi; sleep 1; done`,
		shellEscapeDouble(m.ProjectDir), agencyBin, ws.ID,
		ws.SandboxID, ws.SandboxID,
		shellEscapeDouble(cmd), ws.SandboxID,
		shellEscapeDouble(ws.WorktreePath), ws.SandboxID,
		ws.SessionID, ws.SessionID, ws.SessionID,
	), nil
}

// openTmuxWindow creates a new tmux window for ws whose pane process IS the
// trap-loop script (no intermediate shell, no prompt flash).
// If resume is true, Claude starts with --resume.
func (m *Manager) openTmuxWindow(ws *state.Workspace, resume bool) error {
	action := "provisioning"
	if resume {
		action = "resuming"
	}
	slog.Debug(action+" tmux window", "workspace", ws.ID, "name", ws.DisplayName())

	script, err := m.buildTrapScript(ws, resume)
	if err != nil {
		return fmt.Errorf("%s: building trap script: %w", action, err)
	}

	windowID, err := m.Tmux.NewWindowWithCommand(ws.Name, script)
	if err != nil {
		return fmt.Errorf("%s: creating tmux window: %w", action, err)
	}
	ws.TmuxWindow = windowID

	if panes, err := m.Tmux.GetWindowPanes(windowID); err == nil && len(panes) > 0 {
		ws.PaneID = panes[0]
	}
	return nil
}

// provisionTmux opens a new tmux window for ws, captures the pane ID, and
// launches the agent inside the sandbox.
func (m *Manager) provisionTmux(ws *state.Workspace) error {
	return m.openTmuxWindow(ws, false)
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

	// Step 1b: write Claude Code hooks into worktree for status reporting.
	if err := templates.WriteClaudeHooks(ws.WorktreePath); err != nil {
		slog.Warn("failed to write Claude hooks", "error", err)
	}

	// Step 2: ensure shared project sandbox and assign session ID.
	if err := m.ensureSandbox(ctx, ws); err != nil {
		return fail(err)
	}

	// Step 3: open tmux window and launch agent.
	if err := m.provisionTmux(ws); err != nil {
		return fail(err)
	}

	// Step 4: record the new workspace as active and mark running.
	// The sidebar (sole owner of layout) will create the split and swap on the
	// next tick or when it receives workspaceCreatedMsg.
	m.State.ActiveWorkspaceID = ws.ID
	ws.State = state.StateRunning
	ws.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fail(fmt.Errorf("saving running state: %w", err))
	}

	// Step 5: focus the main window. NewWindowWithCommand switches tmux's
	// active window to the new workspace window; switch back so the sidebar
	// stays visible while the popup is still open.
	if m.State.MainWindowID != "" {
		_ = m.Tmux.SelectWindow(m.State.MainWindowID)
	}

	slog.Info("workspace created successfully", "workspace", ws.ID, "name", ws.Name, "branch", ws.Branch)
	return ws, nil
}

// Remove tears down a workspace: sends C-c to interrupt Claude, kills the tmux
// window, removes the worktree, and removes the workspace from state.
// The shared project sandbox is NOT stopped or removed.
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

// reconcileResult holds the parallel query results used by Reconcile.
type reconcileResult struct {
	windows        []tmux.Window
	sandboxRunning bool
	worktrees      []worktree.Info
	windowsErr     error
	sandboxErr     error
	wtErr          error
}

// gatherReconcileResources queries tmux, Docker sandbox status, and git worktrees
// in parallel and returns the combined result.
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
		if m.Sandbox != nil && m.State.SandboxID != "" {
			running, err := m.Sandbox.IsRunning(ctx, m.State.SandboxID)
			res.sandboxRunning = running
			res.sandboxErr = err
		} else if m.Sandbox == nil {
			res.sandboxErr = errors.New("docker is not available")
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

// buildLookupSets constructs boolean lookup maps for windows and worktree paths
// from the reconcile result. Maps are only populated when the corresponding
// query succeeded to avoid destructive changes on partial data.
func buildLookupSets(res *reconcileResult) (windowSet, worktreeSet map[string]bool) {
	windowSet = make(map[string]bool, len(res.windows))
	if res.windowsErr == nil {
		for _, w := range res.windows {
			windowSet[w.ID] = true
		}
	}

	worktreeSet = make(map[string]bool, len(res.worktrees))
	if res.wtErr == nil {
		for _, wt := range res.worktrees {
			worktreeSet[wt.Path] = true
		}
	}
	return windowSet, worktreeSet
}

// Reconcile queries tmux, Docker sandbox status, and git worktrees in parallel
// then corrects workspace states that have drifted from reality.
func (m *Manager) Reconcile(ctx context.Context) error {
	res := m.gatherReconcileResources(ctx)
	slog.Debug("reconcile resources gathered", "windows", len(res.windows), "sandboxRunning", res.sandboxRunning, "worktrees", len(res.worktrees))

	windowSet, worktreeSet := buildLookupSets(&res)
	toDelete, changed := m.reconcileWorkspaces(ctx, &res, windowSet, worktreeSet)

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
	ctx context.Context,
	ws *state.Workspace,
	res *reconcileResult,
	windowSet, worktreeSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (shouldDelete, changed bool) {
	switch ws.State {
	case state.StateRunning:
		return m.reconcileRunning(ws, res, windowSet, markFailed)

	case state.StateProvisioning:
		return false, m.reconcileProvisioning(ws, res, markFailed)

	case state.StateCreating:
		if ws.WorktreePath == "" {
			markFailed(ws, "workspace stuck in creating state")
			return false, true
		}

	case state.StateCompleting:
		// Sandbox is shared — completing just means the workspace is done.
		// Transition to done state.
		ws.State = state.StateDone
		ws.UpdatedAt = time.Now().UTC()
		return false, true

	case state.StatePaused:
		return false, m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

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
	windowSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (shouldDelete, changed bool) {
	// If the sandbox query succeeded and reports it's not running, mark failed.
	// Only check when a sandbox was actually assigned; SandboxID == "" means
	// Docker wasn't used for this workspace, so a "not running" result is benign.
	if res.sandboxErr == nil && !res.sandboxRunning && m.State.SandboxID != "" {
		markFailed(ws, "sandbox disappeared")
		return false, true
	}
	if res.windowsErr == nil && ws.TmuxWindow != "" && !windowSet[ws.TmuxWindow] {
		slog.Warn("tmux window disappeared, recreating", "workspace", ws.ID, "old_window", ws.TmuxWindow)
		if newWin, err := m.Tmux.NewWindow(worktree.Slugify(ws.Branch)); err == nil {
			if validateErr := sandbox.ValidateSandboxName(ws.SandboxID); validateErr != nil {
				slog.Warn("skipping docker sandbox exec: invalid sandbox name", "workspace", ws.ID, "error", validateErr)
			} else {
				_ = m.Tmux.SendKeys(newWin, fmt.Sprintf("docker sandbox exec -it -w %q %s bash", ws.WorktreePath, ws.SandboxID))
			}
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
		if panes, err := m.Tmux.GetWindowPanes(ws.TmuxWindow); err == nil && !slices.Contains(panes, ws.PaneID) && len(panes) > 0 {
			ws.PaneID = panes[0]
			ws.UpdatedAt = time.Now().UTC()
			return false, true
		}
	}
	return false, false
}

// reconcileProvisioning handles reconcile logic for a workspace in StateProvisioning.
func (m *Manager) reconcileProvisioning(
	ws *state.Workspace,
	res *reconcileResult,
	markFailed func(*state.Workspace, string),
) (changed bool) {
	// If sandbox query had an error, we can't make progress.
	if res.sandboxErr != nil {
		return false
	}
	if res.sandboxRunning {
		ws.State = state.StateRunning
		ws.UpdatedAt = time.Now().UTC()
		return true
	}
	markFailed(ws, "sandbox not found during reconciliation")
	return true
}

// reconcilePaused handles reconcile logic for a workspace in StatePaused.
// It attempts to resume the workspace by ensuring the project sandbox is
// running and attaching a new tmux window.
// Returns true if the workspace state changed.
func (m *Manager) reconcilePaused(
	ctx context.Context,
	ws *state.Workspace,
	res *reconcileResult,
	worktreeSet map[string]bool,
	markFailed func(*state.Workspace, string),
) (changed bool) {
	// If the worktree is gone, the workspace cannot be resumed.
	if res.wtErr == nil && ws.WorktreePath != "" && !worktreeSet[ws.WorktreePath] {
		markFailed(ws, "worktree disappeared while paused")
		return true
	}

	// If Docker is unavailable, do nothing — we cannot make progress.
	if res.sandboxErr != nil || m.Sandbox == nil {
		return false
	}

	// Probe whether the sandbox still exists before ensuring it. If it was
	// garbage-collected and recreated, old Claude session data is gone and we
	// must start a fresh session instead of trying to --resume.
	sandboxExisted := false
	if m.State.SandboxID != "" {
		info, findErr := m.Sandbox.FindByName(ctx, m.State.SandboxID)
		sandboxExisted = (findErr == nil && info != nil)
	}

	// Ensure the shared project sandbox is running.
	if err := m.EnsureProjectSandbox(ctx); err != nil {
		markFailed(ws, fmt.Sprintf("ensuring project sandbox: %v", err))
		return true
	}

	// Make sure this workspace references the sandbox.
	ws.SandboxID = m.State.SandboxID

	// If sandbox was freshly created, old session data is gone.
	// Generate a fresh session ID and start a new session.
	if !sandboxExisted {
		ws.SessionID = generateSessionID()
	} else if ws.SessionID == "" {
		ws.SessionID = generateSessionID()
	}

	// Reattach via tmux. Only use --resume when the sandbox existed (session
	// data preserved); otherwise start a fresh session.
	if err := m.openTmuxWindow(ws, sandboxExisted); err != nil {
		markFailed(ws, fmt.Sprintf("resuming tmux: %v", err))
		return true
	}

	ws.State = state.StateRunning
	ws.UpdatedAt = time.Now().UTC()
	return true
}

// reconcileWorkspaces processes all workspaces and handles state transitions based
// on the current tmux, docker, and worktree state. Returns workspaces to delete
// and whether any changes were made.
func (m *Manager) reconcileWorkspaces(ctx context.Context, res *reconcileResult, windowSet, worktreeSet map[string]bool) ([]string, bool) {
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
		del, chg := m.reconcileOneWorkspace(ctx, ws, res, windowSet, worktreeSet, markFailed)
		if chg {
			changed = true
		}
		if del {
			toDelete = append(toDelete, id)
		}
	}

	return toDelete, changed
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

// SwapActivePane swaps the given workspace's pane into the visible right slot
// of the main window (:0.1) using tmux swap-pane. If another workspace is
// currently active, its pane is swapped back to its own window first.
// No-op when ws.PaneID is empty or WorkspacePaneID is not yet set (the
// sidebar's ensureSplitOnFirstWorkspace is the sole owner of split creation).
func (m *Manager) SwapActivePane(wsID string) error {
	slog.Debug("swapping active pane", "workspace", wsID)
	ws, ok := m.State.Workspaces[wsID]
	if !ok || ws.PaneID == "" {
		return nil
	}

	// The sidebar's ensureSplitOnFirstWorkspace owns split creation.
	// If the split doesn't exist yet, return early and let the sidebar handle it.
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

// SaveState persists current state to disk. Callers must hold the project flock
// before mutating State to prevent concurrent writes from popup processes.
// The PID field is refreshed on every write so the lock owner is always current.
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
// quit purposes (i.e. has a running or provisioning sandbox session that needs stopping).
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

// StopWorkspace transitions ws to StatePaused. The shared project sandbox is
// NOT stopped — it is shared by all workspaces. Does not remove the worktree
// or state entry.
func (m *Manager) StopWorkspace(ctx context.Context, ws *state.Workspace) error {
	slog.Info("stopping workspace", "workspace", ws.ID)
	ws.State = state.StatePaused
	ws.UpdatedAt = time.Now().UTC()
	return m.SaveState()
}

// StopWorkspaceBackground immediately transitions ws to StatePaused without
// stopping the shared project sandbox (which remains running for other workspaces).
func (m *Manager) StopWorkspaceBackground(ctx context.Context, ws *state.Workspace) error {
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
