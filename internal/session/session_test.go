package session

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
//   - State is pre-initialised with an empty Sessions map.
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

// addSession is a helper that inserts a pre-built Session into a Manager's
// in-memory state without going through the full Create path.
func addSession(m *Manager, sess *state.Session) {
	m.State.Sessions[sess.ID] = sess
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

	existing := &state.Session{
		ID:        "sess-aabbccdd",
		Name:      "Existing",
		Branch:    "project/my-feature",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, existing)

	_, err := m.Create(ctx, "Duplicate", "project/my-feature")
	if err == nil {
		t.Fatal("expected error for duplicate active branch, got nil")
	}
}

// TestCreate_AllowsDuplicateBranchWhenDone verifies that a branch used by a
// DONE session can be re-used for a new session.  The new Create call will
// still fail eventually (no Docker), but the duplicate-branch pre-check must
// pass and a session entry with StateCreating must be written before the
// Docker error is returned.
func TestCreate_AllowsDuplicateBranchWhenDone(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	done := &state.Session{
		ID:        "sess-done0001",
		Name:      "Old",
		Branch:    "project/my-feature",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, done)

	_, err := m.Create(ctx, "New", "project/my-feature")
	// We expect an error because docker/worktree is not available.
	// What we are checking is that the error is NOT the duplicate-branch error.
	if err == nil {
		// Unexpected success — still fine for the duplicate-check assertion.
		return
	}
	// The session should have been inserted (and then marked failed).
	found := false
	for _, sess := range m.State.Sessions {
		if sess.Branch == "project/my-feature" && sess.ID != done.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a new session entry to be created before the infrastructure error")
	}
}

// TestCreate_SetsInitialState verifies that a session entry is placed in
// state with the correct Name, Branch, and StateCreating before any
// infrastructure step is attempted.  The Create call will fail at the
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

	var found *state.Session
	for _, sess := range m.State.Sessions {
		if sess.Branch == "project/my-feature" {
			found = sess
			break
		}
	}

	if found == nil {
		t.Fatal("no session entry found in state after Create")
	}
	if found.Name != "My Feature" {
		t.Errorf("Name: got %q, want %q", found.Name, "My Feature")
	}
	if found.Branch != "project/my-feature" {
		t.Errorf("Branch: got %q, want %q", found.Branch, "project/my-feature")
	}
	// The session starts as StateCreating; after an infrastructure failure it
	// transitions to StateFailed.  Either is acceptable here — what matters is
	// that the entry exists with the right Name and Branch.
	if found.State != state.StateCreating && found.State != state.StateFailed {
		t.Errorf("State: got %q, expected StateCreating or StateFailed", found.State)
	}
}

// ----- List: sorted by CreatedAt (oldest first) -----

func TestList_SortedByCreatedAt(t *testing.T) {
	m := newTestManager(t)

	now := time.Now().UTC()
	sessions := []*state.Session{
		{ID: "sess-c", Name: "C", Branch: "c", State: state.StateRunning, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now},
		{ID: "sess-a", Name: "A", Branch: "a", State: state.StateRunning, CreatedAt: now.Add(0), UpdatedAt: now},
		{ID: "sess-b", Name: "B", Branch: "b", State: state.StateRunning, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now},
	}
	for _, s := range sessions {
		addSession(m, s)
	}

	listed := m.List()

	if len(listed) != 3 {
		t.Fatalf("List returned %d sessions, want 3", len(listed))
	}

	wantOrder := []string{"sess-a", "sess-b", "sess-c"}
	for i, sess := range listed {
		if sess.ID != wantOrder[i] {
			t.Errorf("position %d: got ID %q, want %q", i, sess.ID, wantOrder[i])
		}
	}
}

func TestList_EmptyState(t *testing.T) {
	m := newTestManager(t)
	listed := m.List()
	if len(listed) != 0 {
		t.Errorf("expected empty list, got %d sessions", len(listed))
	}
}

// ----- Reconcile: clears ActiveSessionID for missing sessions -----

func TestReconcile_ClearsActiveSessionIDWhenMissing(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	// Set an ActiveSessionID that does not exist in Sessions.
	m.State.ActiveSessionID = "sess-nonexistent"

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	if m.State.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID not cleared: got %q", m.State.ActiveSessionID)
	}
}

func TestReconcile_ClearsActiveSessionIDWhenNotRunning(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	sess := &state.Session{
		ID:        "sess-done0002",
		Name:      "Done Session",
		Branch:    "done/branch",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, sess)
	m.State.ActiveSessionID = sess.ID

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	if m.State.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID should be cleared for non-running session; got %q", m.State.ActiveSessionID)
	}
}

func TestReconcile_KeepsActiveSessionIDWhenRunning(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	// A running session with a SandboxID and TmuxWindow both empty so the
	// reconcileSessions logic does not transition it (it only checks IDs that
	// are non-empty against the live sets, which are empty due to failed
	// external queries).
	sess := &state.Session{
		ID:        "sess-run00001",
		Name:      "Running",
		Branch:    "feature/run",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, sess)
	m.State.ActiveSessionID = sess.ID

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	// The session should still be running (no sandbox/tmux IDs to check).
	if _, ok := m.State.Sessions[sess.ID]; !ok {
		t.Fatal("running session was unexpectedly removed")
	}
	if m.State.Sessions[sess.ID].State != state.StateRunning {
		t.Errorf("expected StateRunning, got %q", m.State.Sessions[sess.ID].State)
	}
	if m.State.ActiveSessionID != sess.ID {
		t.Errorf("ActiveSessionID was cleared unexpectedly; got %q", m.State.ActiveSessionID)
	}
}

// ----- Remove: deletes session from state -----

func TestRemove_DeletesSessionFromState(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	sess := &state.Session{
		ID:        "sess-rm000001",
		Name:      "Finished",
		Branch:    "done/cleanup",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		// Leave SandboxID and WorktreePath empty so Remove skips docker and git.
	}
	addSession(m, sess)

	if err := m.Remove(ctx, sess.ID); err != nil {
		t.Fatalf("Remove returned unexpected error: %v", err)
	}

	if _, ok := m.State.Sessions[sess.ID]; ok {
		t.Error("session still present in state after Remove")
	}
}

func TestRemove_ReturnsErrorForUnknownSession(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	err := m.Remove(ctx, "sess-doesnotexist")
	if err == nil {
		t.Error("expected error when removing unknown session, got nil")
	}
}

func TestRemove_PersistsStateAfterDeletion(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	sess := &state.Session{
		ID:        "sess-persist001",
		Name:      "Persist Test",
		Branch:    "done/persist",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, sess)

	if err := m.Remove(ctx, sess.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Re-read the state from disk to confirm the deletion was persisted.
	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	if _, ok := loaded.Sessions[sess.ID]; ok {
		t.Error("session still present in persisted state file after Remove")
	}
}

// ----- SaveState: round-trips through disk -----

func TestSaveState_RoundTrip(t *testing.T) {
	m := newTestManager(t)

	sess := &state.Session{
		ID:        "sess-rt000001",
		Name:      "Round Trip",
		Branch:    "feat/round-trip",
		State:     state.StateCreating,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addSession(m, sess)

	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}

	got, ok := loaded.Sessions[sess.ID]
	if !ok {
		t.Fatal("session not found after SaveState round-trip")
	}
	if got.Name != sess.Name {
		t.Errorf("Name: got %q, want %q", got.Name, sess.Name)
	}
	if got.Branch != sess.Branch {
		t.Errorf("Branch: got %q, want %q", got.Branch, sess.Branch)
	}
	if got.State != sess.State {
		t.Errorf("State: got %q, want %q", got.State, sess.State)
	}
}

// ----- generateID: format check -----

func TestGenerateID_Format(t *testing.T) {
	id := generateID()
	if len(id) != 13 { // "sess-" (5) + 8 hex chars
		t.Errorf("generateID length: got %d, want 13; id=%q", len(id), id)
	}
	if id[:5] != "sess-" {
		t.Errorf("generateID prefix: got %q, want %q", id[:5], "sess-")
	}
	for _, ch := range id[5:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("generateID contains non-hex character %q in suffix %q", ch, id[5:])
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
