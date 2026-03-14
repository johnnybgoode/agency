package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/session"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/worktree"
)

// Run is the entrypoint for the interactive TUI. It locates the project
// directory, acquires an exclusive lock, reconciles session state, then hands
// control to Bubble Tea.
func Run() error {
	projectDir, err := findProjectDir()
	if err != nil {
		return err
	}

	// Auto-init .tool/ if missing (e.g. bare repo exists but tool dir was never created).
	if !isDir(filepath.Join(projectDir, ".tool")) {
		if err := worktree.Init(projectDir, ""); err != nil {
			return fmt.Errorf("initializing project: %w", err)
		}
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	lockPath := filepath.Join(projectDir, ".tool", "lock")
	lock, err := state.AcquireLock(lockPath)
	if err != nil {
		// Check whether the process that holds the lock is still alive.
		statePath := filepath.Join(projectDir, ".tool", "state.json")
		if s, readErr := state.Read(statePath); readErr == nil && s.PID > 0 {
			if state.IsProcessAlive(s.PID) {
				return fmt.Errorf("agency is already running (pid %d); use tmux to attach", s.PID)
			}
			// PID is dead — stale lock. Force-remove and retry.
			os.Remove(lockPath)
			lock, err = state.AcquireLock(lockPath)
			if err != nil {
				return fmt.Errorf("acquiring lock after stale cleanup: %w", err)
			}
		} else {
			return fmt.Errorf("acquiring lock: %w", err)
		}
	}
	defer lock.Release() //nolint:errcheck // lock cleanup on shutdown

	mgr, err := session.NewManager(projectDir, cfg)
	if err != nil {
		return fmt.Errorf("initializing session manager: %w", err)
	}

	if err := mgr.Tmux.EnsureSession(); err != nil {
		// Non-fatal: tmux may not be installed or we may be in a test env.
		fmt.Fprintf(os.Stderr, "warning: could not ensure tmux session: %v\n", err)
	}

	if err := mgr.Reconcile(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile failed: %v\n", err)
	}

	model := newListModel(mgr)
	p := tea.NewProgram(model, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// If the user selected a session, attach to the tmux session so they
	// land in the correct window rather than falling back to the shell.
	if lm, ok := result.(listModel); ok && lm.selectedWindow != "" {
		return mgr.Tmux.Attach()
	}

	return nil
}

// findProjectDir walks up from the current working directory looking for a
// .tool/ or .bare/ directory, which marks an agency project root.
func findProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	dir := cwd
	for {
		if isDir(filepath.Join(dir, ".tool")) || isDir(filepath.Join(dir, ".bare")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root.
			break
		}
		dir = parent
	}

	return "", fmt.Errorf(
		"not in an agency project (no .tool/ or .bare/ found); run 'agency init' first",
	)
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
