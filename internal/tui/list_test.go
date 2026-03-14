package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/johnnybgoode/agency/internal/state"
)

func TestFriendlyError(t *testing.T) {
	tests := []struct {
		name        string
		input       error
		wantNil     bool
		wantContain string
	}{
		{
			name:    "nil passthrough",
			input:   nil,
			wantNil: true,
		},
		{
			name:        "active workspace conflict",
			input:       errors.New("already has an active workspace for this branch"),
			wantContain: "already has an active workspace",
		},
		{
			name:        "git already checked out",
			input:       errors.New("fatal: 'refs/heads/feature' is already checked out at '/path'"),
			wantContain: "already has an active worktree",
		},
		{
			name:        "already exists",
			input:       errors.New("path already exists and is not empty"),
			wantContain: "already exists",
		},
		{
			name:        "docker not running",
			input:       errors.New("docker is not available on this system"),
			wantContain: "docker is not running",
		},
		{
			name:        "docker daemon not running",
			input:       errors.New("docker daemon is not running"),
			wantContain: "docker daemon is not reachable",
		},
		{
			name:        "no such image",
			input:       errors.New("No such image: claude-sandbox:latest"),
			wantContain: "sandbox image not found",
		},
		{
			name:        "conflict with container name",
			input:       errors.New("Conflict. The container name \"/my-container\" is already in use"),
			wantContain: "container with that name already exists",
		},
		{
			name:        "unknown error truncation at newline",
			input:       errors.New("some error occurred\nwith extra git output\nthat should be stripped"),
			wantContain: "some error occurred",
		},
		{
			name:        "unknown error without newline returned as-is",
			input:       errors.New("simple unknown error"),
			wantContain: "simple unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyError(tt.input)

			if tt.wantNil {
				if got != nil {
					t.Errorf("friendlyError(nil) = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("friendlyError returned nil, want non-nil")
			}

			if !strings.Contains(got.Error(), tt.wantContain) {
				t.Errorf("friendlyError(%q).Error() = %q, want to contain %q",
					tt.input.Error(), got.Error(), tt.wantContain)
			}

			// Unknown errors should not contain multi-line content.
			if strings.Contains(got.Error(), "\n") {
				t.Errorf("friendlyError result contains newline: %q", got.Error())
			}
		})
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		t           time.Time
		wantContain string
		wantSuffix  string
	}{
		{
			name:        "30 seconds ago",
			t:           now.Add(-30 * time.Second),
			wantContain: "s ago",
			wantSuffix:  "ago",
		},
		{
			name:        "5 minutes ago",
			t:           now.Add(-5 * time.Minute),
			wantContain: "m ago",
			wantSuffix:  "ago",
		},
		{
			name:        "3 hours ago",
			t:           now.Add(-3 * time.Hour),
			wantContain: "h ago",
			wantSuffix:  "ago",
		},
		{
			name:        "2 days ago",
			t:           now.Add(-2 * 24 * time.Hour),
			wantContain: "d ago",
			wantSuffix:  "ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeTime(tt.t)

			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("relativeTime(%v) = %q, want to contain %q", tt.t, got, tt.wantContain)
			}

			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("relativeTime(%v) = %q, want suffix %q", tt.t, got, tt.wantSuffix)
			}
		})
	}
}

func TestStyledStatus(t *testing.T) {
	allStates := []state.WorkspaceState{
		state.StateCreating,
		state.StateProvisioning,
		state.StateRunning,
		state.StatePaused,
		state.StateCompleting,
		state.StateDone,
		state.StateFailed,
	}

	for _, s := range allStates {
		t.Run(string(s), func(t *testing.T) {
			got := styledStatus(s)
			if got == "" {
				t.Errorf("styledStatus(%q) returned empty string", s)
			}
			// The styled output should contain the state name (ANSI codes wrap it).
			if !strings.Contains(got, string(s)) {
				t.Errorf("styledStatus(%q) = %q, should contain the state name", s, got)
			}
		})
	}
}
