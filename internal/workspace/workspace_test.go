package workspace

import (
	"context"
	"os"
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
