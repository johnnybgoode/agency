package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/johnnybgoode/agency/internal/config"
	"github.com/johnnybgoode/agency/internal/session"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tui"
	"github.com/johnnybgoode/agency/internal/worktree"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "agency",
	Short:        "Coding agent session manager",
	SilenceUsage: true,
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

		projectName := filepath.Base(cwd)
		statePath := filepath.Join(cwd, ".tool", "state.json")
		s := state.Default(projectName, filepath.Join(cwd, ".bare"))
		if err := state.Write(statePath, s); err != nil {
			return fmt.Errorf("writing initial state: %w", err)
		}

		// Enforce permissions on the global config file.
		if err := config.EnforceGlobalConfigPerms(config.GlobalConfigPath()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not enforce global config permissions: %v\n", err)
		}

		fmt.Printf("Initialized agency project: %s\n", projectName)
		return nil
	},
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage agent sessions",
}

var newCmd = &cobra.Command{
	Use:   "new <branch>",
	Short: "Create a new session for a branch",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		branch := args[0]
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		sess, err := mgr.Create(context.Background(), branch)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		fmt.Printf("Created session %s for branch %s\n", sess.ID, branch)
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		sessions := mgr.List()
		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}
		fmt.Printf("%-20s  %-30s  %-12s  %s\n", "ID", "BRANCH", "STATE", "CREATED")
		fmt.Println(strings.Repeat("-", 80))
		for _, s := range sessions {
			fmt.Printf("%-20s  %-30s  %-12s  %s\n",
				s.ID,
				s.Branch,
				string(s.State),
				s.CreatedAt.Format("2006-01-02 15:04:05"),
			)
		}
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <session-id>",
	Short: "Remove a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		if err := mgr.Remove(context.Background(), id); err != nil {
			return fmt.Errorf("removing session: %w", err)
		}
		fmt.Printf("Removed session %s\n", id)
		return nil
	},
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Run garbage collection on stale sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")

		mgr, err := loadManager()
		if err != nil {
			return err
		}

		ctx := context.Background()
		barePath := mgr.State.BarePath

		// Collect known worktree paths from state.
		knownWorktrees := make(map[string]bool)
		for _, s := range mgr.State.Sessions {
			if s.WorktreePath != "" {
				knownWorktrees[s.WorktreePath] = true
			}
		}

		// Collect known sandbox IDs from state.
		knownSandboxIDs := make(map[string]bool)
		for _, s := range mgr.State.Sessions {
			if s.SandboxID != "" {
				knownSandboxIDs[s.SandboxID] = true
			}
		}

		// Find orphan worktrees.
		var orphanWorktrees []worktree.WorktreeInfo
		if wts, err := worktree.List(barePath); err == nil {
			for _, wt := range wts {
				if !knownWorktrees[wt.Path] {
					orphanWorktrees = append(orphanWorktrees, wt)
				}
			}
		}

		// Find orphan containers.
		var orphanContainers []string // container IDs
		var orphanContainerNames []string
		prefix := "claude-sb-" + mgr.ProjectName
		if mgr.Sandbox != nil {
			if containers, err := mgr.Sandbox.ListByProject(ctx, prefix); err == nil {
				for _, c := range containers {
					if !knownSandboxIDs[c.ID] {
						orphanContainers = append(orphanContainers, c.ID)
						orphanContainerNames = append(orphanContainerNames, c.Name)
					}
				}
			}
		}

		total := len(orphanWorktrees) + len(orphanContainers)

		if total == 0 && len(orphanWorktrees) == 0 && len(orphanContainers) == 0 {
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
				fmt.Fprintf(os.Stderr, "warning: removing worktree %s: %v\n", wt.Path, err)
			} else {
				fmt.Printf("Removed worktree %s\n", wt.Path)
			}
		}

		// Remove orphan containers.
		for i, cid := range orphanContainers {
			if mgr.Sandbox != nil {
				if err := mgr.Sandbox.Remove(ctx, cid); err != nil {
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
// configuration, and constructs a session Manager.
func loadManager() (*session.Manager, error) {
	projectDir, err := findProjectDir()
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(config.GlobalConfigPath(), config.ProjectConfigPath(projectDir))
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	mgr, err := session.NewManager(projectDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("initializing session manager: %w", err)
	}

	return mgr, nil
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

func init() {
	initCmd.Flags().String("remote", "", "Remote repository URL")
	gcCmd.Flags().Bool("force", false, "Force garbage collection without confirmation")

	rootCmd.AddCommand(versionCmd, initCmd, sessionCmd, gcCmd)
	sessionCmd.AddCommand(newCmd, listCmd, rmCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
