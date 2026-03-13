package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFakeClient writes a fake tmux shell script to dir/tmux, makes it
// executable, and returns a Client whose tmuxPath points to it.
//
// The script writes all received arguments (space-joined) to argsFile, then
// prints output to stdout. output may be empty.
func newFakeClient(t *testing.T, output string) (client *Client, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")

	// Build the fake script. It records args and echoes the caller-supplied output.
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n"
	if output != "" {
		script += `printf '%s\n' ` + shellescape(output) + "\n"
	}

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux script: %v", err)
	}

	client = &Client{SessionName: "test-session", tmuxPath: scriptPath}
	return client, argsFile
}

// shellescape wraps s in single quotes so the shell treats it as a literal
// string. It is sufficient for the simple ASCII output strings used in tests.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readArgs reads the space-joined argument string captured by the fake tmux
// script and splits it back into individual arguments.
func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file %s: %v", argsFile, err)
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return nil
	}
	return strings.Split(line, " ")
}

// containsAll checks that all of the expected strings appear in args (order
// independent).
func containsAll(args []string, expected ...string) bool {
	set := make(map[string]bool, len(args))
	for _, a := range args {
		set[a] = true
	}
	for _, e := range expected {
		if !set[e] {
			return false
		}
	}
	return true
}

// argsContainSequence returns true when needle appears as a contiguous
// sub-sequence inside haystack.
func argsContainSequence(haystack []string, needle ...string) bool {
	if len(needle) == 0 {
		return true
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j, n := range needle {
			if haystack[i+j] != n {
				continue outer
			}
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// EnsureSession
// ---------------------------------------------------------------------------

func TestEnsureSession_CreatesNewSession(t *testing.T) {
	c, _ := newFakeClient(t, "")
	// tmuxPath is set to the fake script, which always exits 0, so
	// SessionExists() (has-session) will succeed and EnsureSession will return
	// early without calling new-session.  We need SessionExists to return false
	// so that new-session is actually invoked.
	//
	// Strategy: use a second fake script that exits non-zero for has-session
	// and exits 0 for new-session, recording args on new-session.
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")

	script := "#!/bin/sh\n" +
		`subcmd="$1"` + "\n" +
		`if [ "$subcmd" = "has-session" ]; then exit 1; fi` + "\n" +
		`echo "$@" > ` + argsFile + "\n"

	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	c = &Client{SessionName: "test-session", tmuxPath: scriptPath}

	if err := c.EnsureSession(); err != nil {
		t.Fatalf("EnsureSession() unexpected error: %v", err)
	}

	args := readArgs(t, argsFile)
	if !argsContainSequence(args, "new-session", "-d", "-s", "test-session") {
		t.Errorf("EnsureSession args = %v, want to contain [new-session -d -s test-session]", args)
	}
}

func TestEnsureSession_SessionAlreadyExists(t *testing.T) {
	// has-session returns 0 -> EnsureSession should be a no-op (no new-session).
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")

	// Script exits 0 for everything (has-session included) but does NOT write
	// args so argsFile will not exist if new-session was never called.
	script := "#!/bin/sh\nexit 0\n"
	scriptPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	c := &Client{SessionName: "test-session", tmuxPath: scriptPath}

	if err := c.EnsureSession(); err != nil {
		t.Fatalf("EnsureSession() unexpected error: %v", err)
	}

	// new-session should NOT have been called, so argsFile must not exist.
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Error("EnsureSession should not call new-session when session already exists")
	}
}

// ---------------------------------------------------------------------------
// NewWindow
// ---------------------------------------------------------------------------

func TestNewWindow(t *testing.T) {
	tests := []struct {
		name       string
		windowName string
		wantSeq    []string
	}{
		{
			name:       "simple window name",
			windowName: "editor",
			wantSeq:    []string{"new-window", "-t", "test-session", "-n", "editor", "-P", "-F", "#{window_id}"},
		},
		{
			name:       "window name with hyphen",
			windowName: "my-window",
			wantSeq:    []string{"new-window", "-t", "test-session", "-n", "my-window"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, "@1")
			id, err := c.NewWindow(tt.windowName)
			if err != nil {
				t.Fatalf("NewWindow(%q) error: %v", tt.windowName, err)
			}
			if id != "@1" {
				t.Errorf("NewWindow returned id=%q, want %q", id, "@1")
			}
			args := readArgs(t, argsFile)
			if !argsContainSequence(args, tt.wantSeq...) {
				t.Errorf("NewWindow args = %v, want sequence %v", args, tt.wantSeq)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetWindowPanes
// ---------------------------------------------------------------------------

func TestGetWindowPanes(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		windowID  string
		wantPanes []string
	}{
		{
			name:      "two panes",
			output:    "%1\n%2",
			windowID:  "@3",
			wantPanes: []string{"%1", "%2"},
		},
		{
			name:      "single pane",
			output:    "%5",
			windowID:  "@1",
			wantPanes: []string{"%5"},
		},
		{
			name:      "empty output means no panes",
			output:    "",
			windowID:  "@0",
			wantPanes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, tt.output)
			panes, err := c.GetWindowPanes(tt.windowID)
			if err != nil {
				t.Fatalf("GetWindowPanes(%q) error: %v", tt.windowID, err)
			}

			if len(panes) != len(tt.wantPanes) {
				t.Fatalf("GetWindowPanes returned %d panes, want %d: %v", len(panes), len(tt.wantPanes), panes)
			}
			for i, p := range panes {
				if p != tt.wantPanes[i] {
					t.Errorf("panes[%d] = %q, want %q", i, p, tt.wantPanes[i])
				}
			}

			// Verify the correct tmux sub-command and target were used.
			args := readArgs(t, argsFile)
			target := "test-session:" + tt.windowID
			if !argsContainSequence(args, "list-panes", "-t", target) {
				t.Errorf("GetWindowPanes args = %v, want [list-panes -t %s ...]", args, target)
			}
			if !containsAll(args, "-F", "#{pane_id}") {
				t.Errorf("GetWindowPanes args = %v, want -F #{pane_id}", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SplitWindowVertical
// ---------------------------------------------------------------------------

func TestSplitWindowVertical(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		fakePane string
	}{
		{name: "returns new pane ID", windowID: "@2", fakePane: "%3"},
		{name: "different window", windowID: "@10", fakePane: "%11"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, tt.fakePane)
			paneID, err := c.SplitWindowVertical(tt.windowID)
			if err != nil {
				t.Fatalf("SplitWindowVertical(%q) error: %v", tt.windowID, err)
			}
			if paneID != tt.fakePane {
				t.Errorf("SplitWindowVertical paneID = %q, want %q", paneID, tt.fakePane)
			}

			args := readArgs(t, argsFile)
			target := "test-session:" + tt.windowID
			if !argsContainSequence(args, "split-window", "-h", "-t", target) {
				t.Errorf("SplitWindowVertical args = %v, want [split-window -h -t %s ...]", args, target)
			}
			if !argsContainSequence(args, "-P", "-F", "#{pane_id}") {
				t.Errorf("SplitWindowVertical args = %v, want -P -F #{pane_id}", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// JoinPane
// ---------------------------------------------------------------------------

func TestJoinPane(t *testing.T) {
	tests := []struct {
		name           string
		srcPaneID      string
		targetWindowID string
	}{
		{name: "basic join", srcPaneID: "%5", targetWindowID: "@3"},
		{name: "different IDs", srcPaneID: "%10", targetWindowID: "@7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, "")
			if err := c.JoinPane(tt.srcPaneID, tt.targetWindowID); err != nil {
				t.Fatalf("JoinPane(%q, %q) error: %v", tt.srcPaneID, tt.targetWindowID, err)
			}

			args := readArgs(t, argsFile)
			srcTarget := "test-session:" + tt.srcPaneID
			dstTarget := "test-session:" + tt.targetWindowID

			if !argsContainSequence(args, "join-pane", "-s", srcTarget) {
				t.Errorf("JoinPane args = %v, want [join-pane -s %s ...]", args, srcTarget)
			}
			if !argsContainSequence(args, "-t", dstTarget) {
				t.Errorf("JoinPane args = %v, want [-t %s ...]", args, dstTarget)
			}
			if !containsAll(args, "-h") {
				t.Errorf("JoinPane args = %v, want -h flag", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BreakPane
// ---------------------------------------------------------------------------

func TestBreakPane(t *testing.T) {
	tests := []struct {
		name      string
		windowID  string
		paneID    string
		fakeWinID string
	}{
		{name: "basic break", windowID: "@2", paneID: "%4", fakeWinID: "@9"},
		{name: "other IDs", windowID: "@5", paneID: "%6", fakeWinID: "@12"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, tt.fakeWinID)
			winID, err := c.BreakPane(tt.windowID, tt.paneID)
			if err != nil {
				t.Fatalf("BreakPane(%q, %q) error: %v", tt.windowID, tt.paneID, err)
			}
			if winID != tt.fakeWinID {
				t.Errorf("BreakPane winID = %q, want %q", winID, tt.fakeWinID)
			}

			args := readArgs(t, argsFile)
			srcTarget := "test-session:" + tt.windowID + "." + tt.paneID
			if !argsContainSequence(args, "break-pane", "-s", srcTarget) {
				t.Errorf("BreakPane args = %v, want [break-pane -s %s ...]", args, srcTarget)
			}
			if !argsContainSequence(args, "-d", "-P", "-F", "#{window_id}") {
				t.Errorf("BreakPane args = %v, want [-d -P -F #{window_id}]", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SendKeys
// ---------------------------------------------------------------------------

func TestSendKeys(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		keys     string
	}{
		{name: "simple keys", windowID: "@1", keys: "ls"},
		{name: "complex command", windowID: "@5", keys: "make test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, "")
			if err := c.SendKeys(tt.windowID, tt.keys); err != nil {
				t.Fatalf("SendKeys(%q, %q) error: %v", tt.windowID, tt.keys, err)
			}

			args := readArgs(t, argsFile)
			target := "test-session:" + tt.windowID
			if !argsContainSequence(args, "send-keys", "-t", target) {
				t.Errorf("SendKeys args = %v, want [send-keys -t %s ...]", args, target)
			}
			// The final argument should be "Enter".
			if len(args) == 0 || args[len(args)-1] != "Enter" {
				t.Errorf("SendKeys args = %v, want last arg to be Enter", args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error path: tmuxPath is empty
// ---------------------------------------------------------------------------

func TestRunNoTmux(t *testing.T) {
	c := &Client{SessionName: "x", tmuxPath: ""}
	_, err := c.run("anything")
	if err == nil {
		t.Fatal("run() with empty tmuxPath should return error")
	}
	if !strings.Contains(err.Error(), "tmux is not installed") {
		t.Errorf("error = %q, want to contain 'tmux is not installed'", err.Error())
	}
}

func TestSessionExistsNoTmux(t *testing.T) {
	c := &Client{SessionName: "x", tmuxPath: ""}
	if c.SessionExists() {
		t.Error("SessionExists() with empty tmuxPath should return false")
	}
}

// ---------------------------------------------------------------------------
// ListWindows
// ---------------------------------------------------------------------------

func TestListWindows(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []Window
	}{
		{
			name:   "two windows",
			output: "@1 main\n@2 editor",
			want: []Window{
				{ID: "@1", Name: "main"},
				{ID: "@2", Name: "editor"},
			},
		},
		{
			name:   "window name with spaces",
			output: "@3 my cool window",
			want: []Window{
				{ID: "@3", Name: "my cool window"},
			},
		},
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, argsFile := newFakeClient(t, tt.output)
			windows, err := c.ListWindows()
			if err != nil {
				t.Fatalf("ListWindows() error: %v", err)
			}
			if len(windows) != len(tt.want) {
				t.Fatalf("ListWindows returned %d windows, want %d: %v", len(windows), len(tt.want), windows)
			}
			for i, w := range windows {
				if w.ID != tt.want[i].ID || w.Name != tt.want[i].Name {
					t.Errorf("windows[%d] = %+v, want %+v", i, w, tt.want[i])
				}
			}

			args := readArgs(t, argsFile)
			if !argsContainSequence(args, "list-windows", "-t", "test-session") {
				t.Errorf("ListWindows args = %v, want [list-windows -t test-session ...]", args)
			}
		})
	}
}
