package tui

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/state"
)

// makeInstallerScript writes a bash script to a temp file that:
//   - Parses --install-dir DIR from args
//   - Collects remaining positional args as agent names
//   - Reads one line from stdin (user pressing Enter to confirm)
//   - Creates DIR if it doesn't exist
//   - Touches DIR/<agentname>.md for each agent
//   - Exits 0
func makeInstallerScript(t *testing.T) string {
	t.Helper()
	script := `#!/bin/bash
install_dir=""
agents=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)
      install_dir="$2"
      shift 2
      ;;
    *)
      agents+=("$1")
      shift
      ;;
  esac
done

# Read one line from stdin (simulates user pressing Enter)
read -r _confirm

if [[ -n "$install_dir" ]]; then
  mkdir -p "$install_dir"
  for agent in "${agents[@]}"; do
    touch "$install_dir/$agent.md"
  done
fi

exit 0
`
	path := filepath.Join(t.TempDir(), "install-agents.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("makeInstallerScript: write: %v", err)
	}
	return path
}

type sentKey struct {
	paneID, key string
}

type fakePopupRunner struct {
	mu       sync.Mutex
	popupCmd string
	sentKeys []sentKey
	runErr   error
	keyErr   error
	stdin    io.Reader // piped to the popup process; nil means strings.NewReader("\n")
}

func (f *fakePopupRunner) DisplayPopup(cmd string, width, height, x int) error {
	f.mu.Lock()
	f.popupCmd = cmd
	runErr := f.runErr
	var stdin io.Reader
	if f.stdin != nil {
		stdin = f.stdin
	} else {
		stdin = strings.NewReader("\n")
	}
	f.mu.Unlock()

	if runErr != nil {
		return runErr
	}

	c := exec.Command("sh", "-c", cmd)
	c.Stdin = stdin
	if err := c.Run(); err != nil {
		return err
	}
	return nil
}

func (f *fakePopupRunner) SendRawKeyToPane(paneID, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentKeys = append(f.sentKeys, sentKey{paneID: paneID, key: key})
	return f.keyErr
}

// newInstallerListModel constructs a listModel wired for installer tests.
func newInstallerListModel(t *testing.T, runner *fakePopupRunner, cmdFn func(string) string, ws *state.Workspace) listModel {
	t.Helper()
	m := newListModelForTest(t)
	m.popup = runner
	m.installerCmd = cmdFn
	m.workspaces = []*state.Workspace{ws}
	return m
}

// runSKey simulates pressing 's' on the list model and returns the resulting
// model and command.
//
//nolint:gocritic // test helper; value copy is intentional to avoid mutating caller's model
func runSKey(m listModel) (listModel, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	return next.(listModel), cmd
}

// --- Tests ---

func TestInstall_SKeyNoOp_NonRunningWorkspace(t *testing.T) {
	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateDone,
		SandboxID: "container123",
	}
	m := newInstallerListModel(t, runner, func(id string) string { return "echo " + id }, ws)
	_, cmd := runSKey(m)
	if cmd != nil {
		t.Errorf("expected nil cmd for non-running workspace, got non-nil")
	}
}

func TestInstall_SKeyNoOp_EmptySandboxID(t *testing.T) {
	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "",
	}
	m := newInstallerListModel(t, runner, func(id string) string { return "echo " + id }, ws)
	_, cmd := runSKey(m)
	if cmd != nil {
		t.Errorf("expected nil cmd for empty SandboxID, got non-nil")
	}
}

func TestInstall_SKeyNoOp_EmptyList(t *testing.T) {
	runner := &fakePopupRunner{}
	m := newListModelForTest(t)
	m.popup = runner
	m.installerCmd = func(id string) string { return "echo " + id }
	m.workspaces = []*state.Workspace{}

	// Must not panic; cmd must be nil.
	_, cmd := runSKey(m)
	if cmd != nil {
		t.Errorf("expected nil cmd for empty workspace list, got non-nil")
	}
}

func TestInstall_SKeyDispatchesCmd(t *testing.T) {
	runner := &fakePopupRunner{runErr: nil}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "container123",
	}
	m := newInstallerListModel(t, runner, func(id string) string { return "echo " + id }, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd for running workspace with SandboxID, got nil")
	}
}

func TestInstall_PopupCmdContainerID(t *testing.T) {
	const containerID = "container-abc123"
	runner := &fakePopupRunner{
		// Use a no-op command so DisplayPopup succeeds without executing anything real.
		runErr: nil,
	}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: containerID,
	}
	cmdFn := func(id string) string { return "echo installed-" + id }
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd() // execute the tea.Cmd synchronously

	runner.mu.Lock()
	got := runner.popupCmd
	runner.mu.Unlock()

	if !strings.Contains(got, containerID) {
		t.Errorf("DisplayPopup called with %q, want it to contain %q", got, containerID)
	}
}

func TestInstall_SendsCd_WithPane(t *testing.T) {
	const paneID = "%42"
	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "container123",
		PaneID:    paneID,
	}
	cmdFn := func(id string) string { return "echo ok" }
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd()

	runner.mu.Lock()
	keys := runner.sentKeys
	runner.mu.Unlock()

	if len(keys) == 0 {
		t.Fatal("expected SendRawKeyToPane to be called, but sentKeys is empty")
	}
	found := false
	for _, k := range keys {
		if k.paneID == paneID && k.key == "C-d" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SendRawKeyToPane(%q, %q) not found; sentKeys = %v", paneID, "C-d", keys)
	}
}

func TestInstall_NoCd_EmptyPane(t *testing.T) {
	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "container123",
		PaneID:    "", // no pane
	}
	cmdFn := func(id string) string { return "echo ok" }
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd()

	runner.mu.Lock()
	keys := runner.sentKeys
	runner.mu.Unlock()

	if len(keys) != 0 {
		t.Errorf("expected no SendRawKeyToPane calls for empty PaneID, got %v", keys)
	}
}

func TestInstall_PopupError_StillSendsCd(t *testing.T) {
	const paneID = "%99"
	runner := &fakePopupRunner{
		runErr: os.ErrPermission, // DisplayPopup returns an error
	}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "container123",
		PaneID:    paneID,
	}
	cmdFn := func(id string) string { return "echo ok" }
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd() // error from DisplayPopup is discarded; C-d should still be sent

	runner.mu.Lock()
	keys := runner.sentKeys
	runner.mu.Unlock()

	found := false
	for _, k := range keys {
		if k.paneID == paneID && k.key == "C-d" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("C-d not sent after DisplayPopup error; sentKeys = %v", keys)
	}
}

func TestInstall_MockInstaller_SingleAgent(t *testing.T) {
	scriptPath := makeInstallerScript(t)
	installDir := filepath.Join(t.TempDir(), "agents")

	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "unused-container",
	}
	cmdFn := func(_ string) string {
		return scriptPath + " --install-dir " + installDir + " myagent"
	}
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd()

	matches, err := filepath.Glob(filepath.Join(installDir, "*.md"))
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 .md file, got %d: %v", len(matches), matches)
	}
	want := filepath.Join(installDir, "myagent.md")
	if len(matches) == 1 && matches[0] != want {
		t.Errorf("expected file %q, got %q", want, matches[0])
	}
}

func TestInstall_MockInstaller_MultipleAgents(t *testing.T) {
	scriptPath := makeInstallerScript(t)
	installDir := filepath.Join(t.TempDir(), "agents")

	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "unused-container",
	}
	agents := []string{"alpha", "beta", "gamma"}
	cmdFn := func(_ string) string {
		return scriptPath + " --install-dir " + installDir + " " + strings.Join(agents, " ")
	}
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd()

	matches, err := filepath.Glob(filepath.Join(installDir, "*.md"))
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(matches) != len(agents) {
		t.Errorf("expected %d .md files, got %d: %v", len(agents), len(matches), matches)
	}
}

func TestInstall_MockInstaller_NoAgents_NoFiles(t *testing.T) {
	scriptPath := makeInstallerScript(t)
	installDir := filepath.Join(t.TempDir(), "agents")

	runner := &fakePopupRunner{}
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		State:     state.StateRunning,
		SandboxID: "unused-container",
	}
	// No agent args — only --install-dir
	cmdFn := func(_ string) string {
		return scriptPath + " --install-dir " + installDir
	}
	m := newInstallerListModel(t, runner, cmdFn, ws)
	_, cmd := runSKey(m)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmd()

	matches, err := filepath.Glob(filepath.Join(installDir, "*.md"))
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 .md files with no agents, got %d: %v", len(matches), matches)
	}
}
