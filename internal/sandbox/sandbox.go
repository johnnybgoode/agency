// Package sandbox manages Docker containers for isolated agent sessions.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var containerIDRe = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// ValidateContainerID returns an error if id is not a valid Docker container ID.
// A valid container ID consists of 12 to 64 lowercase hexadecimal characters.
func ValidateContainerID(id string) error {
	if !containerIDRe.MatchString(id) {
		return fmt.Errorf("invalid container ID %q: must match [a-f0-9]{12,64}", id)
	}
	return nil
}

// CreateOpts holds all options required to create a sandbox container.
type CreateOpts struct {
	Image           string
	Name            string
	WorktreeMount   string   // host path mounted to /app inside the container
	ConfigMount     string   // host path mounted to /etc/agency/config.toml (read-only)
	SharedHomeMount string   // host path mounted read-only at /home/agent/.shared-base
	Env             []string // non-sensitive environment variables in KEY=VALUE form
	EnvFile         string   // path to a file containing sensitive KEY=VALUE pairs (passed via --env-file)
	CapDrop         []string
	CapAdd          []string
	Memory          string // e.g. "4g" — passed as --memory to docker create
	CPUs            int    // e.g. 2 — passed as --cpus to docker create
}

// ContainerInfo is a summary of a running or stopped container.
type ContainerInfo struct {
	ID    string
	Name  string
	State string
}

// Manager shells out to the docker CLI to manage sandbox containers.
type Manager struct{}

// New verifies that docker is installed and returns a Manager ready for use.
// The daemon health check is intentionally skipped at construction time so
// startup is fast; any daemon-reachability error surfaces on the first real
// operation (Create, Start, etc.).
func New() (*Manager, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("docker is not installed")
	}
	slog.Debug("docker binary found", "path", path)
	return &Manager{}, nil
}

// redactArgs returns a copy of args with sensitive -e KEY=VALUE pairs redacted.
// The value portion is replaced with KEY=REDACTED for any key containing
// API_KEY, TOKEN, or SECRET (case-insensitive).
func redactArgs(args []string) []string {
	result := make([]string, len(args))
	copy(result, args)
	for i, arg := range result {
		if i > 0 && result[i-1] == "-e" {
			if idx := strings.Index(arg, "="); idx >= 0 {
				key := arg[:idx]
				upper := strings.ToUpper(key)
				if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") {
					result[i] = key + "=REDACTED"
				}
			}
		}
	}
	return result
}

// docker is a shared helper that runs a docker sub-command and returns the
// trimmed stdout. Any non-zero exit is returned as an error together with the
// combined output so callers have full context.
func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	slog.Debug("docker exec", "args", redactArgs(args))
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		slog.Error("docker command failed", "args", redactArgs(args), "error", err, "output", truncateLog(result, 200))
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, result)
	}
	slog.Debug("docker exec done", "args", redactArgs(args), "output_len", len(result))
	return result, nil
}

// truncateLog returns s truncated to maxLen characters with "..." appended if truncated.
func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// defaultCapDrop is applied when CreateOpts.CapDrop is nil or empty.
var defaultCapDrop = []string{"ALL"}

// defaultCapAdd is applied when CreateOpts.CapAdd is nil or empty.
var defaultCapAdd = []string{
	"CHOWN",
	"SETUID",
	"SETGID",
	"DAC_OVERRIDE",
	"FOWNER",
}

// Create runs `docker create` with the provided options and returns the
// container ID assigned by the daemon.
func (m *Manager) Create(ctx context.Context, opts *CreateOpts) (string, error) {
	slog.Info("creating container", "name", opts.Name, "image", opts.Image)
	capDrop := opts.CapDrop
	if len(capDrop) == 0 {
		capDrop = defaultCapDrop
	}
	capAdd := opts.CapAdd
	if len(capAdd) == 0 {
		capAdd = defaultCapAdd
	}

	args := []string{"create"}

	args = append(args, "--name", opts.Name, "--tty", "--interactive", "--workdir", "/app", "--security-opt", "no-new-privileges:true")

	for _, cap := range capDrop {
		args = append(args, "--cap-drop", cap)
	}
	for _, cap := range capAdd {
		args = append(args, "--cap-add", cap)
	}

	args = append(args, "-v", opts.WorktreeMount+":/app:rw")

	if opts.ConfigMount != "" {
		args = append(args, "-v", opts.ConfigMount+":/etc/agency/config.toml:ro")
	}
	if opts.SharedHomeMount != "" {
		args = append(args, "-v", opts.SharedHomeMount+":/home/agent/.shared-base:ro")
	}

	if opts.Memory != "" {
		args = append(args, "--memory", opts.Memory)
	}
	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(opts.CPUs))
	}

	if opts.EnvFile != "" {
		args = append(args, "--env-file", opts.EnvFile)
	}
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}

	// No CMD override — let the image default (sleep infinity) keep the
	// container alive so docker exec can launch interactive claude sessions.
	args = append(args, opts.Image)

	// Use stdout-only output for docker create to avoid stderr warnings
	// polluting the container ID.
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		// Include stderr in the error for diagnostics.
		return "", fmt.Errorf("docker create: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Start starts a previously created (stopped) container.
func (m *Manager) Start(ctx context.Context, containerID string) error {
	slog.Info("starting container", "container", containerID)
	_, err := m.docker(ctx, "start", containerID)
	return err
}

// Stop stops a running container, waiting up to timeoutSecs before killing it.
func (m *Manager) Stop(ctx context.Context, containerID string, timeoutSecs int) error {
	slog.Info("stopping container", "container", containerID, "timeout", timeoutSecs)
	_, err := m.docker(ctx, "stop", "-t", fmt.Sprintf("%d", timeoutSecs), containerID)
	return err
}

// StopBackground fires `docker stop` without waiting for it to complete.
// The docker daemon processes the stop independently; this returns as soon
// as the docker CLI child process has been launched.
func (m *Manager) StopBackground(ctx context.Context, containerID string, timeoutSecs int) error {
	cmd := exec.CommandContext(ctx, "docker", "stop", "-t", fmt.Sprintf("%d", timeoutSecs), containerID) //nolint:gosec // containerID is an internal docker container ID, not user input
	return cmd.Start()
}

// Remove force-removes a container (equivalent to `docker rm -f`).
func (m *Manager) Remove(ctx context.Context, containerID string) error {
	slog.Info("removing container", "container", containerID)
	_, err := m.docker(ctx, "rm", "-f", containerID)
	return err
}

// IsRunning reports whether the container is in the running state.
func (m *Manager) IsRunning(ctx context.Context, containerID string) (bool, error) {
	out, err := m.docker(ctx, "inspect", "-f", "{{.State.Running}}", containerID)
	if err != nil {
		return false, err
	}
	return out == "true", nil
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

// ListByProject returns all containers (running or stopped) whose names begin
// with prefix. It uses the docker --filter flag for server-side filtering.
func (m *Manager) ListByProject(ctx context.Context, prefix string) ([]ContainerInfo, error) {
	out, err := m.docker(ctx,
		"ps", "-a",
		"--filter", "name="+prefix,
		"--format", "{{.ID}}\t{{.Names}}\t{{.State}}",
	)
	if err != nil {
		return nil, err
	}

	slog.Debug("listing containers by project", "prefix", prefix)
	var containers []ContainerInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		containers = append(containers, ContainerInfo{
			ID:    parts[0],
			Name:  parts[1],
			State: parts[2],
		})
	}
	return containers, nil
}
