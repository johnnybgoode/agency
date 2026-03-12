package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/johnnybgoode/agency/internal/tui"
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
		fmt.Println("initializing project...")
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
