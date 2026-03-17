// Package main is the entry point for the agency CLI tool.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/logging"
	"github.com/johnnybgoode/agency/internal/project"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tui"
	"github.com/johnnybgoode/agency/internal/workspace"
	"github.com/johnnybgoode/agency/internal/worktree"
	"github.com/spf13/cobra"
)

// logCleanup is set by PersistentPreRunE and called by PersistentPostRunE to
// close the log file.
var logCleanup func()

var rootCmd = &cobra.Command{
	Use:          "agency",
	Short:        "Coding agent workspace manager",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		projectDir, err := project.FindProjectDir()
		if err != nil {
			// Not in a project (e.g. version, init) — skip logging setup.
			return nil
		}

		statePath := filepath.Join(projectDir, ".agency", "state.json")
		s, err := state.Read(statePath)
		if err != nil {
			// State file doesn't exist yet — skip logging setup.
			return nil
		}

		if s.SessionStartedAt == nil {
			now := time.Now().UTC()
			s.SessionStartedAt = &now
			_ = state.Write(statePath, s)
		}

		levelStr, _ := cmd.Flags().GetString("log-level")
		level, err := logging.ParseLevel(levelStr)
		if err != nil {
			return err
		}

		cleanup, err := logging.Setup(projectDir, level, *s.SessionStartedAt)
		if err != nil {
			return fmt.Errorf("setting up logging: %w", err)
		}
		logCleanup = cleanup

		slog.Info("agency starting", "project", projectDir, "pid", os.Getpid())
		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if logCleanup != nil {
			logCleanup()
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("agency v1.0.0")
	},
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new agency project",
	RunE: func(cmd *cobra.Command, args []string) error {
		remote, _ := cmd.Flags().GetString("remote")

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}

		if err := worktree.Init(cwd, remote); err != nil {
			return err
		}

		homeDir := filepath.Join(cwd, ".agency", "home")
		if err := os.MkdirAll(homeDir, 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create .agency/home: %v\n", err)
		}

		projectName := filepath.Base(cwd)
		statePath := filepath.Join(cwd, ".agency", "state.json")
		s := state.Default(projectName, filepath.Join(cwd, ".bare"))
		if err := state.Write(statePath, s); err != nil {
			return fmt.Errorf("writing initial state: %w", err)
		}

		// Enforce permissions on the global config file.
		if err := config.EnforceGlobalConfigPerms(config.GlobalConfigPath()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not enforce global config permissions: %v\n", err)
		}

		slog.Info("project initialized", "project", projectName)
		fmt.Printf("Initialized agency project: %s\n", projectName)
		fmt.Println()
		fmt.Println("To set up the tmux popup keybinding, add this to your tmux.conf:")
		fmt.Println("  bind n run-shell \"tmux display-popup -E -w 60 -h 10 'agency new --popup'\"")
		return nil
	},
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage workspaces",
}

// topLevelNewCmd is the top-level "agency new" command that creates a workspace.
// With --popup it runs the interactive TUI create form (suitable for tmux popups).
// Without --popup it accepts name and branch as positional arguments.
var topLevelNewCmd = &cobra.Command{
	Use:   "new [name] [branch]",
	Short: "Create a new workspace (interactive with --popup)",
	RunE: func(cmd *cobra.Command, args []string) error {
		popup, _ := cmd.Flags().GetBool("popup")
		if popup {
			return tui.RunPopup()
		}

		// Non-popup: require name and branch args.
		if len(args) < 2 {
			return errors.New("usage: agency new <name> <branch>  (or agency new --popup)")
		}
		name := args[0]
		branch := args[1]

		mgr, err := loadManager()
		if err != nil {
			return err
		}
		ws, err := mgr.Create(context.Background(), name, branch)
		if err != nil {
			return fmt.Errorf("creating workspace: %w", err)
		}
		slog.Info("workspace created", "id", ws.ID, "name", ws.Name, "branch", ws.Branch)
		fmt.Printf("Created workspace %s (%s) for branch %s\n", ws.ID, ws.Name, ws.Branch)
		return nil
	},
}

// topLevelQuitCmd is the top-level "agency quit" command.
// With --popup it runs the interactive quit confirmation (suitable for tmux popups).
var topLevelQuitCmd = &cobra.Command{
	Use:    "quit",
	Short:  "Quit confirmation dialog (interactive with --popup)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		popup, _ := cmd.Flags().GetBool("popup")
		if popup {
			return tui.RunQuitPopup()
		}
		return errors.New("usage: agency quit --popup")
	},
}

// workspaceNewCmd is the "workspace new" subcommand.
var workspaceNewCmd = &cobra.Command{
	Use:   "new <name> <branch>",
	Short: "Create a new workspace for a branch",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		branch := args[1]
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		ws, err := mgr.Create(context.Background(), name, branch)
		if err != nil {
			return fmt.Errorf("creating workspace: %w", err)
		}
		slog.Info("workspace created", "id", ws.ID, "name", ws.Name, "branch", ws.Branch)
		fmt.Printf("Created workspace %s (%s) for branch %s\n", ws.ID, ws.Name, ws.Branch)
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		workspaces := mgr.List()
		if len(workspaces) == 0 {
			fmt.Println("No workspaces found.")
			return nil
		}
		fmt.Printf("%-20s  %-20s  %-30s  %-12s  %s\n", "ID", "NAME", "BRANCH", "STATE", "CREATED")
		fmt.Println(strings.Repeat("-", 100))
		for _, ws := range workspaces {
			fmt.Printf("%-20s  %-20s  %-30s  %-12s  %s\n",
				ws.ID,
				ws.Name,
				ws.Branch,
				string(ws.State),
				ws.CreatedAt.Format("2006-01-02 15:04:05"),
			)
		}
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <workspace-id>",
	Short: "Remove a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		if err := mgr.Remove(context.Background(), id); err != nil {
			return fmt.Errorf("removing workspace: %w", err)
		}
		slog.Info("workspace removed", "id", id)
		fmt.Printf("Removed workspace %s\n", id)
		return nil
	},
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Run garbage collection on stale workspaces",
	RunE: func(cmd *cobra.Command, args []string) error {
		workspaceID, _ := cmd.Flags().GetString("workspace-id")
		if workspaceID != "" {
			mgr, err := loadManager()
			if err != nil {
				return err
			}
			ctx := context.Background()
			ws, ok := mgr.State.Workspaces[workspaceID]
			if !ok {
				return nil // already cleaned up; exit quietly
			}
			dirty, _ := worktree.IsDirty(ws.WorktreePath)
			if ws.SandboxID != "" && mgr.Sandbox != nil {
				_ = mgr.Sandbox.Stop(ctx, ws.SandboxID, 10)
				_ = mgr.Sandbox.Remove(ctx, ws.SandboxID)
			}
			if !dirty {
				if ws.WorktreePath != "" {
					_ = worktree.Remove(mgr.State.BarePath, ws.WorktreePath)
				}
				delete(mgr.State.Workspaces, ws.ID)
			} else {
				ws.State = state.StatePaused
				ws.UpdatedAt = time.Now().UTC()
			}
			return mgr.SaveState()
		}

		force, _ := cmd.Flags().GetBool("force")

		mgr, err := loadManager()
		if err != nil {
			return err
		}

		ctx := context.Background()
		barePath := mgr.State.BarePath

		// Find orphan worktrees (excludes the main development worktree).
		orphanWorktrees, _ := mgr.FindOrphanWorktrees()

		// Collect known sandbox IDs from state.
		knownSandboxIDs := make(map[string]bool)
		for _, ws := range mgr.State.Workspaces {
			if ws.SandboxID != "" {
				knownSandboxIDs[ws.SandboxID] = true
			}
		}

		// Find orphan containers.
		var orphanContainers []string // container IDs
		var orphanContainerNames []string
		if mgr.Sandbox != nil {
			if containers, err := mgr.Sandbox.ListByProject(ctx, mgr.ContainerPrefix()); err == nil {
				for _, c := range containers {
					if !knownSandboxIDs[c.ID] {
						orphanContainers = append(orphanContainers, c.ID)
						orphanContainerNames = append(orphanContainerNames, c.Name)
					}
				}
			}
		}

		total := len(orphanWorktrees) + len(orphanContainers)
		slog.Info("gc scan complete", "orphan_worktrees", len(orphanWorktrees), "orphan_containers", len(orphanContainers))

		if total == 0 {
			fmt.Println("No orphans found.")
		} else {
			fmt.Printf("%-6s  %-10s  %s\n", "TYPE", "KIND", "PATH/NAME")
			fmt.Println(strings.Repeat("-", 60))
			for _, wt := range orphanWorktrees {
				fmt.Printf("%-6s  %-10s  %s\n", "orphan", "worktree", wt.Path)
			}
			for i, name := range orphanContainerNames {
				fmt.Printf("%-6s  %-10s  %s (%s)\n", "orphan", "container", name, orphanContainers[i])
			}
		}

		// Always run git worktree prune.
		pruneCmd := exec.CommandContext(ctx, "git", "-C", barePath, "worktree", "prune")
		pruneCmd.Stdout = os.Stdout
		pruneCmd.Stderr = os.Stderr
		_ = pruneCmd.Run()

		if total == 0 {
			return nil
		}

		// If --force not set, prompt.
		if !force {
			fmt.Printf("Remove %d orphan(s)? (y/n) ", total)
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		// Remove orphan worktrees.
		for _, wt := range orphanWorktrees {
			if err := worktree.Remove(barePath, wt.Path); err != nil {
				slog.Warn("gc: failed to remove worktree", "path", wt.Path, "error", err)
				fmt.Fprintf(os.Stderr, "warning: removing worktree %s: %v\n", wt.Path, err)
			} else {
				fmt.Printf("Removed worktree %s\n", wt.Path)
			}
		}

		// Remove orphan containers.
		for i, cid := range orphanContainers {
			if mgr.Sandbox != nil {
				if err := mgr.Sandbox.Remove(ctx, cid); err != nil {
					slog.Warn("gc: failed to remove container", "name", orphanContainerNames[i], "error", err)
					fmt.Fprintf(os.Stderr, "warning: removing container %s: %v\n", orphanContainerNames[i], err)
				} else {
					fmt.Printf("Removed container %s\n", orphanContainerNames[i])
				}
			}
		}

		return nil
	},
}

// loadManager is a shared helper that finds the project directory, loads
// configuration, and constructs a workspace Manager.
func loadManager() (*workspace.Manager, error) {
	projectDir, err := project.FindProjectDir()
	if err != nil {
		return nil, err
	}
	slog.Debug("loadManager", "projectDir", projectDir)

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	mgr, err := workspace.NewManager(projectDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("initializing workspace manager: %w", err)
	}

	return mgr, nil
}

func init() {
	rootCmd.PersistentFlags().String("log-level", "info", "Log level: debug, info, warn, error")
	initCmd.Flags().String("remote", "", "Remote repository URL")
	gcCmd.Flags().Bool("force", false, "Force garbage collection without confirmation")
	gcCmd.Flags().String("workspace-id", "", "Run single-workspace cleanup (used by EXIT trap)")
	topLevelNewCmd.Flags().Bool("popup", false, "Run interactive create form (for use in tmux popup)")
	topLevelQuitCmd.Flags().Bool("popup", false, "Run interactive quit confirmation (for use in tmux popup)")

	rootCmd.AddCommand(versionCmd, initCmd, workspaceCmd, gcCmd, topLevelNewCmd, topLevelQuitCmd)
	workspaceCmd.AddCommand(workspaceNewCmd, listCmd, rmCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
