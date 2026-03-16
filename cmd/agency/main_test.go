package main

import (
	"testing"
)

// TestDefaultLogLevel verifies that the default log level is "info" so that
// workspace operations (create, remove, reconcile) are written to the log
// file during normal use. "error"-only logging silences all routine activity,
// making the log useless for post-mortem debugging.
func TestDefaultLogLevel(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("log-level")
	if flag == nil {
		t.Fatal("--log-level flag not found on root command")
	}
	if flag.DefValue != "info" {
		t.Errorf("default log-level = %q, want %q", flag.DefValue, "info")
	}
}
