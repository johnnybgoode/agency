// Package sandbox manages Docker sandboxes (MicroVMs) for isolated agent sessions.
package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnnybgoode/agency/internal/state"
)

// ValidateSandboxName returns an error if name is not a valid Docker sandbox name.
func ValidateSandboxName(name string) error {
	return state.ValidateSandboxName(name)
}

// SandboxInfo is a summary of a Docker sandbox (MicroVM).
//
//nolint:revive // SandboxInfo is intentional: avoids ambiguity when imported as sandbox.SandboxInfo.
type SandboxInfo struct {
	Name       string `json:"name"`
	Status     string `json:"status"`      // "running", "stopped" — note: status alone is unreliable
	SocketPath string `json:"socket_path"` // non-empty only when the VM is actually running
}

// IsRunning reports whether this sandbox's VM is actually running.
// The status field can report "running" even when the VM is gone;
// the presence of a socket_path is the reliable indicator.
func (s *SandboxInfo) IsRunning() bool {
	return s.SocketPath != ""
}

// Manager shells out to the docker CLI to manage Docker sandboxes.
type Manager struct{}

// New verifies that docker is installed and that sandbox support is available,
// then returns a Manager ready for use.
func New() (*Manager, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker is not installed")
	}
	slog.Debug("docker binary found", "path", path)

	cmd := exec.Command("docker", "sandbox", "version")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker sandbox support not available: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return &Manager{}, nil
}

// docker is a shared helper that runs a docker sub-command and returns the
// trimmed stdout. Any non-zero exit is returned as an error together with the
// combined output so callers have full context.
func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	slog.Debug("docker exec", "args", args)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		slog.Error("docker command failed", "args", args, "error", err, "output", truncateLog(result, 200))
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, truncateLog(result, 200))
	}
	slog.Debug("docker exec done", "args", args, "output_len", len(result))
	return result, nil
}

// truncateLog returns s truncated to maxLen characters with "..." appended if truncated.
func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Ensure returns the name of a running sandbox, starting or creating as needed:
//   - running  → return immediately
//   - stopped  → start via `docker sandbox run`
//   - absent   → create via `docker sandbox create`
func (m *Manager) Ensure(ctx context.Context, name, projectDir, image string) (string, error) {
	slog.Debug("ensure sandbox", "name", name, "projectDir", projectDir, "image", image)
	info, err := m.FindByName(ctx, name)
	if err != nil {
		return "", fmt.Errorf("finding sandbox %q: %w", name, err)
	}

	if info != nil {
		slog.Debug("sandbox found", "name", info.Name, "status", info.Status, "socket_path", info.SocketPath, "is_running", info.IsRunning())
		if info.IsRunning() {
			slog.Info("sandbox already running", "name", name)
			return info.Name, nil
		}
		// Sandbox exists but is stopped — start it detached.
		slog.Info("starting stopped sandbox", "name", name, "status", info.Status, "socket_path", info.SocketPath)
		_, err = m.docker(ctx, "sandbox", "run", "-d", name)
		if err != nil {
			return "", fmt.Errorf("starting sandbox %q: %w", name, err)
		}
		return info.Name, nil
	}

	slog.Info("creating sandbox (not found in ls)", "name", name, "image", image, "projectDir", projectDir)
	_, err = m.docker(ctx, "sandbox", "create", "--name", name, "-t", image, "claude", projectDir)
	if err != nil {
		return "", fmt.Errorf("creating sandbox %q: %w", name, err)
	}
	return name, nil
}

// sandboxListOutput is the JSON structure returned by `docker sandbox ls --json`.
type sandboxListOutput struct {
	VMs []SandboxInfo `json:"vms"`
}

// ListRetryDelay is the delay before retrying a failed `docker sandbox ls`.
// Exported so tests can set it to zero to avoid slow retries.
var ListRetryDelay = 2 * time.Second

// FindByName returns the SandboxInfo for the sandbox with the given name, or
// nil if no matching sandbox is found.
//
// The Docker sandbox daemon can transiently fail with "socket path is empty"
// while its internal state settles after a stop. We retry once after a short
// delay to ride out this race.
func (m *Manager) FindByName(ctx context.Context, name string) (*SandboxInfo, error) {
	result, err := m.listSandboxes(ctx)
	if err != nil {
		slog.Warn("sandbox ls --json failed, retrying", "delay", ListRetryDelay, "error", err)
		time.Sleep(ListRetryDelay)
		result, err = m.listSandboxes(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing sandboxes: %w", err)
		}
	}

	slog.Debug("sandbox ls parsed", "vm_count", len(result.VMs))
	for i, vm := range result.VMs {
		slog.Debug("sandbox ls entry", "name", vm.Name, "status", vm.Status, "socket_path", vm.SocketPath)
		if vm.Name == name {
			return &result.VMs[i], nil
		}
	}
	slog.Debug("sandbox not found in ls", "wanted", name)
	return nil, nil //nolint:nilnil // nil,nil means "not found" which is the documented API
}

// listSandboxes calls `docker sandbox ls --json` and parses the result.
func (m *Manager) listSandboxes(ctx context.Context) (*sandboxListOutput, error) {
	out, err := m.docker(ctx, "sandbox", "ls", "--json")
	if err != nil {
		slog.Error("sandbox ls --json failed", "error", err)
		return nil, err
	}

	slog.Debug("sandbox ls --json raw output", "output", out)

	var result sandboxListOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		slog.Error("sandbox ls --json parse failed", "error", err, "raw", truncateLog(out, 500))
		return nil, fmt.Errorf("parsing sandbox list JSON: %w", err)
	}
	return &result, nil
}

// ExecArgs returns the argument slice needed to exec a command inside the named
// sandbox. It does NOT run the command — the caller is responsible for execution.
func ExecArgs(sandboxName string, args []string) []string {
	out := make([]string, 0, 4+len(args))
	out = append(out, "docker", "sandbox", "exec", "-it", sandboxName)
	out = append(out, args...)
	return out
}

// Stop stops a running sandbox.
func (m *Manager) Stop(ctx context.Context, sandboxName string) error {
	slog.Info("stopping sandbox", "sandbox", sandboxName)
	out, err := m.docker(ctx, "sandbox", "stop", sandboxName)
	if err != nil {
		slog.Error("sandbox stop failed", "sandbox", sandboxName, "error", err)
	} else {
		slog.Info("sandbox stopped", "sandbox", sandboxName, "output", truncateLog(out, 200))
	}
	return err
}

// StopBackground fires `docker sandbox stop` without waiting for it to complete.
// The docker daemon processes the stop independently; this returns as soon as
// the docker CLI child process has been launched.
func (m *Manager) StopBackground(ctx context.Context, sandboxName string) error {
	cmd := exec.CommandContext(ctx, "docker", "sandbox", "stop", sandboxName)
	return cmd.Start()
}

// Remove removes a sandbox.
func (m *Manager) Remove(ctx context.Context, sandboxName string) error {
	slog.Info("removing sandbox", "sandbox", sandboxName)
	_, err := m.docker(ctx, "sandbox", "rm", sandboxName)
	return err
}

// IsRunning reports whether the sandbox is in the running state.
func (m *Manager) IsRunning(ctx context.Context, sandboxName string) (bool, error) {
	info, err := m.FindByName(ctx, sandboxName)
	if err != nil {
		return false, err
	}
	return info != nil && info.IsRunning(), nil
}

// ImageExists reports whether the named image is present in the local Docker
// image store. A non-zero exit from `docker image inspect` is treated as "not
// found" rather than an error so callers can distinguish missing-image from
// daemon failures via the returned bool.
func (m *Manager) ImageExists(ctx context.Context, image string) (bool, error) {
	slog.Debug("checking image existence", "image", image)
	_, err := m.docker(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		// docker exits non-zero when the image is absent — not a hard error.
		return false, nil
	}
	return true, nil
}

// BuildImage runs `docker build --no-cache -t <image> <contextDir>` to build
// the named image from the supplied build context directory.
// --no-cache is required to prevent Docker from reusing stale intermediate
// layers (e.g. a COPY layer from a previous build with different embedded
// files); the image is only built when it does not already exist, so the
// extra cost is paid at most once per environment.
func (m *Manager) BuildImage(ctx context.Context, image, contextDir string) error {
	slog.Info("building image", "image", image, "context", contextDir)
	out, err := m.docker(ctx, "build", "--no-cache", "-t", image, contextDir)
	slog.Debug("docker build output", "image", image, "output", out)
	return err
}

// EnsureImage checks whether image exists locally and, if not, builds it from
// buildContextFS. buildContextFS must contain a Dockerfile at its root. If
// buildContextFS is nil and the image is absent, an error is returned.
func (m *Manager) EnsureImage(ctx context.Context, image string, buildContextFS fs.FS) error {
	exists, err := m.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("checking image %q: %w", image, err)
	}
	if exists {
		return nil
	}
	if buildContextFS == nil {
		return fmt.Errorf("image %q not found locally and no build context provided; build manually with: docker build -t %s <path-to-agency>/internal/templates/docker", image, image)
	}

	slog.Info("image not found, building from embedded context", "image", image)

	tmpDir, err := os.MkdirTemp("", "agency-build-*")
	if err != nil {
		return fmt.Errorf("creating temp build context: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractFS(tmpDir, buildContextFS); err != nil {
		return fmt.Errorf("extracting build context: %w", err)
	}

	return m.BuildImage(ctx, image, tmpDir)
}

// extractFS copies all files from src into the directory at destDir.
func extractFS(destDir string, src fs.FS) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, path)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o750)
		}
		slog.Debug("extracting build context file", "file", path)
		f, err := src.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		info, err := d.Info()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, f)
		return err
	})
}
