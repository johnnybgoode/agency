// Package session manages agent sessions with worktrees and containers.
package session

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

// Manager coordinates session lifecycle: worktree creation, sandbox
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
// existing sessions without Docker.
func NewManager(projectDir string, cfg *config.Config) (*Manager, error) {
	projectName := filepath.Base(projectDir)
	statePath := filepath.Join(projectDir, ".tool", "state.json")

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

// generateID returns a random session ID with the "sess-" prefix.
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("session: rand.Read: %v", err))
	}
	return "sess-" + hex.EncodeToString(b)
}

// validateCreate checks that name and branch are non-empty and that no active
// session already uses the given branch.
func (m *Manager) validateCreate(name, branch string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	for _, existing := range m.State.Sessions {
		if existing.Branch == branch && existing.State != state.StateDone && existing.State != state.StateFailed {
			return fmt.Errorf("branch %q already has an active session (%s)", branch, existing.ID)
		}
	}
	return nil
}

// provisionWorktree creates the git worktree for sess and updates the session
// fields accordingly.
func (m *Manager) provisionWorktree(sess *state.Session) error {
	wtPath, err := worktree.Create(m.State.BarePath, m.ProjectName, sess.Branch)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	sess.State = state.StateProvisioning
	sess.WorktreePath = wtPath
	sess.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fmt.Errorf("saving provisioning state: %w", err)
	}
	return nil
}

// provisionContainer creates and starts the Docker container for sess.
func (m *Manager) provisionContainer(ctx context.Context, sess *state.Session, cfg *config.Config) error {
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
		Name:          "claude-sb-" + m.ProjectName + "-" + worktree.Slugify(sess.Branch) + "-" + sess.ID,
		WorktreeMount: sess.WorktreePath,
		ConfigMount:   configMount,
		Env:           env,
	})
	if err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	sess.SandboxID = containerID

	if err := m.Sandbox.Start(ctx, containerID); err != nil {
		return fmt.Errorf("starting sandbox: %w", err)
	}
	return nil
}

// provisionTmux opens a new tmux window for sess, captures the pane ID, and
// launches the agent inside the container.
func (m *Manager) provisionTmux(sess *state.Session) error {
	windowID, err := m.Tmux.NewWindow(worktree.Slugify(sess.Branch))
	if err != nil {
		return fmt.Errorf("creating tmux window: %w", err)
	}
	sess.TmuxWindow = windowID

	// Capture pane ID for the new window.
	if panes, err := m.Tmux.GetWindowPanes(windowID); err == nil && len(panes) > 0 {
		sess.PaneID = panes[0]
	}

	if err := m.Tmux.SendKeys(windowID, fmt.Sprintf("docker exec -it %s bash -c claude", sess.SandboxID)); err != nil {
		return fmt.Errorf("sending keys to tmux window: %w", err)
	}
	return nil
}

// Create provisions a full session for the given name and branch: worktree → sandbox →
// tmux window. On any error after the initial state entry is written the
// session is marked as failed before returning the error.
func (m *Manager) Create(ctx context.Context, name, branch string) (*state.Session, error) {
	if err := m.validateCreate(name, branch); err != nil {
		return nil, err
	}

	id := generateID()
	now := time.Now().UTC()
	sess := &state.Session{
		ID:        id,
		Name:      name,
		State:     state.StateCreating,
		Branch:    branch,
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.State.Sessions[id] = sess
	if err := m.SaveState(); err != nil {
		return nil, fmt.Errorf("saving initial state: %w", err)
	}

	fail := func(err error) (*state.Session, error) {
		msg := err.Error()
		fromState := string(sess.State)
		sess.FailedFrom = &fromState
		sess.State = state.StateFailed
		sess.Error = &msg
		sess.UpdatedAt = time.Now().UTC()
		_ = m.SaveState()
		return sess, err
	}

	// Step 1: create git worktree.
	if err := m.provisionWorktree(sess); err != nil {
		return fail(err)
	}

	// Step 2: load session-local config overlay if present.
	cfg := m.Cfg
	if localCfg, err := config.Load(config.SessionConfigPath(sess.WorktreePath)); err == nil {
		cfg = config.Merge(m.Cfg, localCfg)
	}

	// Step 3: create and start container.
	if err := m.provisionContainer(ctx, sess, cfg); err != nil {
		return fail(err)
	}

	// Step 4: open tmux window and launch agent.
	if err := m.provisionTmux(sess); err != nil {
		return fail(err)
	}

	// Step 5: mark running and persist.
	sess.State = state.StateRunning
	sess.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fail(fmt.Errorf("saving running state: %w", err))
	}

	// Step 6: bring the new window into focus.
	_ = m.Tmux.SelectWindow(sess.TmuxWindow)

	return sess, nil
}

// Remove tears down a session: stops/removes its container, deletes the
// worktree, kills the tmux window, and removes the session from state.
func (m *Manager) Remove(ctx context.Context, sessionID string) error {
	sess, ok := m.State.Sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Interrupt the foreground process so it can clean up.
	if sess.TmuxWindow != "" {
		_ = m.Tmux.SendKeys(sess.TmuxWindow, "C-c")
	}

	// Stop and remove the sandbox container.
	if sess.SandboxID != "" && m.Sandbox != nil {
		_ = m.Sandbox.Stop(ctx, sess.SandboxID, 5)
		_ = m.Sandbox.Remove(ctx, sess.SandboxID)
	}

	// Remove the git worktree.
	if sess.WorktreePath != "" {
		_ = worktree.Remove(m.State.BarePath, sess.WorktreePath)
	}

	// Kill the tmux window.
	if sess.TmuxWindow != "" {
		_ = m.Tmux.KillWindow(sess.TmuxWindow)
	}

	delete(m.State.Sessions, sessionID)
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
			res.containers, res.contsErr = m.Sandbox.ListByProject(ctx, "claude-sb-"+m.ProjectName)
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

// cleanupActiveSessionID clears ActiveSessionID if it refers to a session that
// no longer exists or is not running. Returns true if a change was made.
func (m *Manager) cleanupActiveSessionID() bool {
	if m.State.ActiveSessionID == "" {
		return false
	}
	activeSess, ok := m.State.Sessions[m.State.ActiveSessionID]
	if !ok || activeSess.State != state.StateRunning {
		m.State.ActiveSessionID = ""
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

// logOrphans prints worktrees and containers not referenced by any known session.
func (m *Manager) logOrphans(res *reconcileResult) {
	knownWorktrees := make(map[string]bool, len(m.State.Sessions))
	for _, sess := range m.State.Sessions {
		if sess.WorktreePath != "" {
			knownWorktrees[sess.WorktreePath] = true
		}
	}

	if res.wtErr == nil {
		for _, wt := range res.worktrees {
			if !knownWorktrees[wt.Path] {
				fmt.Fprintf(os.Stderr, "[reconcile] orphan worktree: %s\n", wt.Path)
			}
		}
	}

	if m.Sandbox == nil || res.contsErr != nil {
		return
	}
	knownSandboxIDs := make(map[string]bool, len(m.State.Sessions))
	for _, sess := range m.State.Sessions {
		if sess.SandboxID != "" {
			knownSandboxIDs[sess.SandboxID] = true
		}
	}
	for _, c := range res.containers {
		if !knownSandboxIDs[c.ID] {
			fmt.Fprintf(os.Stderr, "[reconcile] orphan container: %s\n", c.Name)
		}
	}
}

// Reconcile queries tmux, Docker, and git worktrees in parallel then corrects
// session states that have drifted from reality.
func (m *Manager) Reconcile(ctx context.Context) error {
	res := m.gatherReconcileResources(ctx)

	windowSet, containerIDSet, worktreeSet := buildLookupSets(&res)
	toDelete, changed := m.reconcileSessions(&res, windowSet, containerIDSet, worktreeSet)

	for _, id := range toDelete {
		delete(m.State.Sessions, id)
	}

	if m.cleanupActiveSessionID() {
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

// reconcileOneSession handles the reconcile logic for a single session.
// Returns (shouldDelete bool, changed bool).
func (m *Manager) reconcileOneSession(
	id string,
	sess *state.Session,
	res *reconcileResult,
	windowSet, containerIDSet, worktreeSet map[string]bool,
	markFailed func(*state.Session, string),
) (shouldDelete, changed bool) {
	switch sess.State {
	case state.StateRunning:
		return m.reconcileRunning(sess, res, windowSet, containerIDSet, markFailed)

	case state.StateProvisioning:
		return false, m.reconcileProvisioning(sess, res, containerIDSet, markFailed)

	case state.StateCreating:
		if sess.WorktreePath == "" {
			markFailed(sess, "session stuck in creating state")
			return false, true
		}

	case state.StateCompleting:
		if res.contsErr == nil && (sess.SandboxID == "" || !containerIDSet[sess.SandboxID]) {
			sess.State = state.StateDone
			sess.UpdatedAt = time.Now().UTC()
			return false, true
		}

	case state.StatePaused:
		if res.wtErr == nil && sess.WorktreePath != "" && !worktreeSet[sess.WorktreePath] {
			markFailed(sess, "worktree disappeared while paused")
			return false, true
		}

	case state.StateDone:
		if res.wtErr == nil && sess.WorktreePath != "" && !worktreeSet[sess.WorktreePath] {
			return true, true
		}
	}

	_ = id
	return false, false
}

// reconcileRunning handles reconcile logic for a session in StateRunning.
func (m *Manager) reconcileRunning(
	sess *state.Session,
	res *reconcileResult,
	windowSet, containerIDSet map[string]bool,
	markFailed func(*state.Session, string),
) (shouldDelete, changed bool) {
	if res.contsErr == nil && sess.SandboxID != "" && !containerIDSet[sess.SandboxID] {
		markFailed(sess, "sandbox disappeared")
		return false, true
	}
	if res.windowsErr == nil && sess.TmuxWindow != "" && !windowSet[sess.TmuxWindow] {
		if newWin, err := m.Tmux.NewWindow(worktree.Slugify(sess.Branch)); err == nil {
			_ = m.Tmux.SendKeys(newWin, fmt.Sprintf("docker exec -it %s bash", sess.SandboxID))
			sess.TmuxWindow = newWin
			// Capture new pane ID.
			if panes, err := m.Tmux.GetWindowPanes(newWin); err == nil && len(panes) > 0 {
				sess.PaneID = panes[0]
			}
			sess.UpdatedAt = time.Now().UTC()
			return false, true
		}
	} else if res.windowsErr == nil && sess.TmuxWindow != "" && windowSet[sess.TmuxWindow] && sess.PaneID != "" {
		// Verify pane still exists in the window; if not, re-capture.
		if panes, err := m.Tmux.GetWindowPanes(sess.TmuxWindow); err == nil {
			paneFound := false
			for _, p := range panes {
				if p == sess.PaneID {
					paneFound = true
					break
				}
			}
			if !paneFound && len(panes) > 0 {
				sess.PaneID = panes[0]
				sess.UpdatedAt = time.Now().UTC()
				return false, true
			}
		}
	}
	return false, false
}

// reconcileProvisioning handles reconcile logic for a session in StateProvisioning.
func (m *Manager) reconcileProvisioning(
	sess *state.Session,
	res *reconcileResult,
	containerIDSet map[string]bool,
	markFailed func(*state.Session, string),
) (changed bool) {
	if res.contsErr == nil && sess.SandboxID != "" {
		if containerIDSet[sess.SandboxID] {
			sess.State = state.StateRunning
			sess.UpdatedAt = time.Now().UTC()
			return true
		}
		markFailed(sess, "sandbox not found during reconciliation")
		return true
	} else if res.contsErr == nil {
		markFailed(sess, "sandbox not found during reconciliation")
		return true
	}
	return false
}

// reconcileSessions processes all sessions and handles state transitions based on
// the current tmux, docker, and worktree state. Returns sessions to delete and
// whether any changes were made.
func (m *Manager) reconcileSessions(res *reconcileResult, windowSet, containerIDSet, worktreeSet map[string]bool) ([]string, bool) {
	markFailed := func(sess *state.Session, reason string) {
		fromState := string(sess.State)
		sess.FailedFrom = &fromState
		sess.State = state.StateFailed
		sess.Error = &reason
		sess.UpdatedAt = time.Now().UTC()
	}

	changed := false
	toDelete := []string{}

	for id, sess := range m.State.Sessions {
		del, chg := m.reconcileOneSession(id, sess, res, windowSet, containerIDSet, worktreeSet, markFailed)
		if chg {
			changed = true
		}
		if del {
			toDelete = append(toDelete, id)
		}
	}

	return toDelete, changed
}

// List returns all sessions sorted by creation time (oldest first).
func (m *Manager) List() []*state.Session {
	sessions := make([]*state.Session, 0, len(m.State.Sessions))
	for _, s := range m.State.Sessions {
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})
	return sessions
}

// SaveState persists current state to disk, updating the PID field first.
func (m *Manager) SaveState() error {
	m.State.PID = os.Getpid()
	return state.Write(m.StatePath, m.State)
}
