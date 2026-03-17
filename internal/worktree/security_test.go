package worktree

import (
	"os/exec"
	"strings"
	"testing"
)

// --- Issue 8: Branch name injection via leading dash ---

func TestCreate_RejectsLeadingDashBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found")
	}
	_, err := Create("/does-not-matter", "proj", "--flag-injection")
	if err == nil {
		t.Error("Create should reject branch names starting with '-'")
	}
	if !strings.Contains(err.Error(), "-") && !strings.Contains(strings.ToLower(err.Error()), "branch") {
		t.Logf("error returned (ok): %v", err)
	}
}

func TestCreate_RejectsLeadingDashBranchVariants(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found")
	}
	cases := []string{"-", "--", "-evil", "--force", "-c injected=value"}
	for _, branch := range cases {
		_, err := Create("/does-not-matter", "proj", branch)
		if err == nil {
			t.Errorf("Create should reject branch %q, got nil error", branch)
		}
	}
}

// --- Issue 13: Remote URL validation ---

func TestValidateRemoteURL_ValidHTTPS(t *testing.T) {
	valid := []string{
		"https://github.com/user/repo.git",
		"https://gitlab.com/org/project",
		"https://bitbucket.org/team/repo",
	}
	for _, u := range valid {
		if err := validateRemoteURL(u); err != nil {
			t.Errorf("validateRemoteURL(%q) unexpected error: %v", u, err)
		}
	}
}

func TestValidateRemoteURL_ValidSSH(t *testing.T) {
	valid := []string{
		"git@github.com:user/repo.git",
		"git@gitlab.com:group/project.git",
		"ssh://git@github.com/user/repo.git",
	}
	for _, u := range valid {
		if err := validateRemoteURL(u); err != nil {
			t.Errorf("validateRemoteURL(%q) unexpected error: %v", u, err)
		}
	}
}

func TestValidateRemoteURL_ValidLocalPath(t *testing.T) {
	// Local paths are allowed (used in tests and internal setups).
	valid := []string{
		"/tmp/bare-repo",
		"/home/user/projects/myrepo",
	}
	for _, u := range valid {
		if err := validateRemoteURL(u); err != nil {
			t.Errorf("validateRemoteURL(%q) unexpected error: %v", u, err)
		}
	}
}

func TestValidateRemoteURL_RejectsHTTP(t *testing.T) {
	// Issue 13: Plain HTTP must be rejected (no TLS).
	err := validateRemoteURL("http://github.com/user/repo.git")
	if err == nil {
		t.Error("validateRemoteURL should reject http:// (insecure) URLs")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should suggest using https instead, got: %v", err)
	}
}

func TestValidateRemoteURL_RejectsUnknownScheme(t *testing.T) {
	unknown := []string{
		"ftp://example.com/repo.git",
		"file:///tmp/repo",
		"gopher://example.com/repo",
	}
	for _, u := range unknown {
		if err := validateRemoteURL(u); err == nil {
			t.Errorf("validateRemoteURL(%q) should reject unknown scheme, got nil", u)
		}
	}
}

func TestValidateRemoteURL_EmptyIsAllowed(t *testing.T) {
	// Empty remote is rejected separately by Init() — validateRemoteURL
	// returns nil for empty so Init's own check handles the error message.
	if err := validateRemoteURL(""); err != nil {
		t.Errorf("validateRemoteURL(\"\") should return nil (empty handled by Init), got: %v", err)
	}
}
