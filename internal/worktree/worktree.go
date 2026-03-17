// Package worktree manages git worktrees for agent sessions.
package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Info holds metadata about a single git worktree.
type Info struct {
	Path   string
	Branch string
	HEAD   string
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9\-_]`)

// Slugify converts a branch name into a filesystem-safe slug:
// lowercase, / replaced with -, non-alphanumeric/dash/underscore removed,
// truncated to 40 characters.
func Slugify(branch string) string {
	s := strings.ToLower(branch)
	s = strings.ReplaceAll(s, "/", "-")
	s = nonSlugRe.ReplaceAllString(s, "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// Create adds a new git worktree for the given branch inside a bare repository.
// bareDir is the path to the bare repo. projectName is used when constructing
// the destination directory name. The resulting worktree path is returned.
//
// Returns an error if branch starts with '-', which would be misinterpreted as
// a flag by git.
func Create(bareDir, projectName, branch string) (string, error) {
	if strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("branch name cannot start with '-': %q", branch)
	}

	slug := Slugify(branch)
	destPath := filepath.Join(filepath.Dir(bareDir), projectName+"-"+slug)

	if _, err := os.Stat(destPath); err == nil {
		slog.Debug("worktree path collision, adding suffix", "path", destPath)
		// Path already exists — append a 4-char random hex suffix.
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generating random suffix: %w", err)
		}
		destPath = destPath + "-" + hex.EncodeToString(b)
	}

	// Attempt to create the branch; ignore the error in case it already exists.
	// Use -- to prevent branch name from being interpreted as a flag.
	_ = exec.Command("git", "-C", bareDir, "branch", "--", branch).Run()

	// Use -- to prevent branch name and destPath from being interpreted as flags.
	cmd := exec.Command("git", "-C", bareDir, "worktree", "add", "--", destPath, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	slog.Info("worktree created", "branch", branch, "path", destPath)
	return destPath, nil
}

// List returns all worktrees registered with the bare repository at bareDir,
// excluding the bare repo entry itself.
func List(bareDir string) ([]Info, error) {
	cmd := exec.Command("git", "-C", bareDir, "worktree", "list", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	var results []Info
	var current Info
	isBare := false

	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			// Start of a new block — save the previous one if it is not bare.
			if current.Path != "" && !isBare {
				results = append(results, current)
			}
			current = Info{Path: strings.TrimPrefix(line, "worktree ")}
			isBare = false

		case strings.HasPrefix(line, "HEAD "):
			current.HEAD = strings.TrimPrefix(line, "HEAD ")

		case strings.HasPrefix(line, "branch "):
			raw := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(raw, "refs/heads/")

		case line == "bare":
			isBare = true
		}
	}

	// Flush the last block.
	if current.Path != "" && !isBare {
		results = append(results, current)
	}

	slog.Debug("worktree list", "count", len(results))
	return results, nil
}

// Remove forcefully removes the worktree at wtPath from the bare repo.
func Remove(bareDir, wtPath string) error {
	slog.Info("removing worktree", "path", wtPath)
	cmd := exec.Command("git", "-C", bareDir, "worktree", "remove", "--force", wtPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsDirty returns true if the worktree has uncommitted changes or if HEAD
// does not match any remote ref. A worktree is considered clean (not dirty)
// only when git status is empty AND HEAD is reachable by at least one remote
// branch (e.g., origin/main, origin/feat/foo).
func IsDirty(worktreePath string) (bool, error) {
	// Check for uncommitted changes.
	statusOut, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		return true, nil
	}

	// Check if HEAD matches at least one remote ref.
	branchOut, err := exec.Command("git", "-C", worktreePath, "branch", "-r", "--contains", "HEAD").Output()
	if err != nil {
		// Command failed — treat as dirty (safe default).
		return true, nil
	}
	if strings.TrimSpace(string(branchOut)) == "" {
		// HEAD is not on any remote branch — local-only commits exist.
		slog.Debug("worktree dirty: unpushed commits", "path", worktreePath)
		return true, nil
	}

	slog.Debug("worktree clean", "path", worktreePath)
	return false, nil
}

// validateRemoteURL returns an error if remote is not a valid HTTPS or SSH URL.
func validateRemoteURL(remote string) error {
	if remote == "" {
		return nil // empty is handled separately
	}
	// Allow HTTPS and SSH formats
	if strings.HasPrefix(remote, "https://") {
		return nil
	}
	// SSH format: git@host:path or ssh://user@host/path
	if strings.HasPrefix(remote, "git@") || strings.HasPrefix(remote, "ssh://") {
		return nil
	}
	// Local paths (for testing) — allow absolute paths
	if strings.HasPrefix(remote, "/") {
		return nil
	}
	if strings.HasPrefix(remote, "http://") {
		return fmt.Errorf("insecure remote URL %q: use https:// instead of http://", remote)
	}
	return fmt.Errorf("remote URL %q must use https://, git@, or ssh:// scheme", remote)
}

// Init prepares a project directory for use with worktrees. It handles three
// cases depending on the current state of projectDir:
//
//   - .git/ exists  → return an unsupported-operation error with instructions.
//   - .bare/ exists → validate the setup, create .agency/ if absent, return nil.
//   - Neither       → clone remote as a bare repo into .bare/, create an
//     initial worktree named "<basename>-main", and create .agency/.
func Init(projectDir, remote string) error {
	if err := validateRemoteURL(remote); err != nil {
		return err
	}

	gitPath := filepath.Join(projectDir, ".git")
	barePath := filepath.Join(projectDir, ".bare")
	toolPath := filepath.Join(projectDir, ".agency")

	gitInfo, gitErr := os.Stat(gitPath)
	bareInfo, bareErr := os.Stat(barePath)

	// Case 1: existing regular git clone.
	if gitErr == nil && gitInfo.IsDir() {
		slog.Error("existing .git directory found", "path", gitPath)
		return fmt.Errorf(
			"converting existing git clone to bare repo is not yet supported; " +
				"please clone bare manually with: git clone --bare <remote> .bare",
		)
	}

	// Case 2: bare repo already set up.
	if bareErr == nil && bareInfo.IsDir() {
		slog.Info("bare repo exists, skipping clone", "path", barePath)
		if err := os.MkdirAll(toolPath, 0o700); err != nil {
			return fmt.Errorf("creating .agency directory: %w", err)
		}
		return nil
	}

	// Case 3: fresh directory — require a remote.
	if remote == "" {
		return errors.New("remote is required when initializing a new project directory")
	}

	slog.Info("cloning bare repo", "remote", remote, "path", barePath)
	cloneCmd := exec.Command("git", "clone", "--bare", remote, barePath)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone --bare: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	basename := filepath.Base(projectDir)
	mainWT := filepath.Join(projectDir, basename+"-main")

	addCmd := exec.Command("git", "-C", barePath, "worktree", "add", mainWT, "main")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating initial worktree: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	if err := os.MkdirAll(toolPath, 0o700); err != nil {
		return fmt.Errorf("creating .agency directory: %w", err)
	}

	return nil
}
