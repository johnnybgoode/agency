// Package tmux provides a client for tmux session management.
package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// Window represents a single tmux window.
type Window struct {
	ID   string
	Name string
}

// Tmux session environment variable keys for crash-resilient pane rediscovery.
const (
	EnvNavPane       = "AGENCY_NAV_PANE"
	EnvWorkspacePane = "AGENCY_WORKSPACE_PANE"
	EnvMainWindow    = "AGENCY_MAIN_WINDOW"
)

// Client is a thin wrapper around the tmux CLI, scoped to a single session.
type Client struct {
	SessionName string
	tmuxPath    string // resolved path to the tmux binary
}

// New returns a Client for the named tmux session. It resolves the tmux
// binary path once; all subsequent calls reuse the cached path.
func New(sessionName string) *Client {
	path, _ := exec.LookPath("tmux")
	slog.Debug("tmux client created", "session", sessionName, "binary", path)
	return &Client{SessionName: sessionName, tmuxPath: path}
}

// NewWithBinaryPath creates a Client that uses the supplied binaryPath instead
// of searching PATH. Intended for tests that inject a fake tmux script.
func NewWithBinaryPath(sessionName, binaryPath string) *Client {
	return &Client{SessionName: sessionName, tmuxPath: binaryPath}
}

// run is a shared helper that executes a tmux sub-command, returns trimmed
// stdout, and wraps any error with the combined output for diagnostics.
func (c *Client) run(args ...string) (string, error) {
	if c.tmuxPath == "" {
		return "", errors.New("tmux is not installed")
	}
	slog.Debug("tmux exec", "args", args)
	cmd := exec.Command(c.tmuxPath, args...) //nolint:gosec // tmuxPath is validated via exec.LookPath at construction
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		slog.Debug("tmux command failed", "args", args, "error", err)
		return "", fmt.Errorf("tmux %s: %w\n%s", strings.Join(args, " "), err, result)
	}
	slog.Debug("tmux exec done", "args", args, "output", result)
	return result, nil
}

// SessionExists reports whether the session already exists.
func (c *Client) SessionExists() bool {
	if c.tmuxPath == "" {
		return false
	}
	err := exec.Command(c.tmuxPath, "has-session", "-t", c.SessionName).Run() //nolint:gosec // tmuxPath is validated via exec.LookPath at construction
	exists := err == nil
	slog.Debug("session exists check", "session", c.SessionName, "exists", exists)
	return exists
}

// EnsureSession creates the session if it does not already exist.
func (c *Client) EnsureSession() error {
	if c.SessionExists() {
		return nil
	}
	slog.Info("creating tmux session", "session", c.SessionName)
	_, err := c.run("new-session", "-d", "-s", c.SessionName)
	return err
}

// NewWindow opens a new window in the session and returns its window ID.
func (c *Client) NewWindow(name string) (string, error) {
	return c.run("new-window", "-t", c.SessionName, "-n", name, "-P", "-F", "#{window_id}")
}

// KillWindow closes the window identified by windowID.
func (c *Client) KillWindow(windowID string) error {
	_, err := c.run("kill-window", "-t", c.SessionName+":"+windowID)
	return err
}

// SelectWindow brings windowID into focus.
func (c *Client) SelectWindow(windowID string) error {
	_, err := c.run("select-window", "-t", c.SessionName+":"+windowID)
	return err
}

// RenameWindow renames the window identified by windowID to newName.
func (c *Client) RenameWindow(windowID, newName string) error {
	_, err := c.run("rename-window", "-t", c.SessionName+":"+windowID, newName)
	return err
}

// ListWindows returns all windows in the session.
func (c *Client) ListWindows() ([]Window, error) {
	out, err := c.run("list-windows", "-t", c.SessionName, "-F", "#{window_id} #{window_name}")
	if err != nil {
		return nil, err
	}

	var windows []Window
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split on the first space only so window names with spaces are preserved.
		idx := strings.Index(line, " ")
		if idx < 0 {
			continue
		}
		windows = append(windows, Window{
			ID:   line[:idx],
			Name: line[idx+1:],
		})
	}
	return windows, nil
}

// SendKeys sends key strokes to the given window followed by Enter.
func (c *Client) SendKeys(windowID, keys string) error {
	_, err := c.run("send-keys", "-t", c.SessionName+":"+windowID, keys, "Enter")
	return err
}

// SendKeysToPane sends keystrokes directly to a pane by pane ID and presses Enter.
// Pane IDs (e.g. "%5") are globally unique within the tmux server.
func (c *Client) SendKeysToPane(paneID, keys string) error {
	_, err := c.run("send-keys", "-t", paneID, keys, "Enter")
	return err
}

// SendRawKeyToPane sends a single tmux key name (e.g. "C-d") to a pane
// without appending Enter. Use this for control characters.
func (c *Client) SendRawKeyToPane(paneID, key string) error {
	_, err := c.run("send-keys", "-t", paneID, key)
	return err
}

// SelectPane makes the given pane the active pane in its window.
func (c *Client) SelectPane(paneID string) error {
	_, err := c.run("select-pane", "-t", paneID)
	return err
}

// SplitWindowVertical creates a vertical (horizontal split) pane in windowID
// and returns the new right pane ID.
func (c *Client) SplitWindowVertical(windowID string) (string, error) {
	return c.run("split-window", "-h", "-t", c.SessionName+":"+windowID, "-P", "-F", "#{pane_id}")
}

// JoinPane moves a pane from its current location into targetWindowID as the
// right pane.
func (c *Client) JoinPane(srcPaneID, targetWindowID string) error {
	_, err := c.run("join-pane", "-s", srcPaneID, "-t", c.SessionName+":"+targetWindowID, "-h")
	return err
}

// BreakPane moves a pane out of its current window into a new detached window.
// Returns the new window ID.
func (c *Client) BreakPane(windowID, paneID string) (string, error) {
	return c.run("break-pane", "-s", c.SessionName+":"+windowID+"."+paneID, "-d", "-P", "-F", "#{window_id}")
}

// GetWindowPanes returns all pane IDs for a window.
func (c *Client) GetWindowPanes(windowID string) ([]string, error) {
	out, err := c.run("list-panes", "-t", c.SessionName+":"+windowID, "-F", "#{pane_id}")
	if err != nil {
		return nil, err
	}
	var panes []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			panes = append(panes, line)
		}
	}
	return panes, nil
}

// KillPane destroys a pane by its pane ID.
func (c *Client) KillPane(paneID string) error {
	_, err := c.run("kill-pane", "-t", paneID)
	return err
}

// ResizePane resizes the pane identified by paneID to the given width (columns).
func (c *Client) ResizePane(paneID string, width int) error {
	_, err := c.run("resize-pane", "-t", paneID, "-x", fmt.Sprintf("%d", width))
	return err
}

// WindowWidth returns the width in columns of the given window.
func (c *Client) WindowWidth(windowID string) (int, error) {
	out, err := c.run("display-message", "-p", "-t", c.SessionName+":"+windowID, "#{window_width}")
	if err != nil {
		return 0, err
	}
	var w int
	if _, err := fmt.Sscanf(out, "%d", &w); err != nil {
		return 0, fmt.Errorf("parsing window width %q: %w", out, err)
	}
	return w, nil
}

// DisplayPopup runs cmd in a tmux display-popup overlay.
// The popup is sized to width columns × height rows.
// If x > 0 it is passed as the -x left-edge offset of the popup.
func (c *Client) DisplayPopup(cmd string, width, height, x int) error {
	args := []string{
		"display-popup", "-E",
		"-w", fmt.Sprintf("%d", width),
		"-h", fmt.Sprintf("%d", height),
	}
	if x > 0 {
		args = append(args, "-x", fmt.Sprintf("%d", x))
	}
	args = append(args, cmd)
	_, err := c.run(args...)
	return err
}

// SwapPane swaps two panes by their pane IDs. -d prevents the active pane from
// changing (i.e. the currently focused pane stays focused after the swap).
func (c *Client) SwapPane(targetPaneID, sourcePaneID string) error {
	_, err := c.run("swap-pane", "-d", "-t", targetPaneID, "-s", sourcePaneID)
	return err
}

// SetOption sets a tmux option on the session.
func (c *Client) SetOption(option, value string) error {
	_, err := c.run("set-option", "-t", c.SessionName, option, value)
	return err
}

// SplitWindowHorizontalPercent splits windowID horizontally, giving the new
// right pane pct% of the total width. Returns the new right pane ID.
func (c *Client) SplitWindowHorizontalPercent(windowID string, pct int) (string, error) {
	return c.run("split-window", "-h", "-p", fmt.Sprintf("%d", pct), "-t", c.SessionName+":"+windowID, "-P", "-F", "#{pane_id}")
}

// KillSession kills the entire tmux session (called after graceful quit cleanup).
func (c *Client) KillSession() error {
	slog.Info("killing tmux session", "session", c.SessionName)
	_, err := c.run("kill-session", "-t", c.SessionName)
	return err
}

// DetachClients detaches all clients from the session without killing it
// (called when the user cancels quit while active workspaces are running).
func (c *Client) DetachClients() error {
	_, err := c.run("detach-client", "-s", c.SessionName)
	return err
}

// Attach attaches the current terminal to the session interactively.
func (c *Client) Attach() error {
	slog.Info("attaching to tmux session", "session", c.SessionName)
	if c.tmuxPath == "" {
		return errors.New("tmux is not installed")
	}
	cmd := exec.Command(c.tmuxPath, "attach-session", "-t", c.SessionName) //nolint:gosec // tmuxPath is validated via exec.LookPath at construction
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux attach-session -t %s: %w", c.SessionName, err)
	}
	return nil
}

// SetEnvironment sets a session-scoped environment variable.
func (c *Client) SetEnvironment(key, value string) error {
	_, err := c.run("set-environment", "-t", c.SessionName, key, value)
	return err
}

// GetEnvironment reads a session-scoped environment variable.
// Returns ("", nil) if unset.
func (c *Client) GetEnvironment(key string) (string, error) {
	out, err := c.run("show-environment", "-t", c.SessionName, key)
	if err != nil {
		// tmux returns error when the variable is not set.
		return "", nil
	}
	// Output format: "KEY=value"
	if idx := strings.Index(out, "="); idx >= 0 {
		return out[idx+1:], nil
	}
	return "", nil
}

// PaneExists checks whether a pane ID is alive in the tmux server.
func (c *Client) PaneExists(paneID string) bool {
	_, err := c.run("display-message", "-p", "-t", paneID, "#{pane_id}")
	return err == nil
}

// CapturePane returns the visible content of the pane identified by paneID.
// This is useful for detecting what state a program inside the pane is in
// (e.g., whether Claude is at its input prompt or inside a sub-command dialog).
// Note: leading and trailing whitespace is trimmed by the underlying run()
// helper. Callers that depend on whitespace at the boundaries of pane content
// should account for this.
func (c *Client) CapturePane(paneID string) (string, error) {
	return c.run("capture-pane", "-p", "-t", paneID)
}

// SetPaneOption sets a pane-level option (e.g., remain-on-exit).
func (c *Client) SetPaneOption(paneID, option, value string) error {
	_, err := c.run("set-option", "-p", "-t", paneID, option, value)
	return err
}

// RespawnPane respawns a dead pane (one with remain-on-exit on).
func (c *Client) RespawnPane(paneID string) error {
	_, err := c.run("respawn-pane", "-t", paneID)
	return err
}

// SetHook installs a session-scoped tmux hook.
// The name is appended to the trigger as a named hook (e.g. "pane-died[respawn-workspace]"),
// which allows multiple hooks to be registered for the same event without overwriting each other.
func (c *Client) SetHook(name, trigger, command string) error {
	_, err := c.run("set-hook", "-t", c.SessionName, trigger+"["+name+"]", command)
	return err
}

// BindKey installs a session-scoped key binding (no prefix required).
func (c *Client) BindKey(key, tmuxCommand string) error {
	_, err := c.run("bind-key", "-n", "-T", "root", key, tmuxCommand)
	return err
}
