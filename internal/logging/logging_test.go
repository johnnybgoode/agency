package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetup_CreatesDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	sessionTime := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)

	cleanup, err := Setup(dir, slog.LevelInfo, sessionTime)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	defer cleanup()

	logDir := filepath.Join(dir, ".agency", "logs")
	if _, err := os.Stat(logDir); err != nil {
		t.Fatalf("log directory not created: %v", err)
	}

	logFile := filepath.Join(logDir, "2026-03-15T10-30-00Z.log")
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestSetup_LevelFiltering(t *testing.T) {
	dir := t.TempDir()
	sessionTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cleanup, err := Setup(dir, slog.LevelWarn, sessionTime)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	slog.Info("this should not appear")
	slog.Warn("this should appear")
	cleanup()

	logFile := filepath.Join(dir, ".agency", "logs", "2026-01-01T00-00-00Z.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "this should not appear") {
		t.Error("info message should have been filtered at warn level")
	}
	if !strings.Contains(content, "this should appear") {
		t.Error("warn message should have been written")
	}
}

func TestSetup_AppendBehavior(t *testing.T) {
	dir := t.TempDir()
	sessionTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// First session write.
	cleanup1, err := Setup(dir, slog.LevelInfo, sessionTime)
	if err != nil {
		t.Fatalf("Setup 1 failed: %v", err)
	}
	slog.Info("first message")
	cleanup1()

	// Second session write with same sessionTime.
	cleanup2, err := Setup(dir, slog.LevelInfo, sessionTime)
	if err != nil {
		t.Fatalf("Setup 2 failed: %v", err)
	}
	slog.Info("second message")
	cleanup2()

	logFile := filepath.Join(dir, ".agency", "logs", "2026-06-01T12-00-00Z.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "first message") {
		t.Error("first message should be present")
	}
	if !strings.Contains(content, "second message") {
		t.Error("second message should be present (appended)")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
		err   bool
	}{
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{" Info ", slog.LevelInfo, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseLevel(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("ParseLevel(%q) should return error", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseLevel(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
