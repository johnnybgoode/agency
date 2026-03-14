// Package tmux provides a client for tmux session management.
package tmux

import (
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

// run is a shared helper that executes a tmux sub-command, returns trimmed
// stdout, and wraps any error with the combined output for diagnostics.
func (c *Client) run(args ...string) (string, error) {
	if c.tmuxPath == "" {
		return "", fmt.Errorf("tmux is not installed")
	}
	cmd := exec.Command(c.tmuxPath, args...)
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
	err := exec.Command(c.tmuxPath, "has-session", "-t", c.SessionName).Run()
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

// Attach attaches the current terminal to the session interactively.
func (c *Client) Attach() error {
	if c.tmuxPath == "" {
		return fmt.Errorf("tmux is not installed")
	}
	cmd := exec.Command(c.tmuxPath, "attach-session", "-t", c.SessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux attach-session -t %s: %w", c.SessionName, err)
	}
	return nil
}
