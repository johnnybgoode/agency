package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFakeDocker writes a shell script that records its arguments and returns
// canned exit codes based on the subcommand. Returns a Manager using the
// fake binary and the path to the args-log file.
func newFakeDocker(t *testing.T, imageExists bool) (mgr *Manager, argsLogFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	imageInspectExit := "0"
	if !imageExists {
		imageInspectExit = "1"
	}

	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"` + "\n" +
		`shift` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  image)` + "\n" +
		`    case "$1" in` + "\n" +
		`      inspect) exit ` + imageInspectExit + `;;` + "\n" +
		`    esac;;` + "\n" +
		`  build) exit 0;;` + "\n" +
		`  create) echo "sha256:fakecontainerid"; exit 0;;` + "\n" +
		`  start)  exit 0;;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	// Patch exec.LookPath by prepending the fake binary's directory to PATH.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return &Manager{}, argsFile
}

// readCalls reads the recorded docker subcommands from the log file.
func readCalls(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		return nil
	}
	var cmds []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			cmds = append(cmds, fields[0])
		}
	}
	return cmds
}

func TestImageExists_ReturnsFalseWhenNotFound(t *testing.T) {
	m, _ := newFakeDocker(t, false)
	exists, err := m.ImageExists(context.Background(), "agency:latest")
	if err != nil {
		t.Fatalf("ImageExists returned unexpected error: %v", err)
	}
	if exists {
		t.Error("ImageExists should return false when image is not found")
	}
}

func TestImageExists_ReturnsTrueWhenFound(t *testing.T) {
	m, _ := newFakeDocker(t, true)
	exists, err := m.ImageExists(context.Background(), "agency:latest")
	if err != nil {
		t.Fatalf("ImageExists returned unexpected error: %v", err)
	}
	if !exists {
		t.Error("ImageExists should return true when image exists")
	}
}

func TestEnsureImage_BuildsWhenImageMissing(t *testing.T) {
	m, argsFile := newFakeDocker(t, false)

	// Provide a minimal build context directory with a Dockerfile.
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "entrypoint.sh"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	buildFS := os.DirFS(ctxDir)

	if err := m.EnsureImage(context.Background(), "agency:latest", buildFS); err != nil {
		t.Fatalf("EnsureImage returned unexpected error: %v", err)
	}

	calls := readCalls(t, argsFile)
	buildFound := false
	for _, c := range calls {
		if c == "build" {
			buildFound = true
		}
	}
	if !buildFound {
		t.Errorf("expected docker build to be called; got calls: %v", calls)
	}
}

func TestEnsureImage_SkipsBuildWhenImageExists(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	if err := m.EnsureImage(context.Background(), "agency:latest", nil); err != nil {
		t.Fatalf("EnsureImage returned unexpected error: %v", err)
	}

	calls := readCalls(t, argsFile)
	for _, c := range calls {
		if c == "build" {
			t.Errorf("docker build should not be called when image already exists; calls: %v", calls)
		}
	}
}

func TestEnsureImage_ReturnsErrorWhenMissingAndNoFS(t *testing.T) {
	m, _ := newFakeDocker(t, false)

	err := m.EnsureImage(context.Background(), "agency:latest", nil)
	if err == nil {
		t.Error("expected error when image is missing and no build context provided")
	}
}

// readArgsLog reads the full raw content of the docker args log file.
func readArgsLog(t *testing.T, argsFile string) string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestCopyFrom_CallsDockerCpWithCorrectArgs(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	err := m.CopyFrom(context.Background(), "abc123", "/home/agent/.", "/tmp/dest")
	if err != nil {
		t.Fatalf("CopyFrom returned unexpected error: %v", err)
	}

	log := readArgsLog(t, argsFile)
	want := "cp abc123:/home/agent/. /tmp/dest"
	if !strings.Contains(log, want) {
		t.Errorf("expected docker args to contain %q, but got:\n%s", want, log)
	}
}

func TestCreate_SharedHomeMount_AddsReadOnlyVolumeArg(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:           "agency:latest",
		Name:            "test",
		WorktreeMount:   "/app",
		SharedHomeMount: "/host/shared",
	})
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	log := readArgsLog(t, argsFile)
	want := "-v /host/shared:/home/agent/.shared-base:ro"
	if !strings.Contains(log, want) {
		t.Errorf("expected docker args to contain %q, but got:\n%s", want, log)
	}
}

func TestCreate_SharedHomeMount_OmittedWhenEmpty(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	_, err := m.Create(context.Background(), &CreateOpts{
		Image:           "agency:latest",
		Name:            "test",
		WorktreeMount:   "/app",
		SharedHomeMount: "",
	})
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if strings.Contains(log, "shared-base") {
		t.Errorf("expected docker args NOT to contain 'shared-base', but got:\n%s", log)
	}
}
