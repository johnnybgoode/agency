// Package tmux provides a client for tmux session management.
package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Window represents a single tmux window.
type Window struct {
	ID   string
	Name string
}

// Client is a thin wrapper around the tmux CLI, scoped to a single session.
type Client struct {
	SessionName string
	tmuxPath    string // resolved path to the tmux binary
}

// New returns a Client for the named tmux session. It resolves the tmux
// binary path once; all subsequent calls reuse the cached path.
func New(sessionName string) *Client {
	path, _ := exec.LookPath("tmux")
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
	cmd := exec.Command(c.tmuxPath, args...) //nolint:gosec // tmuxPath is validated via exec.LookPath at construction
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SessionExists reports whether the session already exists.
func (c *Client) SessionExists() bool {
	if c.tmuxPath == "" {
		return false
	}
	err := exec.Command(c.tmuxPath, "has-session", "-t", c.SessionName).Run() //nolint:gosec // tmuxPath is validated via exec.LookPath at construction
	return err == nil
}

// EnsureSession creates the session if it does not already exist.
func (c *Client) EnsureSession() error {
	if c.SessionExists() {
		return nil
	}
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

// ResizePane resizes the pane identified by paneID to the given width (columns).
func (c *Client) ResizePane(paneID string, width int) error {
	_, err := c.run("resize-pane", "-t", paneID, "-x", fmt.Sprintf("%d", width))
	return err
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

// Attach attaches the current terminal to the session interactively.
func (c *Client) Attach() error {
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
