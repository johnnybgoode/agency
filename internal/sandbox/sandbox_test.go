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
		`  sandbox)` + "\n" +
		`    case "$1" in` + "\n" +
		`      version) exit 0;;` + "\n" +
		`      ls)      echo '{"vms":[]}'; exit 0;;` + "\n" +
		`      create)` + "\n" +
		`        # extract --name value: find index of --name then print next arg` + "\n" +
		`        name=""` + "\n" +
		`        prev=""` + "\n" +
		`        for arg in "$@"; do` + "\n" +
		`          if [ "$prev" = "--name" ]; then name="$arg"; fi` + "\n" +
		`          prev="$arg"` + "\n" +
		`        done` + "\n" +
		`        echo "$name"; exit 0;;` + "\n" +
		`      stop)    exit 0;;` + "\n" +
		`      rm)      exit 0;;` + "\n" +
		`      inspect) echo '{"name":"test","status":"running"}'; exit 0;;` + "\n" +
		`    esac;;` + "\n" +
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

// readArgsLog reads the full raw content of the docker args log file.
func readArgsLog(t *testing.T, argsFile string) string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		return ""
	}
	return string(data)
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

func TestFindByName_ReturnsNilWhenNotFound(t *testing.T) {
	m, _ := newFakeDocker(t, true)

	info, err := m.FindByName(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("FindByName returned unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("FindByName should return nil when sandbox is not found, got: %+v", info)
	}
}

func TestFindByName_ReturnsSandboxWhenFound(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "calls.txt")

	// Build a fake docker that returns a non-empty sandbox list.
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`subcmd="$1"; shift` + "\n" +
		`case "$subcmd" in` + "\n" +
		`  sandbox)` + "\n" +
		`    case "$1" in` + "\n" +
		`      version) exit 0;;` + "\n" +
		`      ls) echo '{"vms":[{"name":"my-sandbox","status":"running"}]}'; exit 0;;` + "\n" +
		`    esac;;` + "\n" +
		`esac` + "\n"

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := &Manager{}
	info, err := m.FindByName(context.Background(), "my-sandbox")
	if err != nil {
		t.Fatalf("FindByName returned unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("FindByName should return a SandboxInfo when sandbox exists, got nil")
	}
	if info.Name != "my-sandbox" {
		t.Errorf("FindByName returned wrong name: got %q, want %q", info.Name, "my-sandbox")
	}
	if info.Status != "running" {
		t.Errorf("FindByName returned wrong status: got %q, want %q", info.Status, "running")
	}
}

func TestEnsure_CreatesNewSandbox(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	name, err := m.Ensure(context.Background(), "my-sandbox", "/projects/foo", "agency:latest")
	if err != nil {
		t.Fatalf("Ensure returned unexpected error: %v", err)
	}
	if name != "my-sandbox" {
		t.Errorf("Ensure returned wrong name: got %q, want %q", name, "my-sandbox")
	}

	log := readArgsLog(t, argsFile)
	if !strings.Contains(log, "sandbox create") {
		t.Errorf("expected 'sandbox create' in docker args, got:\n%s", log)
	}
	if !strings.Contains(log, "--name my-sandbox") {
		t.Errorf("expected '--name my-sandbox' in docker args, got:\n%s", log)
	}
}

func TestStop_CallsSandboxStop(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	err := m.Stop(context.Background(), "my-sandbox")
	if err != nil {
		t.Fatalf("Stop returned unexpected error: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if !strings.Contains(log, "sandbox stop my-sandbox") {
		t.Errorf("expected 'sandbox stop my-sandbox' in docker args, got:\n%s", log)
	}
}

func TestRemove_CallsSandboxRm(t *testing.T) {
	m, argsFile := newFakeDocker(t, true)

	err := m.Remove(context.Background(), "my-sandbox")
	if err != nil {
		t.Fatalf("Remove returned unexpected error: %v", err)
	}

	log := readArgsLog(t, argsFile)
	if !strings.Contains(log, "sandbox rm my-sandbox") {
		t.Errorf("expected 'sandbox rm my-sandbox' in docker args, got:\n%s", log)
	}
}
