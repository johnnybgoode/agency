package worktree

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{
			name:  "already lowercase alphanumeric",
			input: "feature",
			want:  "feature",
		},
		{
			name:  "uppercase converted to lowercase",
			input: "MyFeature",
			want:  "myfeature",
		},
		{
			name:  "slash replaced with dash",
			input: "agent/fix-login-bug",
			want:  "agent-fix-login-bug",
		},
		{
			name:  "multiple slashes replaced",
			input: "feat/sub/branch",
			want:  "feat-sub-branch",
		},
		{
			name:  "special chars stripped",
			input: "feature@2.0!",
			want:  "feature20",
		},
		{
			name:  "spaces stripped",
			input: "my feature",
			want:  "myfeature",
		},
		{
			name:  "dashes and underscores preserved",
			input: "my-feature_branch",
			want:  "my-feature_branch",
		},
		{
			name:  "truncation at 40 chars",
			input: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghij",
			want:  "abcdefghijklmnopqrstuvwxyz0123456789abcd",
		},
		{
			name:  "exactly 40 chars not truncated",
			input: "abcdefghijklmnopqrstuvwxyz0123456789abcd",
			want:  "abcdefghijklmnopqrstuvwxyz0123456789abcd",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "already slug input",
			input: "fix-login-bug",
			want:  "fix-login-bug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// Verify truncation invariant.
			if len(got) > 40 {
				t.Errorf("Slugify(%q) returned %d chars, max is 40", tt.input, len(got))
			}
		})
	}
}

func TestCreateList(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found, skipping worktree integration test")
	}

	dir := t.TempDir()
	bareDir := filepath.Join(dir, ".bare")

	// Create a bare repo with an initial commit so worktree add works.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init", "--bare", bareDir)
	// Point HEAD to main so that branch creation inside the bare repo works
	// regardless of the system's defaultBranch setting.
	run("git", "-C", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	// We need at least one commit; create a temporary non-bare clone, commit,
	// then push back to bare.
	srcDir := filepath.Join(dir, "src")
	run("git", "clone", bareDir, srcDir)
	run("git", "-C", srcDir, "config", "user.email", "test@test.com")
	run("git", "-C", srcDir, "config", "user.name", "Test")
	run("git", "-C", srcDir, "commit", "--allow-empty", "-m", "init")
	run("git", "-C", srcDir, "push", "origin", "HEAD:main")

	// Now create a worktree from the bare repo.
	wtPath, err := Create(bareDir, "myproject", "agent/test-branch")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// List should include the newly created worktree.
	worktrees, err := List(bareDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, wt := range worktrees {
		if wt.Path == wtPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created worktree %q not found in List output: %v", wtPath, worktrees)
	}

	// Remove the worktree.
	if err := Remove(bareDir, wtPath); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// List should no longer include the removed worktree.
	worktrees, err = List(bareDir)
	if err != nil {
		t.Fatalf("List after Remove failed: %v", err)
	}

	for _, wt := range worktrees {
		if wt.Path == wtPath {
			t.Errorf("removed worktree %q still present in List output", wtPath)
		}
	}
}
