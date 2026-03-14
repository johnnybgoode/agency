// Package worktree manages git worktrees for agent sessions.
package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// WorktreeInfo holds metadata about a single git worktree.
type WorktreeInfo struct {
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
func Create(bareDir, projectName, branch string) (string, error) {
	slug := Slugify(branch)
	destPath := filepath.Join(filepath.Dir(bareDir), projectName+"-"+slug)

	if _, err := os.Stat(destPath); err == nil {
		// Path already exists — append a 4-char random hex suffix.
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generating random suffix: %w", err)
		}
		destPath = destPath + "-" + hex.EncodeToString(b)
	}

	// Attempt to create the branch; ignore the error in case it already exists.
	_ = exec.Command("git", "-C", bareDir, "branch", branch).Run()

	cmd := exec.Command("git", "-C", bareDir, "worktree", "add", destPath, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return destPath, nil
}

// List returns all worktrees registered with the bare repository at bareDir,
// excluding the bare repo entry itself.
func List(bareDir string) ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "-C", bareDir, "worktree", "list", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	var results []WorktreeInfo
	var current WorktreeInfo
	isBare := false

	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			// Start of a new block — save the previous one if it is not bare.
			if current.Path != "" && !isBare {
				results = append(results, current)
			}
			current = WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
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

	return results, nil
}

// Remove forcefully removes the worktree at wtPath from the bare repo.
func Remove(bareDir, wtPath string) error {
	cmd := exec.Command("git", "-C", bareDir, "worktree", "remove", "--force", wtPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Init prepares a project directory for use with worktrees. It handles three
// cases depending on the current state of projectDir:
//
//   - .git/ exists  → return an unsupported-operation error with instructions.
//   - .bare/ exists → validate the setup, create .tool/ if absent, return nil.
//   - Neither       → clone remote as a bare repo into .bare/, create an
//     initial worktree named "<basename>-main", and create .tool/.
func Init(projectDir, remote string) error {
	gitPath := filepath.Join(projectDir, ".git")
	barePath := filepath.Join(projectDir, ".bare")
	toolPath := filepath.Join(projectDir, ".tool")

	gitInfo, gitErr := os.Stat(gitPath)
	bareInfo, bareErr := os.Stat(barePath)

	// Case 1: existing regular git clone.
	if gitErr == nil && gitInfo.IsDir() {
		return fmt.Errorf(
			"converting existing git clone to bare repo is not yet supported; " +
				"please clone bare manually with: git clone --bare <remote> .bare",
		)
	}

	// Case 2: bare repo already set up.
	if bareErr == nil && bareInfo.IsDir() {
		if err := os.MkdirAll(toolPath, 0o700); err != nil {
			return fmt.Errorf("creating .tool directory: %w", err)
		}
		return nil
	}

	// Case 3: fresh directory — require a remote.
	if remote == "" {
		return fmt.Errorf("remote is required when initializing a new project directory")
	}

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
		return fmt.Errorf("creating .tool directory: %w", err)
	}

	return nil
}
