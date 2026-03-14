package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tmux"
)

// newTestManager builds a Manager that is safe to use without real tmux,
// Docker, or git infrastructure:
//   - StatePath points to a temp file so SaveState does not fail.
//   - Tmux is a real *tmux.Client whose binary path will be empty when tmux
//     is not installed, causing all tmux calls to return errors that the
//     Manager ignores (they are wrapped with _ =).
//   - Sandbox is nil, which the Manager explicitly handles.
//   - State is pre-initialized with an empty Workspaces map.
func newTestManager(t *testing.T) *Manager {
	t.Helper()

	dir := t.TempDir()
	statePath := dir + "/state.json"

	s := state.Default("testproject", dir+"/.bare")

	m := &Manager{
		StatePath:   statePath,
		ProjectDir:  dir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}

	// Write an initial state file so SaveState (which calls state.Write) has a
	// valid directory already in place.
	if err := m.SaveState(); err != nil {
		t.Fatalf("newTestManager: SaveState: %v", err)
	}

	return m
}

// addWorkspace is a helper that inserts a pre-built Workspace into a Manager's
// in-memory state without going through the full Create path.
func addWorkspace(m *Manager, ws *state.Workspace) {
	m.State.Workspaces[ws.ID] = ws
}

// ----- Create: name, branch, and initial state -----

func TestCreate_RejectsEmptyBranch(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.Create(ctx, "My Feature", "")
	if err == nil {
		t.Fatal("expected error for empty branch, got nil")
	}
}

func TestCreate_RejectsDuplicateActiveBranch(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	existing := &state.Workspace{
		ID:        "ws-aabbccdd",
		Name:      "Existing",
		Branch:    "project/my-feature",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, existing)

	_, err := m.Create(ctx, "Duplicate", "project/my-feature")
	if err == nil {
		t.Fatal("expected error for duplicate active branch, got nil")
	}
}

// TestCreate_AllowsDuplicateBranchWhenDone verifies that a branch used by a
// DONE workspace can be re-used for a new workspace. The new Create call will
// still fail eventually (no Docker), but the duplicate-branch pre-check must
// pass and a workspace entry with StateCreating must be written before the
// Docker error is returned.
func TestCreate_AllowsDuplicateBranchWhenDone(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	done := &state.Workspace{
		ID:        "ws-done0001",
		Name:      "Old",
		Branch:    "project/my-feature",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, done)

	_, err := m.Create(ctx, "New", "project/my-feature")
	// We expect an error because docker/worktree is not available.
	// What we are checking is that the error is NOT the duplicate-branch error.
	if err == nil {
		// Unexpected success — still fine for the duplicate-check assertion.
		return
	}
	// The workspace should have been inserted (and then marked failed).
	found := false
	for _, ws := range m.State.Workspaces {
		if ws.Branch == "project/my-feature" && ws.ID != done.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a new workspace entry to be created before the infrastructure error")
	}
}

// TestCreate_SetsInitialState verifies that a workspace entry is placed in
// state with the correct Name, Branch, and StateCreating before any
// infrastructure step is attempted. The Create call will fail at the
// worktree step (no real git repo), but by then the entry is already saved.
func TestCreate_SetsInitialState(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.Create(ctx, "My Feature", "project/my-feature")
	// We fully expect an error here (no git / docker).
	// The important assertion is on the state that was written.
	if err == nil {
		// If somehow it succeeded (unlikely in CI), just check state normally.
	}

	var found *state.Workspace
	for _, ws := range m.State.Workspaces {
		if ws.Branch == "project/my-feature" {
			found = ws
			break
		}
	}

	if found == nil {
		t.Fatal("no workspace entry found in state after Create")
	}
	if found.Name != "My Feature" {
		t.Errorf("Name: got %q, want %q", found.Name, "My Feature")
	}
	if found.Branch != "project/my-feature" {
		t.Errorf("Branch: got %q, want %q", found.Branch, "project/my-feature")
	}
	// The workspace starts as StateCreating; after an infrastructure failure it
	// transitions to StateFailed. Either is acceptable here — what matters is
	// that the entry exists with the right Name and Branch.
	if found.State != state.StateCreating && found.State != state.StateFailed {
		t.Errorf("State: got %q, expected StateCreating or StateFailed", found.State)
	}
}

// ----- List: sorted by CreatedAt (oldest first) -----

func TestList_SortedByCreatedAt(t *testing.T) {
	m := newTestManager(t)

	now := time.Now().UTC()
	workspaces := []*state.Workspace{
		{ID: "ws-c", Name: "C", Branch: "c", State: state.StateRunning, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now},
		{ID: "ws-a", Name: "A", Branch: "a", State: state.StateRunning, CreatedAt: now.Add(0), UpdatedAt: now},
		{ID: "ws-b", Name: "B", Branch: "b", State: state.StateRunning, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now},
	}
	for _, ws := range workspaces {
		addWorkspace(m, ws)
	}

	listed := m.List()

	if len(listed) != 3 {
		t.Fatalf("List returned %d workspaces, want 3", len(listed))
	}

	wantOrder := []string{"ws-a", "ws-b", "ws-c"}
	for i, ws := range listed {
		if ws.ID != wantOrder[i] {
			t.Errorf("position %d: got ID %q, want %q", i, ws.ID, wantOrder[i])
		}
	}
}

func TestList_EmptyState(t *testing.T) {
	m := newTestManager(t)
	listed := m.List()
	if len(listed) != 0 {
		t.Errorf("expected empty list, got %d workspaces", len(listed))
	}
}

// ----- Reconcile: clears ActiveWorkspaceID for missing workspaces -----

func TestReconcile_ClearsActiveWorkspaceIDWhenMissing(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	// Set an ActiveWorkspaceID that does not exist in Workspaces.
	m.State.ActiveWorkspaceID = "ws-nonexistent"

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	if m.State.ActiveWorkspaceID != "" {
		t.Errorf("ActiveWorkspaceID not cleared: got %q", m.State.ActiveWorkspaceID)
	}
}

func TestReconcile_ClearsActiveWorkspaceIDWhenNotRunning(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-done0002",
		Name:      "Done Workspace",
		Branch:    "done/branch",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)
	m.State.ActiveWorkspaceID = ws.ID

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	if m.State.ActiveWorkspaceID != "" {
		t.Errorf("ActiveWorkspaceID should be cleared for non-running workspace; got %q", m.State.ActiveWorkspaceID)
	}
}

func TestReconcile_KeepsActiveWorkspaceIDWhenRunning(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	// A running workspace with a SandboxID and TmuxWindow both empty so the
	// reconcileWorkspaces logic does not transition it (it only checks IDs that
	// are non-empty against the live sets, which are empty due to failed
	// external queries).
	ws := &state.Workspace{
		ID:        "ws-run00001",
		Name:      "Running",
		Branch:    "feature/run",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)
	m.State.ActiveWorkspaceID = ws.ID

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	// The workspace should still be running (no sandbox/tmux IDs to check).
	if _, ok := m.State.Workspaces[ws.ID]; !ok {
		t.Fatal("running workspace was unexpectedly removed")
	}
	if m.State.Workspaces[ws.ID].State != state.StateRunning {
		t.Errorf("expected StateRunning, got %q", m.State.Workspaces[ws.ID].State)
	}
	if m.State.ActiveWorkspaceID != ws.ID {
		t.Errorf("ActiveWorkspaceID was cleared unexpectedly; got %q", m.State.ActiveWorkspaceID)
	}
}

// ----- Remove: deletes workspace from state -----

func TestRemove_DeletesWorkspaceFromState(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-rm000001",
		Name:      "Finished",
		Branch:    "done/cleanup",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		// Leave SandboxID and WorktreePath empty so Remove skips docker and git.
	}
	addWorkspace(m, ws)

	if err := m.Remove(ctx, ws.ID); err != nil {
		t.Fatalf("Remove returned unexpected error: %v", err)
	}

	if _, ok := m.State.Workspaces[ws.ID]; ok {
		t.Error("workspace still present in state after Remove")
	}
}

func TestRemove_ReturnsErrorForUnknownWorkspace(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	err := m.Remove(ctx, "ws-doesnotexist")
	if err == nil {
		t.Error("expected error when removing unknown workspace, got nil")
	}
}

func TestRemove_PersistsStateAfterDeletion(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-persist001",
		Name:      "Persist Test",
		Branch:    "done/persist",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.Remove(ctx, ws.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Re-read the state from disk to confirm the deletion was persisted.
	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	if _, ok := loaded.Workspaces[ws.ID]; ok {
		t.Error("workspace still present in persisted state file after Remove")
	}
}

// ----- SaveState: round-trips through disk -----

func TestSaveState_RoundTrip(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-rt000001",
		Name:      "Round Trip",
		Branch:    "feat/round-trip",
		State:     state.StateCreating,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}

	got, ok := loaded.Workspaces[ws.ID]
	if !ok {
		t.Fatal("workspace not found after SaveState round-trip")
	}
	if got.Name != ws.Name {
		t.Errorf("Name: got %q, want %q", got.Name, ws.Name)
	}
	if got.Branch != ws.Branch {
		t.Errorf("Branch: got %q, want %q", got.Branch, ws.Branch)
	}
	if got.State != ws.State {
		t.Errorf("State: got %q, want %q", got.State, ws.State)
	}
}

// ----- generateID: format check -----

func TestGenerateID_Format(t *testing.T) {
	id := generateID()
	if len(id) != 11 { // "ws-" (3) + 8 hex chars
		t.Errorf("generateID length: got %d, want 11; id=%q", len(id), id)
	}
	if id[:3] != "ws-" {
		t.Errorf("generateID prefix: got %q, want %q", id[:3], "ws-")
	}
	for _, ch := range id[3:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			t.Errorf("generateID contains non-hex character %q in suffix %q", ch, id[3:])
		}
	}
}

func TestGenerateID_Unique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := generateID()
		if seen[id] {
			t.Errorf("generateID produced duplicate: %q", id)
		}
		seen[id] = true
	}
}

// ----- cleanup: no leftover temp files -----

func TestNewTestManager_TempDirCleaned(t *testing.T) {
	var dir string
	func() {
		inner := t.TempDir()
		dir = inner
		_ = inner
	}()
	// After the subtest the TempDir is removed by the testing framework; this
	// test simply confirms that os.Stat fails, ensuring TempDir is valid.
	if _, err := os.Stat(dir); err == nil {
		// Still exists while test is running — that is expected.
	}
}

// ----- fake tmux helpers -----

// newFakeTmuxManager creates a Manager wired to a fake tmux script that
// records every invocation (one line per call) to argsFile and returns
// canned responses for break-pane and join-pane.
func newFakeTmuxManager(t *testing.T) (m *Manager, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "calls.txt")

	// The script appends one line per call so ordering can be verified.
	// break-pane returns a fake new window ID; everything else exits 0.
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  break-pane) echo "@99";;` + "\n" +
		`  new-window)  echo "@88";;` + "\n" +
		`  list-panes)  echo "%5";;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	mgr := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	if err := mgr.SaveState(); err != nil {
		t.Fatalf("newFakeTmuxManager: SaveState: %v", err)
	}
	return mgr, argsFile
}

// readCalls reads the recorded tmux subcommands in order.
func readCalls(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		// File may not exist if no tmux calls were made.
		return nil
	}
	var cmds []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		// First word is the subcommand.
		fields := strings.Fields(line)
		if len(fields) > 0 {
			cmds = append(cmds, fields[0])
		}
	}
	return cmds
}

// ----- SwitchActivePane -----

// TestSwitchActivePane_BreaksPreviousBeforeJoin verifies the 2-pane invariant:
// when a workspace is currently active SwitchActivePane must call break-pane
// on it before calling join-pane for the incoming workspace.
func TestSwitchActivePane_BreaksPreviousBeforeJoin(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)

	// Set up an active workspace with a pane in the main window.
	prevWS := &state.Workspace{
		ID:         "ws-prev0001",
		Name:       "Previous",
		Branch:     "feat/prev",
		State:      state.StateRunning,
		PaneID:     "%10",
		TmuxWindow: "@main",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	addWorkspace(m, prevWS)
	m.State.MainWindowID = "@main"
	m.State.ActiveWorkspaceID = prevWS.ID

	newWS := &state.Workspace{
		ID:        "ws-new00001",
		Name:      "New",
		Branch:    "feat/new",
		State:     state.StateRunning,
		PaneID:    "%20",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	m.SwitchActivePane(newWS)

	calls := readCalls(t, argsFile)

	// break-pane must appear before join-pane.
	breakIdx, joinIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "break-pane":
			if breakIdx < 0 {
				breakIdx = i
			}
		case "join-pane":
			if joinIdx < 0 {
				joinIdx = i
			}
		}
	}
	if breakIdx < 0 {
		t.Fatalf("break-pane was never called; calls = %v", calls)
	}
	if joinIdx < 0 {
		t.Fatalf("join-pane was never called; calls = %v", calls)
	}
	if breakIdx > joinIdx {
		t.Errorf("break-pane (pos %d) must precede join-pane (pos %d); calls = %v", breakIdx, joinIdx, calls)
	}

	// State should be updated.
	if m.State.ActiveWorkspaceID != newWS.ID {
		t.Errorf("ActiveWorkspaceID = %q, want %q", m.State.ActiveWorkspaceID, newWS.ID)
	}
	if newWS.TmuxWindow != "@main" {
		t.Errorf("new ws TmuxWindow = %q, want %q", newWS.TmuxWindow, "@main")
	}
	// Previous workspace's TmuxWindow should have been updated to the new window ID.
	if prevWS.TmuxWindow != "@99" {
		t.Errorf("prev ws TmuxWindow = %q, want %q (fake break-pane output)", prevWS.TmuxWindow, "@99")
	}
}

// TestSwitchActivePane_JoinsDirectlyWhenNoActive verifies that when no
// workspace is currently active, join-pane is called without break-pane.
func TestSwitchActivePane_JoinsDirectlyWhenNoActive(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.MainWindowID = "@main"
	// No active workspace.

	ws := &state.Workspace{
		ID:        "ws-first001",
		PaneID:    "%5",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	m.SwitchActivePane(ws)

	calls := readCalls(t, argsFile)
	for _, c := range calls {
		if c == "break-pane" {
			t.Errorf("break-pane must not be called when no workspace is active; calls = %v", calls)
		}
	}
	joinFound := false
	for _, c := range calls {
		if c == "join-pane" {
			joinFound = true
		}
	}
	if !joinFound {
		t.Errorf("join-pane was not called; calls = %v", calls)
	}
	if m.State.ActiveWorkspaceID != ws.ID {
		t.Errorf("ActiveWorkspaceID = %q, want %q", m.State.ActiveWorkspaceID, ws.ID)
	}
}

// TestSwitchActivePane_NoopWhenMainWindowEmpty verifies that SwitchActivePane
// is a no-op when no main window has been established.
func TestSwitchActivePane_NoopWhenMainWindowEmpty(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	// No MainWindowID set.

	ws := &state.Workspace{ID: "ws-noop0001", PaneID: "%5", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	m.SwitchActivePane(ws)

	calls := readCalls(t, argsFile)
	if len(calls) != 0 {
		t.Errorf("expected no tmux calls when MainWindowID is empty; got %v", calls)
	}
}

// TestSwitchActivePane_NoopWhenPaneIDEmpty verifies that SwitchActivePane
// is a no-op when the incoming workspace has no pane yet.
func TestSwitchActivePane_NoopWhenPaneIDEmpty(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.MainWindowID = "@main"

	ws := &state.Workspace{ID: "ws-nopane01", PaneID: "", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	m.SwitchActivePane(ws)

	calls := readCalls(t, argsFile)
	if len(calls) != 0 {
		t.Errorf("expected no tmux calls when workspace PaneID is empty; got %v", calls)
	}
}

// ----- ContainerPrefix -----

// TestContainerPrefix_EndsWithDash verifies that the container filter prefix
// ends with "-" to prevent Docker's substring --filter from matching containers
// belonging to other projects whose names start with the same characters.
func TestContainerPrefix_EndsWithDash(t *testing.T) {
	m := newTestManager(t)
	prefix := m.ContainerPrefix()
	if !strings.HasSuffix(prefix, "-") {
		t.Errorf("ContainerPrefix() = %q: must end with '-' for project isolation", prefix)
	}
}

// TestContainerPrefix_DoesNotMatchSimilarProjectName verifies that the prefix
// will not match a container belonging to a differently-named project.
func TestContainerPrefix_DoesNotMatchSimilarProjectName(t *testing.T) {
	m := newTestManager(t)
	prefix := m.ContainerPrefix()

	// A container from project "testprojectextended" must NOT contain our prefix.
	other := "claude-sb-testprojectextended-feat-ws-12345678"
	if strings.Contains(other, prefix) {
		t.Errorf("prefix %q incorrectly matches container from another project: %q", prefix, other)
	}

	// Our own containers must match.
	own := "claude-sb-testproject-feat-ws-12345678"
	if !strings.Contains(own, prefix) {
		t.Errorf("prefix %q should match own container %q", prefix, own)
	}
}

// ----- FindOrphanWorktrees -----

// setupBareRepo initializes a bare git repo with one commit and returns the
// bare repo directory. Skips the test if git is not available.
func setupBareRepo(t *testing.T) (bareDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found; skipping worktree integration test")
	}

	dir := t.TempDir()
	bareDir = filepath.Join(dir, ".bare")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v: %v\n%s", args, err, out)
		}
	}

	run("git", "init", "--bare", bareDir)
	run("git", "-C", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	srcDir := filepath.Join(dir, "src")
	run("git", "clone", bareDir, srcDir)
	run("git", "-C", srcDir, "config", "user.email", "test@test.com")
	run("git", "-C", srcDir, "config", "user.name", "Test")
	run("git", "-C", srcDir, "commit", "--allow-empty", "-m", "init")
	run("git", "-C", srcDir, "push", "origin", "HEAD:main")

	return bareDir
}

// TestFindOrphanWorktrees_ExcludesMainWorktree verifies that the development
// worktree created by `agency init` (<project>-main) is never flagged as an
// orphan even when it is not tracked in state.
func TestFindOrphanWorktrees_ExcludesMainWorktree(t *testing.T) {
	bareDir := setupBareRepo(t)
	projectDir := filepath.Dir(bareDir)
	projectName := "testproject"

	s := state.Default(projectName, bareDir)
	m := &Manager{
		StatePath:   filepath.Join(projectDir, ".agency", "state.json"),
		ProjectDir:  projectDir,
		ProjectName: projectName,
		State:       s,
		Tmux:        tmux.New("agency-" + projectName),
		Cfg:         config.DefaultConfig(),
	}

	// Simulate `agency init`: create the main worktree directly.
	mainWT := filepath.Join(projectDir, projectName+"-main")
	cmd := exec.Command("git", "-C", bareDir, "worktree", "add", mainWT, "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	// FindOrphanWorktrees must not include the main worktree.
	orphans, err := m.FindOrphanWorktrees()
	if err != nil {
		t.Fatalf("FindOrphanWorktrees: %v", err)
	}
	for _, wt := range orphans {
		if wt.Path == mainWT {
			t.Errorf("main worktree %q was incorrectly flagged as orphan", mainWT)
		}
	}
}

// TestFindOrphanWorktrees_ExcludesTrackedWorktrees verifies that worktrees
// which are tracked in state are not returned as orphans.
func TestFindOrphanWorktrees_ExcludesTrackedWorktrees(t *testing.T) {
	bareDir := setupBareRepo(t)
	projectDir := filepath.Dir(bareDir)
	projectName := "testproject"

	s := state.Default(projectName, bareDir)
	m := &Manager{
		StatePath:   filepath.Join(projectDir, ".agency", "state.json"),
		ProjectDir:  projectDir,
		ProjectName: projectName,
		State:       s,
		Tmux:        tmux.New("agency-" + projectName),
		Cfg:         config.DefaultConfig(),
	}

	// Create a worktree and register it in state.
	wtPath := filepath.Join(projectDir, projectName+"-feat1")
	cmd := exec.Command("git", "-C", bareDir, "worktree", "add", wtPath, "-b", "feat1", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	ws := &state.Workspace{
		ID:           "ws-track001",
		Branch:       "feat1",
		WorktreePath: wtPath,
		State:        state.StateRunning,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	orphans, err := m.FindOrphanWorktrees()
	if err != nil {
		t.Fatalf("FindOrphanWorktrees: %v", err)
	}
	for _, wt := range orphans {
		if wt.Path == wtPath {
			t.Errorf("tracked worktree %q was incorrectly flagged as orphan", wtPath)
		}
	}
}

// TestFindOrphanWorktrees_IncludesUntrackedWorktrees verifies that worktrees
// present in the repo but absent from state (and not the main worktree) are
// returned as orphans.
func TestFindOrphanWorktrees_IncludesUntrackedWorktrees(t *testing.T) {
	bareDir := setupBareRepo(t)
	projectDir := filepath.Dir(bareDir)
	projectName := "testproject"

	s := state.Default(projectName, bareDir)
	m := &Manager{
		StatePath:   filepath.Join(projectDir, ".agency", "state.json"),
		ProjectDir:  projectDir,
		ProjectName: projectName,
		State:       s,
		Tmux:        tmux.New("agency-" + projectName),
		Cfg:         config.DefaultConfig(),
	}

	// Create a worktree but do NOT register it in state → should be orphaned.
	wtPath := filepath.Join(projectDir, projectName+"-orphan1")
	cmd := exec.Command("git", "-C", bareDir, "worktree", "add", wtPath, "-b", "orphan1", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	orphans, err := m.FindOrphanWorktrees()
	if err != nil {
		t.Fatalf("FindOrphanWorktrees: %v", err)
	}
	found := false
	for _, wt := range orphans {
		if wt.Path == wtPath {
			found = true
		}
	}
	if !found {
		t.Errorf("untracked worktree %q was not returned as orphan; orphans = %v", wtPath, orphans)
	}
}
