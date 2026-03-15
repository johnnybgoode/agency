// Package logging sets up structured logging for agency using log/slog.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Setup initializes structured logging to a file in <projectDir>/.agency/logs/.
// The filename is derived from sessionTime for append-on-reattach behavior.
// Returns a cleanup function that closes the log file.
func Setup(projectDir string, level slog.Level, sessionTime time.Time) (func(), error) {
	logDir := filepath.Join(projectDir, ".agency", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}

	filename := sessionTime.UTC().Format("2006-01-02T15-04-05Z") + ".log"
	logPath := filepath.Join(logDir, filename)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", logPath, err)
	}

	handler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	cleanup := func() {
		f.Close()
	}
	return cleanup, nil
}

// ParseLevel converts a string level name to slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (valid: debug, info, warn, error)", s)
	}
}
