package workspace

import (
	"strings"
	"testing"

	"github.com/johnnybgoode/agency/internal/state"
)

// --- Issue 1 & 7: buildTrapCmd validates container ID and workspace ID ---

func TestBuildTrapCmd_RejectsInvalidContainerID(t *testing.T) {
	mgr := newTestManager(t)
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		SandboxID: "INVALID_ID_WITH_UPPERCASE",
	}
	_, err := mgr.buildTrapCmd(ws, false)
	if err == nil {
		t.Error("buildTrapCmd should return error for invalid container ID")
	}
}

func TestBuildTrapCmd_RejectsShellInjectionContainerID(t *testing.T) {
	mgr := newTestManager(t)
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		SandboxID: "abc123def456; rm -rf /",
	}
	_, err := mgr.buildTrapCmd(ws, false)
	if err == nil {
		t.Error("buildTrapCmd should reject container ID containing shell metacharacters")
	}
}

func TestBuildTrapCmd_RejectsInvalidWorkspaceID(t *testing.T) {
	mgr := newTestManager(t)
	ws := &state.Workspace{
		ID:        "not-a-valid-ws-id",
		SandboxID: "abc123def456abc1", // valid container ID
	}
	_, err := mgr.buildTrapCmd(ws, false)
	if err == nil {
		t.Error("buildTrapCmd should return error for invalid workspace ID format")
	}
}

func TestBuildTrapCmd_AcceptsValidIDs(t *testing.T) {
	mgr := newTestManager(t)
	ws := &state.Workspace{
		ID:        "ws-aabbccdd",
		SandboxID: "abc123def456abc1", // 16 hex chars — valid
	}
	cmd, err := mgr.buildTrapCmd(ws, false)
	if err != nil {
		t.Fatalf("buildTrapCmd returned unexpected error: %v", err)
	}
	if cmd == "" {
		t.Error("buildTrapCmd returned empty command string")
	}
}

// --- Issue 6: shellEscapeDouble and buildTrapCmd use POSIX-safe quoting ---

func TestShellEscapeDouble(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain string unchanged",
			input: "/home/user/project",
			want:  "/home/user/project",
		},
		{
			name:  "spaces preserved",
			input: "/my projects/cool repo",
			want:  "/my projects/cool repo",
		},
		{
			name:  "double quote escaped",
			input: `/path/with"quote`,
			want:  `/path/with\"quote`,
		},
		{
			name:  "dollar sign escaped",
			input: "/path/$HOME/project",
			want:  `/path/\$HOME/project`,
		},
		{
			name:  "backtick escaped",
			input: "/path/with`cmd`/project",
			want:  `/path/with\` + "`" + `cmd\` + "`" + `/project`,
		},
		{
			name:  "backslash escaped",
			input: `/path/with\backslash`,
			want:  `/path/with\\backslash`,
		},
		{
			name:  "multiple special chars",
			input: `"$HOME"`,
			want:  `\"\$HOME\"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscapeDouble(tc.input)
			if got != tc.want {
				t.Errorf("shellEscapeDouble(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildTrapCmd_ProjectDirSpecialChars(t *testing.T) {
	cases := []struct {
		name       string
		projectDir string
	}{
		{
			name:       "path with spaces",
			projectDir: "/home/user/my projects/cool repo",
		},
		{
			name:       "path with dollar sign",
			projectDir: "/home/$USER/project",
		},
		{
			name:       "path with double quote",
			projectDir: `/home/user/weird"dir`,
		},
		{
			name:       "path with backtick",
			projectDir: "/home/user/dir`whoami`/project",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager(t)
			mgr.ProjectDir = tc.projectDir

			ws := &state.Workspace{
				ID:        "ws-aabbccdd",
				SandboxID: "abc123def456abc1",
			}
			cmd, err := mgr.buildTrapCmd(ws, false)
			if err != nil {
				t.Fatalf("buildTrapCmd returned unexpected error: %v", err)
			}

			// The escaped project dir must appear literally in the command
			// and must not contain any unescaped $ or backtick sequences.
			escaped := shellEscapeDouble(tc.projectDir)
			if !strings.Contains(cmd, escaped) {
				t.Errorf("buildTrapCmd output does not contain escaped dir %q\nfull cmd: %s", escaped, cmd)
			}
		})
	}
}

// --- Issue 8: validateCreate rejects leading-dash names and branches ---

func TestValidateCreate_RejectsLeadingDashBranch(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.validateCreate("My Workspace", "--evil-branch")
	if err == nil {
		t.Error("validateCreate should reject branch starting with '-'")
	}
}

func TestValidateCreate_RejectsLeadingDashName(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.validateCreate("-evil-name", "feature/branch")
	if err == nil {
		t.Error("validateCreate should reject workspace name starting with '-'")
	}
}

func TestValidateCreate_RejectsEmptyBranch(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.validateCreate("My Workspace", "")
	if err == nil {
		t.Error("validateCreate should reject empty branch name")
	}
}

func TestValidateCreate_RejectsEmptyName(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.validateCreate("", "feature/branch")
	if err == nil {
		t.Error("validateCreate should reject empty workspace name")
	}
}

func TestValidateCreate_AcceptsValidInputs(t *testing.T) {
	mgr := newTestManager(t)
	cases := []struct{ name, branch string }{
		{"My Feature", "feature/my-thing"},
		{"Fix Bug", "bugfix/issue-123"},
		{"Refactor", "refactor/clean-up"},
	}
	for _, c := range cases {
		if err := mgr.validateCreate(c.name, c.branch); err != nil {
			t.Errorf("validateCreate(%q, %q) unexpected error: %v", c.name, c.branch, err)
		}
	}
}
