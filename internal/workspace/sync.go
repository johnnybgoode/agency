package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/johnnybgoode/agency/internal/state"
)

// SyncOpts controls sync behavior.
type SyncOpts struct {
	Force  bool
	DryRun bool
}

// SyncResult summarizes the outcome of a SyncHome call.
type SyncResult struct {
	Copied    []string // files copied (new or updated)
	Skipped   []string // files skipped because host was newer
	Unchanged []string // files with identical content
	Errors    []SyncError
}

// SyncError records a per-file error during sync.
type SyncError struct {
	Path string
	Err  error
}

// syncDenylist is the set of top-level directory names under /home/agent/ that
// are never synced back to the host. These are ephemeral or very large paths
// that agents install on container start.
var syncDenylist = map[string]bool{
	".shared-base": true,
	".cache":       true,
	".npm":         true,
	".nvm":         true,
	".local":       true,
	"subagents":    true,
}

// SyncHome copies files from the agent sandbox's /home/agent/ directory back
// to the host's .agency/home/ directory, applying timestamp-based conflict
// resolution. Works on running sandboxes via docker sandbox exec + tar pipe.
func (m *Manager) SyncHome(ctx context.Context, wsID string, opts SyncOpts) (*SyncResult, error) {
	ws, ok := m.State.Workspaces[wsID]
	if !ok {
		return nil, fmt.Errorf("workspace %s not found", wsID)
	}
	if m.State.SandboxID == "" {
		return nil, fmt.Errorf("no project sandbox available")
	}
	if m.Sandbox == nil {
		return nil, fmt.Errorf("docker is not available")
	}
	// Keep ws reference alive (the variable is used in the error message only).
	_ = ws

	hostHome := filepath.Join(m.ProjectDir, ".agency", "home")

	// Copy sandbox's /home/agent/ to a temp directory via tar pipe.
	// docker sandbox exec <name> tar cf - -C /home/agent/ . | tar xf - -C <tmpDir>
	tmpDir, err := os.MkdirTemp("", "agency-sync-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarCmd := exec.CommandContext(ctx, "docker", "sandbox", "exec", m.State.SandboxID, "tar", "cf", "-", "-C", "/home/agent/", ".") //nolint:gosec // SandboxID is validated via state.ValidateSandboxName before storage
	extractCmd := exec.CommandContext(ctx, "tar", "xf", "-", "-C", tmpDir)

	pipe, err := tarCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating tar pipe: %w", err)
	}
	extractCmd.Stdin = pipe

	if err := tarCmd.Start(); err != nil {
		return nil, fmt.Errorf("starting tar from sandbox: %w", err)
	}
	if err := extractCmd.Start(); err != nil {
		_ = tarCmd.Process.Kill()
		return nil, fmt.Errorf("starting tar extract: %w", err)
	}

	tarErr := tarCmd.Wait()
	extractErr := extractCmd.Wait()
	if tarErr != nil {
		return nil, fmt.Errorf("tar from sandbox: %w", tarErr)
	}
	if extractErr != nil {
		return nil, fmt.Errorf("tar extract: %w", extractErr)
	}

	result := &SyncResult{}

	err = filepath.Walk(tmpDir, func(srcPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			result.Errors = append(result.Errors, SyncError{Path: srcPath, Err: walkErr})
			return nil
		}
		if info.IsDir() {
			return nil
		}
		m.syncFile(srcPath, info, tmpDir, hostHome, opts, result)
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("walking temp dir: %w", err)
	}

	return result, nil
}

// syncFile processes a single file discovered during the SyncHome walk.
func (m *Manager) syncFile(srcPath string, info os.FileInfo, tmpDir, hostHome string, opts SyncOpts, result *SyncResult) {
	relPath, err := filepath.Rel(tmpDir, srcPath)
	if err != nil {
		result.Errors = append(result.Errors, SyncError{Path: srcPath, Err: err})
		return
	}

	// Check if the top-level component is in the denylist.
	topLevel := strings.SplitN(relPath, string(filepath.Separator), 2)[0]
	if syncDenylist[topLevel] {
		slog.Debug("sync: skipping denylisted path", "path", relPath)
		return
	}

	hostPath := filepath.Join(hostHome, relPath)

	hostInfo, statErr := os.Stat(hostPath)
	if os.IsNotExist(statErr) {
		// New file.
		if !opts.DryRun {
			if err := atomicCopy(srcPath, hostPath, info.Mode()); err != nil {
				result.Errors = append(result.Errors, SyncError{Path: relPath, Err: err})
				return
			}
		}
		result.Copied = append(result.Copied, relPath)
		return
	}
	if statErr != nil {
		result.Errors = append(result.Errors, SyncError{Path: relPath, Err: statErr})
		return
	}

	// File exists on host — compare content.
	same, err := sameContent(srcPath, hostPath)
	if err != nil {
		result.Errors = append(result.Errors, SyncError{Path: relPath, Err: err})
		return
	}
	if same {
		result.Unchanged = append(result.Unchanged, relPath)
		return
	}

	// Different content — compare mtimes.
	containerMtime := info.ModTime()
	hostMtime := hostInfo.ModTime()

	if opts.Force || containerMtime.After(hostMtime) {
		if !opts.DryRun {
			if err := atomicCopy(srcPath, hostPath, info.Mode()); err != nil {
				result.Errors = append(result.Errors, SyncError{Path: relPath, Err: err})
				return
			}
		}
		result.Copied = append(result.Copied, relPath)
	} else {
		slog.Debug("sync: skipping host-newer file", "path", relPath, "host_mtime", hostMtime, "container_mtime", containerMtime)
		result.Skipped = append(result.Skipped, relPath)
	}
}

// FindByName returns the workspace whose Name matches name (case-insensitive),
// or nil if not found.
func (m *Manager) FindByName(name string) *state.Workspace {
	for _, ws := range m.State.Workspaces {
		if strings.EqualFold(ws.Name, name) {
			return ws
		}
	}
	return nil
}

// atomicCopy writes src to dst atomically using a .tmp suffix + rename.
// Parent directories are created as needed.
func atomicCopy(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("creating parent dirs: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// sameContent reports whether the files at a and b have identical SHA-256 digests.
func sameContent(a, b string) (bool, error) {
	ha, err := fileHash(a)
	if err != nil {
		return false, err
	}
	hb, err := fileHash(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// fileHash returns the hex-encoded SHA-256 digest of the file at path.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
