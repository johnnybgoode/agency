package tui

import (
	"errors"
	"strings"
	"testing"
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
