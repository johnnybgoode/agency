// Package sandbox manages Docker containers for isolated agent sessions.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CreateOpts holds all options required to create a sandbox container.
type CreateOpts struct {
	Image           string
	Name            string
	WorktreeMount   string   // host path mounted to /app inside the container
	ConfigMount     string   // host path mounted to /etc/agency/config.toml (read-only)
	AgentHomeVolume string   // named volume mounted to /home/agent
	Env             []string // environment variables in KEY=VALUE form
	CapDrop         []string
	CapAdd          []string
}

// ContainerInfo is a summary of a running or stopped container.
type ContainerInfo struct {
	ID    string
	Name  string
	State string
}

// Manager shells out to the docker CLI to manage sandbox containers.
type Manager struct{}

// New verifies that docker is installed and the daemon is reachable, then
// returns a Manager ready for use.
func New() (*Manager, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker is not installed")
	}
	m := &Manager{}
	if _, err := m.docker(context.Background(), "info"); err != nil {
		return nil, fmt.Errorf("docker daemon is not running")
	}
	return m, nil
}

// docker is a shared helper that runs a docker sub-command and returns the
// trimmed stdout. Any non-zero exit is returned as an error together with the
// combined output so callers have full context.
func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
	"NET_RAW",
}

// Create runs `docker create` with the provided options and returns the
// container ID assigned by the daemon.
func (m *Manager) Create(ctx context.Context, opts CreateOpts) (string, error) {
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
	if opts.AgentHomeVolume != "" {
		args = append(args, "-v", opts.AgentHomeVolume+":/home/agent")
	}

	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}

	// Entrypoint command: bash -c claude
	args = append(args, opts.Image, "bash", "-c", "claude")

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
	_, err := m.docker(ctx, "start", containerID)
	return err
}

// Stop stops a running container, waiting up to timeoutSecs before killing it.
func (m *Manager) Stop(ctx context.Context, containerID string, timeoutSecs int) error {
	_, err := m.docker(ctx, "stop", "-t", fmt.Sprintf("%d", timeoutSecs), containerID)
	return err
}

// Remove force-removes a container (equivalent to `docker rm -f`).
func (m *Manager) Remove(ctx context.Context, containerID string) error {
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
