package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/johnnybgoode/agency/internal/state"
	"github.com/johnnybgoode/agency/internal/tui"
	"github.com/johnnybgoode/agency/internal/worktree"
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
		fmt.Printf("creating session for branch: %s\n", args[0])
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("listing sessions...")
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <session-id>",
	Short: "Remove a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("removing session: %s\n", args[0])
		return nil
	},
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Run garbage collection on stale sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("running garbage collection...")
		return nil
	},
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
