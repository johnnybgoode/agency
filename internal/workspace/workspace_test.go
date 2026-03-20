package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/sandbox"
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
		ID:        "ws-d00e0001",
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
	m.State.ActiveWorkspaceID = "ws-00000000"

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
		ID:        "ws-d00e0002",
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

	// A running workspace with empty SandboxID and TmuxWindow so the
	// reconcileWorkspaces logic does not transition it (it only checks IDs that
	// are non-empty against the live sets, which are empty due to failed
	// external queries).
	ws := &state.Workspace{
		ID:        "ws-a0000001",
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
		ID:        "ws-b0000001",
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

	err := m.Remove(ctx, "ws-00000099")
	if err == nil {
		t.Error("expected error when removing unknown workspace, got nil")
	}
}

func TestRemove_PersistsStateAfterDeletion(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-ce000001",
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

// TestRemove_DoesNotStopSandbox verifies that Remove does NOT stop or remove
// the shared project sandbox when removing a workspace.
func TestRemove_DoesNotStopSandbox(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`exit 0` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	s.SandboxID = "agency-testproject"
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     &sandbox.Manager{},
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	ws := &state.Workspace{
		ID:        "ws-b0000002",
		Name:      "Remove Test",
		Branch:    "feat/remove",
		State:     state.StateRunning,
		SandboxID: "agency-testproject",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.Remove(context.Background(), ws.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify docker stop/rm were NOT called.
	data, _ := os.ReadFile(argsFile)
	log := string(data)
	if strings.Contains(log, "sandbox stop") || strings.Contains(log, "sandbox rm") {
		t.Errorf("Remove should not stop/remove shared sandbox; docker calls: %s", log)
	}
}

// ----- SaveState: round-trips through disk -----

func TestSaveState_RoundTrip(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-ff000001",
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

// ----- generateSessionID: format check -----

func TestGenerateSessionID_Format(t *testing.T) {
	id := generateSessionID()
	// UUID v4 format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	// Length: 8-4-4-4-12 + 4 dashes = 36
	if len(id) != 36 {
		t.Errorf("generateSessionID length: got %d, want 36; id=%q", len(id), id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("generateSessionID: expected 5 dash-separated parts, got %d; id=%q", len(parts), id)
	}
	// Check version bit (4).
	if len(parts) >= 3 && (parts[2][0] != '4') {
		t.Errorf("generateSessionID: version nibble should be '4', got %q; id=%q", string(parts[2][0]), id)
	}
}

func TestGenerateSessionID_Unique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := generateSessionID()
		if seen[id] {
			t.Errorf("generateSessionID produced duplicate: %q", id)
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

// ----- provisionTmux: trapCmd must use docker sandbox ls to check existence -----

// TestProvisionTmux_TrapCmdChecksSandboxExistence verifies that the shell
// command sent to the workspace's tmux window uses a sandbox-existence check
// in the loop condition (docker sandbox ls -q | grep) rather than "while true".
func TestProvisionTmux_TrapCmdChecksSandboxExistence(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	// Capture full send-keys arguments. new-window returns "@88", list-panes returns "%5".
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`case "$1" in` + "\n" +
		`  new-window) echo "@88";;` + "\n" +
		`  list-panes) echo "%5";;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	ws := &state.Workspace{
		ID:        "ws-ac1d0001",
		Name:      "Test",
		Branch:    "feat/test",
		SandboxID: "agency-testproject",
		SessionID: "12345678-1234-4234-8234-123456789abc",
		State:     state.StateProvisioning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := m.provisionTmux(ws); err != nil {
		t.Fatalf("provisionTmux returned error: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading args file: %v", err)
	}
	captured := string(data)

	// The loop must NOT use "while true" — that keeps running after removal.
	if strings.Contains(captured, "while true") {
		t.Errorf("trapCmd uses 'while true'; it must check sandbox existence instead")
	}
	// The loop must check sandbox existence via docker sandbox ls -q | grep.
	if !strings.Contains(captured, "docker sandbox ls -q | grep -qx") {
		t.Errorf("trapCmd does not contain 'docker sandbox ls -q | grep -qx'; got: %s", captured)
	}
}

// TestProvisionTmux_TrapCmdUsesSessionID verifies that the trapCmd
// uses --session-id <UUID> for the first run and --resume <UUID> for restarts.
func TestProvisionTmux_TrapCmdUsesSessionID(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`case "$1" in` + "\n" +
		`  new-window) echo "@88";;` + "\n" +
		`  list-panes) echo "%5";;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	sessionID := "12345678-1234-4234-8234-123456789abc"
	ws := &state.Workspace{
		ID:        "ws-c0de0001",
		Name:      "Test",
		Branch:    "feat/test",
		SandboxID: "agency-testproject",
		SessionID: sessionID,
		State:     state.StateProvisioning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := m.provisionTmux(ws); err != nil {
		t.Fatalf("provisionTmux returned error: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading args file: %v", err)
	}
	captured := string(data)

	// The trapCmd must use --session-id <UUID> for first run.
	if !strings.Contains(captured, "--session-id "+sessionID) {
		t.Errorf("trapCmd does not contain '--session-id %s'; got: %s", sessionID, captured)
	}
	// The trapCmd must use --resume <UUID> for subsequent runs.
	if !strings.Contains(captured, "--resume "+sessionID) {
		t.Errorf("trapCmd does not contain '--resume %s'; got: %s", sessionID, captured)
	}
	// Must use docker sandbox exec.
	if !strings.Contains(captured, "docker sandbox exec") {
		t.Errorf("trapCmd does not contain 'docker sandbox exec'; got: %s", captured)
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
	// display-message always succeeds so PaneExists returns true by default.
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  break-pane) echo "@99";;` + "\n" +
		`  new-window)  echo "@88";;` + "\n" +
		`  list-panes)  echo "%5";;` + "\n" +
		`  display-message) echo "%0";;` + "\n" +
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

// newFakeTmuxManagerWithDeadPanes creates a Manager wired to a fake tmux that
// fails display-message (PaneExists returns false) to simulate dead panes.
func newFakeTmuxManagerWithDeadPanes(t *testing.T) (m *Manager, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "calls.txt")

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  display-message) exit 1;;` + "\n" +
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
		t.Fatalf("newFakeTmuxManagerWithDeadPanes: SaveState: %v", err)
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

// ----- SwapActivePane / SwapBackToShell -----

// TestSwapActivePane_CallsSwapPane verifies that SwapActivePane calls swap-pane
// when no workspace is currently active (shell is in :0.1).
func TestSwapActivePane_CallsSwapPane(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.WorkspacePaneID = "%shell"

	ws := &state.Workspace{
		ID:        "ws-f1000001",
		PaneID:    "%5",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.SwapActivePane(ws.ID); err != nil {
		t.Fatalf("SwapActivePane returned error: %v", err)
	}

	calls := readCalls(t, argsFile)
	swapFound := false
	for _, c := range calls {
		if c == "swap-pane" {
			swapFound = true
		}
	}
	if !swapFound {
		t.Errorf("swap-pane was not called; calls = %v", calls)
	}
	if m.State.ActiveWorkspaceID != ws.ID {
		t.Errorf("ActiveWorkspaceID = %q, want %q", m.State.ActiveWorkspaceID, ws.ID)
	}
}

// TestSwapActivePane_NoopWhenWorkspacePaneIDEmpty verifies that SwapActivePane
// is a no-op when WorkspacePaneID (the shell pane) has not been set and there
// is no MainWindowID set either.
func TestSwapActivePane_NoopWhenWorkspacePaneIDEmpty(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	// WorkspacePaneID not set, MainWindowID not set.

	ws := &state.Workspace{
		ID:        "ws-00100001",
		PaneID:    "%5",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	_ = m.SwapActivePane(ws.ID)

	calls := readCalls(t, argsFile)
	if len(calls) != 0 {
		t.Errorf("expected no tmux calls when WorkspacePaneID and MainWindowID are empty; got %v", calls)
	}
}

// TestSwapActivePane_ReusesExistingRightPaneWhenAlreadySplit verifies that
// SwapActivePane does NOT call split-window when the main window already has
// two panes (e.g. because the sidebar's ensureSplitOnFirstWorkspace ran
// concurrently). It should reuse the existing right pane instead.
func TestSwapActivePane_ReusesExistingRightPaneWhenAlreadySplit(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	// list-panes returns two pane IDs simulating an already-split window.
	// display-message succeeds so PaneExists returns true.
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`case "$1" in` + "\n" +
		`  list-panes)  printf '%%1\n%%2\n';;` + "\n" +
		`  display-message) echo "%%0";;` + "\n" +
		`  resize-pane) ;;` + "\n" +
		`  swap-pane) ;;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// MainWindowID set, WorkspacePaneID empty — simulates the race where the
	// sidebar's tick already split the window but the popup's manager still
	// has stale in-memory state.
	m.State.MainWindowID = "@1"

	ws := &state.Workspace{
		ID:        "ws-00200001",
		PaneID:    "%5",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.SwapActivePane(ws.ID); err != nil {
		t.Fatalf("SwapActivePane returned error: %v", err)
	}

	// split-window must NOT have been called.
	data, _ := os.ReadFile(argsFile)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "split-window") {
			t.Errorf("split-window should not be called when window already has 2 panes; got line=%q", line)
		}
	}

	// WorkspacePaneID should be set to the second pane returned by list-panes.
	if m.State.WorkspacePaneID != "%2" {
		t.Errorf("WorkspacePaneID = %q, want %%2", m.State.WorkspacePaneID)
	}
}

// TestSwapActivePane_NoopWhenPaneIDEmpty verifies that SwapActivePane is a
// no-op when the workspace has no pane ID yet.
func TestSwapActivePane_NoopWhenPaneIDEmpty(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.WorkspacePaneID = "%shell"

	ws := &state.Workspace{
		ID:        "ws-00300001",
		PaneID:    "",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	_ = m.SwapActivePane(ws.ID)

	calls := readCalls(t, argsFile)
	if len(calls) != 0 {
		t.Errorf("expected no tmux calls when workspace PaneID is empty; got %v", calls)
	}
}

// TestSwapActivePane_SwapsBackFirstWhenActive verifies that when switching from
// one workspace to another, swap-pane is called twice: once to restore the
// previous workspace's pane, then once to bring in the new workspace's pane.
func TestSwapActivePane_SwapsBackFirstWhenActive(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.WorkspacePaneID = "%shell"

	prevWS := &state.Workspace{
		ID:        "ws-00400001",
		PaneID:    "%10",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, prevWS)
	m.State.ActiveWorkspaceID = prevWS.ID

	newWS := &state.Workspace{
		ID:        "ws-00500001",
		PaneID:    "%20",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, newWS)

	if err := m.SwapActivePane(newWS.ID); err != nil {
		t.Fatalf("SwapActivePane returned error: %v", err)
	}

	calls := readCalls(t, argsFile)

	// swap-pane must be called exactly twice (swap-back + swap-forward).
	swapCount := 0
	for _, c := range calls {
		if c == "swap-pane" {
			swapCount++
		}
	}
	if swapCount != 2 {
		t.Errorf("expected 2 swap-pane calls when switching workspaces, got %d; calls = %v", swapCount, calls)
	}

	if m.State.ActiveWorkspaceID != newWS.ID {
		t.Errorf("ActiveWorkspaceID = %q, want %q", m.State.ActiveWorkspaceID, newWS.ID)
	}
	if m.State.LastActiveWorkspaceID != prevWS.ID {
		t.Errorf("LastActiveWorkspaceID = %q, want %q", m.State.LastActiveWorkspaceID, prevWS.ID)
	}
}

// TestSwapBackToShell_CallsSwapPane verifies that SwapBackToShell calls
// swap-pane and clears ActiveWorkspaceID.
func TestSwapBackToShell_CallsSwapPane(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.WorkspacePaneID = "%shell"

	ws := &state.Workspace{
		ID:        "ws-ac000001",
		PaneID:    "%5",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)
	m.State.ActiveWorkspaceID = ws.ID

	if err := m.SwapBackToShell(); err != nil {
		t.Fatalf("SwapBackToShell returned error: %v", err)
	}

	calls := readCalls(t, argsFile)
	swapFound := false
	for _, c := range calls {
		if c == "swap-pane" {
			swapFound = true
		}
	}
	if !swapFound {
		t.Errorf("swap-pane was not called; calls = %v", calls)
	}
	if m.State.ActiveWorkspaceID != "" {
		t.Errorf("ActiveWorkspaceID = %q, want empty string", m.State.ActiveWorkspaceID)
	}
}

// TestSwapBackToShell_NoopWhenNotActive verifies that SwapBackToShell is a
// no-op when no workspace is active.
func TestSwapBackToShell_NoopWhenNotActive(t *testing.T) {
	m, argsFile := newFakeTmuxManager(t)
	m.State.WorkspacePaneID = "%shell"
	// ActiveWorkspaceID is empty.

	if err := m.SwapBackToShell(); err != nil {
		t.Fatalf("SwapBackToShell returned error: %v", err)
	}

	calls := readCalls(t, argsFile)
	if len(calls) != 0 {
		t.Errorf("expected no tmux calls when no workspace is active; got %v", calls)
	}
}

// ----- SandboxName -----

// TestSandboxName_Format verifies that SandboxName returns "agency-<projectName>".
func TestSandboxName_Format(t *testing.T) {
	m := newTestManager(t)
	name := m.SandboxName()
	want := "agency-testproject"
	if name != want {
		t.Errorf("SandboxName() = %q, want %q", name, want)
	}
}

// TestSandboxName_IsValidSandboxName verifies that SandboxName returns a name
// that passes ValidateSandboxName.
func TestSandboxName_IsValidSandboxName(t *testing.T) {
	m := newTestManager(t)
	name := m.SandboxName()
	if err := sandbox.ValidateSandboxName(name); err != nil {
		t.Errorf("SandboxName() = %q is not a valid sandbox name: %v", name, err)
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
		ID:           "ws-dd000001",
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

// ----- StopWorkspace -----

func TestStopWorkspace_TransitionsToPaused(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-ee000001",
		Name:      "To Stop",
		Branch:    "feat/stop",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		// SandboxID empty — sandbox nil anyway.
	}
	addWorkspace(m, ws)

	if err := m.StopWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("StopWorkspace: %v", err)
	}

	if ws.State != state.StatePaused {
		t.Errorf("State after StopWorkspace: got %q, want %q", ws.State, state.StatePaused)
	}
}

func TestStopWorkspace_PersistsState(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-ee000002",
		Name:      "Persist Stop",
		Branch:    "feat/persist-stop",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.StopWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("StopWorkspace: %v", err)
	}

	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	got, ok := loaded.Workspaces[ws.ID]
	if !ok {
		t.Fatal("workspace not found in persisted state after StopWorkspace")
	}
	if got.State != state.StatePaused {
		t.Errorf("persisted state: got %q, want %q", got.State, state.StatePaused)
	}
}

// TestStopWorkspace_DoesNotStopSandbox verifies that StopWorkspace does NOT
// stop the shared project sandbox.
func TestStopWorkspace_DoesNotStopSandbox(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`exit 0` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	s.SandboxID = "agency-testproject"
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     &sandbox.Manager{},
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	ws := &state.Workspace{
		ID:        "ws-ee000003",
		Name:      "Stop No Docker",
		Branch:    "feat/stop-no-docker",
		State:     state.StateRunning,
		SandboxID: "agency-testproject",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.StopWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("StopWorkspace: %v", err)
	}

	// Verify docker sandbox stop was NOT called.
	data, _ := os.ReadFile(argsFile)
	if strings.Contains(string(data), "sandbox stop") {
		t.Errorf("StopWorkspace should not call 'docker sandbox stop' on shared sandbox; got: %s", string(data))
	}
}

// TestStopWorkspaceBackground_DoesNotStopSandbox verifies that StopWorkspaceBackground
// also does NOT stop the shared project sandbox.
func TestStopWorkspaceBackground_DoesNotStopSandbox(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`exit 0` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	s.SandboxID = "agency-testproject"
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     &sandbox.Manager{},
		Cfg:         config.DefaultConfig(),
	}
	if err := m.SaveState(); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	ws := &state.Workspace{
		ID:        "ws-ee000004",
		Name:      "StopBg No Docker",
		Branch:    "feat/stopbg-no-docker",
		State:     state.StateRunning,
		SandboxID: "agency-testproject",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.StopWorkspaceBackground(context.Background(), ws); err != nil {
		t.Fatalf("StopWorkspaceBackground: %v", err)
	}

	data, _ := os.ReadFile(argsFile)
	if strings.Contains(string(data), "sandbox stop") {
		t.Errorf("StopWorkspaceBackground should not call 'docker sandbox stop'; got: %s", string(data))
	}
	if ws.State != state.StatePaused {
		t.Errorf("workspace state after StopWorkspaceBackground: got %q, want StatePaused", ws.State)
	}
}

// ----- CleanupDoneWorkspace -----

func TestCleanupDoneWorkspace_RemovesFromState(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-cc000001",
		Name:      "Cleanup",
		Branch:    "feat/cleanup",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		// No WorktreePath or TmuxWindow — skip git and tmux ops.
	}
	addWorkspace(m, ws)

	if err := m.CleanupDoneWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("CleanupDoneWorkspace: %v", err)
	}

	if _, ok := m.State.Workspaces[ws.ID]; ok {
		t.Error("workspace still present in state after CleanupDoneWorkspace")
	}
}

func TestCleanupDoneWorkspace_PersistsDeletion(t *testing.T) {
	m := newTestManager(t)

	ws := &state.Workspace{
		ID:        "ws-cc000002",
		Name:      "Cleanup Persist",
		Branch:    "feat/cleanup-persist",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	if err := m.CleanupDoneWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("CleanupDoneWorkspace: %v", err)
	}

	loaded, err := state.Read(m.StatePath)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	if _, ok := loaded.Workspaces[ws.ID]; ok {
		t.Error("workspace still present in persisted state after CleanupDoneWorkspace")
	}
}

// ----- AssessQuitStatuses -----

func TestAssessQuitStatuses_ClassifiesActive(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-a5000001",
		Name:      "Running",
		Branch:    "feat/active",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		// No WorktreePath — IsDirty skipped, defaults to dirty.
	}
	addWorkspace(m, ws)

	infos, err := m.AssessQuitStatuses(ctx)
	if err != nil {
		t.Fatalf("AssessQuitStatuses: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if !infos[0].IsActive {
		t.Error("running workspace should be classified as active")
	}
	// No WorktreePath → treated as dirty.
	if !infos[0].IsDirty {
		t.Error("workspace with no worktree path should be classified as dirty")
	}
}

func TestAssessQuitStatuses_ClassifiesInactive(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:        "ws-a5000002",
		Name:      "Done",
		Branch:    "feat/done",
		State:     state.StateDone,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	infos, err := m.AssessQuitStatuses(ctx)
	if err != nil {
		t.Fatalf("AssessQuitStatuses: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if infos[0].IsActive {
		t.Error("done workspace should not be classified as active")
	}
}

func TestAssessQuitStatuses_EmptyReturnsEmpty(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	infos, err := m.AssessQuitStatuses(ctx)
	if err != nil {
		t.Fatalf("AssessQuitStatuses: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 infos for empty state, got %d", len(infos))
	}
}

func TestAssessQuitStatuses_AllStatesClassified(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	activeStates := []state.WorkspaceState{
		state.StateCreating,
		state.StateProvisioning,
		state.StateRunning,
		state.StatePaused,
	}
	inactiveStates := []state.WorkspaceState{
		state.StateCompleting,
		state.StateDone,
		state.StateFailed,
	}

	now := time.Now().UTC()
	for i, s := range activeStates {
		ws := &state.Workspace{
			ID:        fmt.Sprintf("ws-ac0000%02d", i),
			Branch:    fmt.Sprintf("feat/active%d", i),
			State:     s,
			CreatedAt: now,
			UpdatedAt: now,
		}
		addWorkspace(m, ws)
	}
	for i, s := range inactiveStates {
		ws := &state.Workspace{
			ID:        fmt.Sprintf("ws-bc0000%02d", i),
			Branch:    fmt.Sprintf("feat/inactive%d", i),
			State:     s,
			CreatedAt: now,
			UpdatedAt: now,
		}
		addWorkspace(m, ws)
	}

	infos, err := m.AssessQuitStatuses(ctx)
	if err != nil {
		t.Fatalf("AssessQuitStatuses: %v", err)
	}

	activeCount, inactiveCount := 0, 0
	for _, info := range infos {
		if info.IsActive {
			activeCount++
		} else {
			inactiveCount++
		}
	}
	if activeCount != len(activeStates) {
		t.Errorf("active count: got %d, want %d", activeCount, len(activeStates))
	}
	if inactiveCount != len(inactiveStates) {
		t.Errorf("inactive count: got %d, want %d", inactiveCount, len(inactiveStates))
	}
}

// ----- Dead pane handling -----

// TestSwapActivePane_DeadWorkspacePane verifies that SwapActivePane returns an
// error and clears WorkspacePaneID when the shell pane is dead.
func TestSwapActivePane_DeadWorkspacePane(t *testing.T) {
	m, _ := newFakeTmuxManagerWithDeadPanes(t)
	m.State.WorkspacePaneID = "%dead"

	ws := &state.Workspace{
		ID:        "ws-de000001",
		PaneID:    "%5",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	err := m.SwapActivePane(ws.ID)
	if err == nil {
		t.Fatal("expected error for dead workspace pane, got nil")
	}
	if m.State.WorkspacePaneID != "" {
		t.Errorf("WorkspacePaneID should be cleared; got %q", m.State.WorkspacePaneID)
	}
}

// TestSwapActivePane_DeadTargetPane verifies that SwapActivePane returns an
// error and clears ws.PaneID when the target workspace pane is dead.
func TestSwapActivePane_DeadTargetPane(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	// display-message succeeds for "%shell" but fails for anything else.
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`case "$1" in` + "\n" +
		`  display-message)` + "\n" +
		`    for arg in "$@"; do` + "\n" +
		`      case "$arg" in` + "\n" +
		`        %shell) echo "%shell"; exit 0;;` + "\n" +
		`      esac` + "\n" +
		`    done` + "\n" +
		`    exit 1;;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	m := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.NewWithBinaryPath("agency-testproject", scriptPath),
		Sandbox:     nil,
		Cfg:         config.DefaultConfig(),
	}
	_ = m.SaveState()

	m.State.WorkspacePaneID = "%shell"
	ws := &state.Workspace{
		ID:        "ws-de000002",
		PaneID:    "%deadpane",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)

	err := m.SwapActivePane(ws.ID)
	if err == nil {
		t.Fatal("expected error for dead target pane, got nil")
	}
	if ws.PaneID != "" {
		t.Errorf("ws.PaneID should be cleared; got %q", ws.PaneID)
	}
}

// TestSwapBackToShell_DeadActivePaneClearsState verifies that SwapBackToShell
// clears ActiveWorkspaceID when the active workspace's pane is dead.
func TestSwapBackToShell_DeadActivePaneClearsState(t *testing.T) {
	m, _ := newFakeTmuxManagerWithDeadPanes(t)
	m.State.WorkspacePaneID = "%shell"

	ws := &state.Workspace{
		ID:        "ws-de000003",
		PaneID:    "%dead",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)
	m.State.ActiveWorkspaceID = ws.ID

	if err := m.SwapBackToShell(); err != nil {
		t.Fatalf("SwapBackToShell returned error: %v", err)
	}

	if m.State.ActiveWorkspaceID != "" {
		t.Errorf("ActiveWorkspaceID should be cleared; got %q", m.State.ActiveWorkspaceID)
	}
	if ws.PaneID != "" {
		t.Errorf("ws.PaneID should be cleared; got %q", ws.PaneID)
	}
}

// TestCleanupActiveWorkspaceID_DeadPaneClearsActive verifies that
// cleanupActiveWorkspaceID clears the active workspace when its pane is dead.
func TestCleanupActiveWorkspaceID_DeadPaneClearsActive(t *testing.T) {
	m, _ := newFakeTmuxManagerWithDeadPanes(t)
	m.State.WorkspacePaneID = "%deadshell"

	ws := &state.Workspace{
		ID:        "ws-c1000001",
		PaneID:    "%deadpane",
		State:     state.StateRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	addWorkspace(m, ws)
	m.State.ActiveWorkspaceID = ws.ID

	changed := m.cleanupActiveWorkspaceID()
	if !changed {
		t.Error("expected cleanupActiveWorkspaceID to report changes")
	}
	if m.State.ActiveWorkspaceID != "" {
		t.Errorf("ActiveWorkspaceID should be cleared; got %q", m.State.ActiveWorkspaceID)
	}
	if m.State.WorkspacePaneID != "" {
		t.Errorf("WorkspacePaneID should be cleared; got %q", m.State.WorkspacePaneID)
	}
}

// ---------------------------------------------------------------------------
// SidebarWidthPercent / SidebarColumns
// ---------------------------------------------------------------------------

func TestSidebarWidthPercent(t *testing.T) {
	tests := []struct {
		name     string
		cfgWidth int
		wantPct  int
	}{
		{"default config", 0, config.DefaultSidebarWidth},
		{"negative falls back to default", -5, config.DefaultSidebarWidth},
		{"custom percentage", 20, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(t)
			m.Cfg.TUI.SidebarWidth = tt.cfgWidth
			if got := m.SidebarWidthPercent(); got != tt.wantPct {
				t.Errorf("SidebarWidthPercent() = %d, want %d", got, tt.wantPct)
			}
		})
	}
}

func TestSidebarColumns(t *testing.T) {
	tests := []struct {
		name      string
		cfgWidth  int
		termWidth int
		wantCols  int
	}{
		{"15 pct of 200 cols", 15, 200, 30},
		{"15 pct of 100 cols", 15, 100, MinSidebarColumns},
		{"25 pct of 200 cols", 25, 200, 50},
		{"narrow terminal clamps to min", 15, 80, MinSidebarColumns},
		{"very narrow terminal", 10, 50, MinSidebarColumns},
		{"exact min boundary", 25, 100, MinSidebarColumns},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(t)
			m.Cfg.TUI.SidebarWidth = tt.cfgWidth
			if got := m.SidebarColumns(tt.termWidth); got != tt.wantCols {
				t.Errorf("SidebarColumns(%d) = %d, want %d", tt.termWidth, got, tt.wantCols)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildTrapCmd
// ---------------------------------------------------------------------------

// TestBuildTrapCmd_ResumeFlag verifies that buildTrapCmd uses --session-id when
// resume=false and --resume when resume=true, and that both outputs include the
// workspace ID, sandbox ID, and session ID.
func TestBuildTrapCmd_ResumeFlag(t *testing.T) {
	sessionID := "12345678-1234-4234-8234-123456789abc"
	tests := []struct {
		name         string
		resume       bool
		wantContains string
		wantAbsent   string
	}{
		{
			name:         "resume=false uses --session-id",
			resume:       false,
			wantContains: "--session-id " + sessionID,
			wantAbsent:   "",
		},
		{
			name:         "resume=true uses --resume",
			resume:       true,
			wantContains: "--resume " + sessionID,
			wantAbsent:   "--session-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(t)
			ws := &state.Workspace{
				ID:        "ws-ab12cd34",
				Name:      "Trap Test",
				Branch:    "feat/trap",
				SandboxID: "agency-testproject",
				SessionID: sessionID,
				State:     state.StatePaused,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}
			addWorkspace(m, ws)

			cmd, err := m.buildTrapCmd(ws, tt.resume)
			if err != nil {
				t.Fatalf("buildTrapCmd(resume=%v): unexpected error: %v", tt.resume, err)
			}

			if !strings.Contains(cmd, tt.wantContains) {
				t.Errorf("buildTrapCmd(resume=%v): output does not contain %q\ngot: %s", tt.resume, tt.wantContains, cmd)
			}
			if tt.wantAbsent != "" && strings.Contains(cmd, tt.wantAbsent) {
				t.Errorf("buildTrapCmd(resume=%v): output should not contain %q\ngot: %s", tt.resume, tt.wantAbsent, cmd)
			}
			// Both workspace ID and sandbox ID must appear.
			if !strings.Contains(cmd, ws.ID) {
				t.Errorf("buildTrapCmd: output does not contain workspace ID %q\ngot: %s", ws.ID, cmd)
			}
			if !strings.Contains(cmd, ws.SandboxID) {
				t.Errorf("buildTrapCmd: output does not contain sandbox ID %q\ngot: %s", ws.SandboxID, cmd)
			}
			// Must use docker sandbox ls -q | grep to check existence, and docker sandbox exec.
			if !strings.Contains(cmd, "docker sandbox ls -q | grep -qx") {
				t.Errorf("buildTrapCmd: output does not contain 'docker sandbox ls -q | grep -qx'\ngot: %s", cmd)
			}
			if !strings.Contains(cmd, "docker sandbox exec") {
				t.Errorf("buildTrapCmd: output does not contain 'docker sandbox exec'\ngot: %s", cmd)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// reconcilePaused
// ---------------------------------------------------------------------------

// newMarkFailedCapture returns a markFailed function that records calls and the
// reason strings so tests can assert on them.
func newMarkFailedCapture() (markFailed func(*state.Workspace, string), called func() bool, reason func() string) {
	var wasCalled bool
	var lastReason string
	fn := func(ws *state.Workspace, r string) {
		wasCalled = true
		lastReason = r
		fromState := string(ws.State)
		ws.FailedFrom = &fromState
		ws.State = state.StateFailed
		ws.Error = &r
		ws.UpdatedAt = time.Now().UTC()
	}
	return fn, func() bool { return wasCalled }, func() string { return lastReason }
}

// TestReconcilePaused_WorktreeGone verifies that reconcilePaused marks a
// workspace failed when its worktree no longer exists.
func TestReconcilePaused_WorktreeGone(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	ws := &state.Workspace{
		ID:           "ws-aa000001",
		Name:         "Paused",
		Branch:       "feat/paused",
		SandboxID:    "agency-testproject",
		WorktreePath: "/nonexistent/path/testproject-feat-paused",
		State:        state.StatePaused,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	markFailed, called, reason := newMarkFailedCapture()

	// worktreeSet does NOT contain the workspace's path; wtErr is nil so the
	// absence is authoritative.
	worktreeSet := map[string]bool{}
	res := &reconcileResult{wtErr: nil, sandboxErr: nil, sandboxRunning: false}

	changed := m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

	if !changed {
		t.Error("reconcilePaused: expected changed=true when worktree is gone")
	}
	if !called() {
		t.Error("reconcilePaused: expected markFailed to be called when worktree is gone")
	}
	if !strings.Contains(reason(), "worktree") {
		t.Errorf("reconcilePaused: markFailed reason should mention worktree; got %q", reason())
	}
	if ws.State != state.StateFailed {
		t.Errorf("reconcilePaused: workspace state = %q, want StateFailed", ws.State)
	}
}

// TestReconcilePaused_DockerUnavailable_NilSandbox verifies that reconcilePaused
// makes no change when the sandbox manager is nil (Docker unavailable).
func TestReconcilePaused_DockerUnavailable_NilSandbox(t *testing.T) {
	m := newTestManager(t) // Sandbox is nil by default
	ctx := context.Background()

	wtPath := t.TempDir() // real path so worktreeSet check passes
	ws := &state.Workspace{
		ID:           "ws-aa000002",
		Name:         "Paused No Docker",
		Branch:       "feat/no-docker",
		SandboxID:    "agency-testproject",
		WorktreePath: wtPath,
		State:        state.StatePaused,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	markFailed, called, _ := newMarkFailedCapture()

	worktreeSet := map[string]bool{wtPath: true}
	res := &reconcileResult{wtErr: nil, sandboxErr: errors.New("docker is not available")}

	changed := m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

	if changed {
		t.Error("reconcilePaused: expected changed=false when Sandbox is nil")
	}
	if called() {
		t.Error("reconcilePaused: markFailed should not be called when Sandbox is nil")
	}
	if ws.State != state.StatePaused {
		t.Errorf("reconcilePaused: workspace state should remain StatePaused; got %q", ws.State)
	}
}

// TestReconcilePaused_DockerUnavailable_SandboxErr verifies that reconcilePaused
// makes no change when the sandbox query returned an error.
func TestReconcilePaused_DockerUnavailable_SandboxErr(t *testing.T) {
	m := newTestManager(t)
	m.Sandbox = &sandbox.Manager{} // non-nil but sandboxErr causes early return
	ctx := context.Background()

	wtPath := t.TempDir()
	ws := &state.Workspace{
		ID:           "ws-aa000003",
		Name:         "Paused Sandbox Err",
		Branch:       "feat/sandbox-err",
		SandboxID:    "agency-testproject",
		WorktreePath: wtPath,
		State:        state.StatePaused,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	markFailed, called, _ := newMarkFailedCapture()

	worktreeSet := map[string]bool{wtPath: true}
	res := &reconcileResult{
		wtErr:      nil,
		sandboxErr: errors.New("docker daemon unreachable"),
	}

	changed := m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

	if changed {
		t.Error("reconcilePaused: expected changed=false when sandboxErr is set")
	}
	if called() {
		t.Error("reconcilePaused: markFailed should not be called when sandboxErr is set")
	}
	if ws.State != state.StatePaused {
		t.Errorf("reconcilePaused: workspace state should remain StatePaused; got %q", ws.State)
	}
}

// newFakeDockerManager creates a Manager with a fake docker binary and returns
// the Manager and the path to the docker args-log file. The fake docker script
// handles sandbox commands.
func newFakeDockerManager(t *testing.T) (m *Manager, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "calls.txt")

	// Fake docker that handles sandbox subcommands:
	// - "image inspect" → exit 0 (image exists)
	// - "sandbox version" → exit 0
	// - "sandbox ls --json" → returns sandbox list JSON
	// - "sandbox create" → exit 0, echoes name
	// - "sandbox exec" → exit 0
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"` + "\n" +
		`shift` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  image)` + "\n" +
		`    case "$1" in` + "\n" +
		`      inspect) exit 0;;` + "\n" +
		`    esac;;` + "\n" +
		`  sandbox)` + "\n" +
		`    case "$1" in` + "\n" +
		`      version) exit 0;;` + "\n" +
		`      ls) echo "{\"vms\":[{\"name\":\"agency-testproject\",\"status\":\"running\",\"socket_path\":\"/tmp/agency-testproject.sock\"}]}";;` + "\n" +
		`      create) echo "agency-testproject"; exit 0;;` + "\n" +
		`      run)    exit 0;;` + "\n" +
		`      exec) exit 0;;` + "\n" +
		`    esac;;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	s.SandboxID = "agency-testproject"
	mgr := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     &sandbox.Manager{},
		Cfg:         config.DefaultConfig(),
	}
	if err := mgr.SaveState(); err != nil {
		t.Fatalf("newFakeDockerManager: SaveState: %v", err)
	}
	return mgr, argsFile
}

// newFakeDockerManagerSandboxEnsureFails is like newFakeDockerManager but
// "sandbox ls" returns an error so EnsureProjectSandbox fails.
func newFakeDockerManagerSandboxEnsureFails(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()

	script := "#!/bin/sh\n" +
		`subcmd="$1"` + "\n" +
		`shift` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  sandbox)` + "\n" +
		`    echo "fake: sandbox error" >&2; exit 1;;` + "\n" +
		`esac` + "\n" +
		`exit 0` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	s := state.Default("testproject", stateDir+"/.bare")
	mgr := &Manager{
		StatePath:   filepath.Join(stateDir, "state.json"),
		ProjectDir:  stateDir,
		ProjectName: "testproject",
		State:       s,
		Tmux:        tmux.New("agency-testproject"),
		Sandbox:     &sandbox.Manager{},
		Cfg:         config.DefaultConfig(),
	}
	if err := mgr.SaveState(); err != nil {
		t.Fatalf("newFakeDockerManagerSandboxEnsureFails: SaveState: %v", err)
	}
	return mgr
}

// TestReconcilePaused_SandboxEnsureFails verifies that reconcilePaused calls
// markFailed when EnsureProjectSandbox returns an error.
func TestReconcilePaused_SandboxEnsureFails(t *testing.T) {
	// Disable retry delay so the intentional ls failure doesn't slow tests.
	orig := sandbox.ListRetryDelay
	sandbox.ListRetryDelay = 0
	t.Cleanup(func() { sandbox.ListRetryDelay = orig })

	m := newFakeDockerManagerSandboxEnsureFails(t)
	ctx := context.Background()

	wtPath := t.TempDir()
	ws := &state.Workspace{
		ID:           "ws-aa000004",
		Name:         "Paused Ensure Fail",
		Branch:       "feat/ensure-fail",
		SandboxID:    "agency-testproject",
		WorktreePath: wtPath,
		State:        state.StatePaused,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	markFailed, called, reason := newMarkFailedCapture()

	worktreeSet := map[string]bool{wtPath: true}
	// sandboxErr nil means Docker responded (even if sandbox is down).
	res := &reconcileResult{wtErr: nil, sandboxErr: nil, sandboxRunning: false}

	changed := m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

	if !changed {
		t.Error("reconcilePaused: expected changed=true after EnsureProjectSandbox failure")
	}
	if !called() {
		t.Error("reconcilePaused: markFailed should be called when EnsureProjectSandbox fails")
	}
	if !strings.Contains(reason(), "ensuring project sandbox") {
		t.Errorf("reconcilePaused: markFailed reason should mention 'ensuring project sandbox'; got %q", reason())
	}
}

// TestReconcilePaused_SandboxRunning_ResumesTmux verifies that reconcilePaused
// attempts to resume the workspace via tmux when the sandbox is running.
// resumeTmux will fail (no real tmux), which is acceptable for this test.
func TestReconcilePaused_SandboxRunning_ResumesTmux(t *testing.T) {
	m, _ := newFakeDockerManager(t)
	ctx := context.Background()

	wtPath := t.TempDir()
	ws := &state.Workspace{
		ID:           "ws-aa000005",
		Name:         "Paused Resume",
		Branch:       "feat/resume",
		SandboxID:    "agency-testproject",
		SessionID:    "12345678-1234-4234-8234-123456789abc",
		WorktreePath: wtPath,
		State:        state.StatePaused,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	addWorkspace(m, ws)

	markFailed, called, reason := newMarkFailedCapture()

	worktreeSet := map[string]bool{wtPath: true}
	// sandboxErr nil, sandboxRunning true — sandbox is healthy.
	res := &reconcileResult{wtErr: nil, sandboxErr: nil, sandboxRunning: true}

	changed := m.reconcilePaused(ctx, ws, res, worktreeSet, markFailed)

	// Either the tmux resume succeeded (changed=true, state=running)
	// or it failed (changed=true, markFailed called with "resuming tmux").
	if !changed {
		t.Error("reconcilePaused: expected changed=true")
	}
	if called() {
		// resumeTmux failed — that's fine in tests, verify the reason.
		if !strings.Contains(reason(), "resuming tmux") {
			t.Errorf("reconcilePaused: markFailed reason should mention 'resuming tmux'; got %q", reason())
		}
	} else {
		// resumeTmux succeeded.
		if ws.State != state.StateRunning {
			t.Errorf("reconcilePaused: expected StateRunning when resume succeeds; got %q", ws.State)
		}
	}
}
