package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// initialises the state file, creates a tmux client, and optionally connects to
// the Docker daemon. A nil Sandbox is not fatal — the TUI can still list
// existing sessions without Docker.
func NewManager(projectDir string, cfg *config.Config) (*Manager, error) {
	projectName := filepath.Base(projectDir)
	statePath := filepath.Join(projectDir, ".tool", "state.json")

	s, err := state.Read(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			s = state.Default(projectName, filepath.Join(projectDir, ".bare"))
			// Persist immediately so subsequent reads don't re-default.
			_ = state.Write(statePath, s)
		} else {
			return nil, fmt.Errorf("loading state: %w", err)
		}
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

// Create provisions a full session for the given branch: worktree → sandbox →
// tmux window. On any error after the initial state entry is written the
// session is marked as failed before returning the error.
func (m *Manager) Create(ctx context.Context, branch string) (*state.Session, error) {
	id := generateID()

	now := time.Now().UTC()
	sess := &state.Session{
		ID:        id,
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
		sess.State = state.StateFailed
		sess.Error = &msg
		sess.UpdatedAt = time.Now().UTC()
		_ = m.SaveState()
		return sess, err
	}

	// Pre-check: reject duplicate branches across existing sessions.
	for _, existing := range m.State.Sessions {
		if existing.Branch == branch && existing.State != state.StateDone && existing.State != state.StateFailed {
			return fail(fmt.Errorf("branch %q already has an active session (%s)", branch, existing.ID))
		}
	}

	// Step 1: create git worktree.
	wtPath, err := worktree.Create(m.State.BarePath, m.ProjectName, branch)
	if err != nil {
		return fail(fmt.Errorf("creating worktree: %w", err))
	}
	sess.State = state.StateProvisioning
	sess.WorktreePath = wtPath
	sess.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fail(fmt.Errorf("saving provisioning state: %w", err))
	}

	// Step 2: require sandbox.
	if m.Sandbox == nil {
		return fail(fmt.Errorf("docker is not available"))
	}

	// Step 3: build environment variables.
	env := []string{}
	if key := m.Cfg.Credentials.AnthropicAPIKey; key != "" {
		env = append(env, "ANTHROPIC_API_KEY="+key)
	}
	if tok := m.Cfg.Credentials.GithubToken; tok != "" {
		env = append(env, "GITHUB_TOKEN="+tok)
	}
	if v := os.Getenv("GIT_USER"); v != "" {
		env = append(env, "GIT_USER="+v)
	}
	if v := os.Getenv("GIT_EMAIL"); v != "" {
		env = append(env, "GIT_EMAIL="+v)
	}

	// Step 4: determine whether the project config file should be mounted.
	configMount := ""
	cfgPath := config.ProjectConfigPath(m.ProjectDir)
	if _, err := os.Stat(cfgPath); err == nil {
		configMount = cfgPath
	}

	// Step 5: create container.
	containerID, err := m.Sandbox.Create(ctx, sandbox.CreateOpts{
		Image:         m.Cfg.Sandbox.Image,
		Name:          "claude-sb-" + m.ProjectName + "-" + worktree.Slugify(branch),
		WorktreeMount: sess.WorktreePath,
		ConfigMount:   configMount,
		Env:           env,
	})
	if err != nil {
		return fail(fmt.Errorf("creating sandbox: %w", err))
	}
	sess.SandboxID = containerID

	// Step 6: start container.
	if err := m.Sandbox.Start(ctx, containerID); err != nil {
		return fail(fmt.Errorf("starting sandbox: %w", err))
	}

	// Step 7: open tmux window.
	windowID, err := m.Tmux.NewWindow(worktree.Slugify(branch))
	if err != nil {
		return fail(fmt.Errorf("creating tmux window: %w", err))
	}
	sess.TmuxWindow = windowID

	// Step 8: launch the agent inside the container.
	if err := m.Tmux.SendKeys(windowID, fmt.Sprintf("docker exec -it %s bash -c claude", containerID)); err != nil {
		return fail(fmt.Errorf("sending keys to tmux window: %w", err))
	}

	// Step 9: mark running and persist.
	sess.State = state.StateRunning
	sess.UpdatedAt = time.Now().UTC()
	if err := m.SaveState(); err != nil {
		return fail(fmt.Errorf("saving running state: %w", err))
	}

	// Step 10: bring the new window into focus.
	_ = m.Tmux.SelectWindow(windowID)

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
	worktrees  []worktree.WorktreeInfo
	windowsErr error
	contsErr   error
	wtErr      error
}

// Reconcile queries tmux, Docker, and git worktrees in parallel then corrects
// session states that have drifted from reality.
func (m *Manager) Reconcile(ctx context.Context) error {
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

	// Build lookup maps.
	windowSet := make(map[string]bool, len(res.windows))
	for _, w := range res.windows {
		windowSet[w.ID] = true
	}

	containerMap := make(map[string]sandbox.ContainerInfo, len(res.containers))
	for _, c := range res.containers {
		containerMap[c.Name] = c
	}

	worktreeSet := make(map[string]bool, len(res.worktrees))
	for _, wt := range res.worktrees {
		worktreeSet[wt.Path] = true
	}

	changed := false
	toDelete := []string{}

	for id, sess := range m.State.Sessions {
		containerName := "claude-sb-" + m.ProjectName + "-" + worktree.Slugify(sess.Branch)

		switch sess.State {
		case state.StateRunning:
			if _, inContainerMap := containerMap[containerName]; !inContainerMap {
				msg := "sandbox disappeared"
				sess.State = state.StateFailed
				sess.Error = &msg
				sess.UpdatedAt = time.Now().UTC()
				changed = true
			}
			// If tmux window is gone but sandbox still exists, leave as running;
			// the window can be recreated.

		case state.StateProvisioning:
			if _, inContainerMap := containerMap[containerName]; inContainerMap {
				sess.State = state.StateRunning
				sess.UpdatedAt = time.Now().UTC()
				changed = true
			} else {
				msg := "sandbox not found during reconciliation"
				sess.State = state.StateFailed
				sess.Error = &msg
				sess.UpdatedAt = time.Now().UTC()
				changed = true
			}

		case state.StateDone:
			if sess.WorktreePath != "" && !worktreeSet[sess.WorktreePath] {
				toDelete = append(toDelete, id)
				changed = true
			}
		}
	}

	for _, id := range toDelete {
		delete(m.State.Sessions, id)
	}

	if changed {
		return m.SaveState()
	}
	return nil
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
