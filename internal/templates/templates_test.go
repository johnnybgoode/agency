package templates_test

import (
	"io/fs"
	"testing"

	"github.com/johnnybgoode/agency/internal/templates"
)

func TestSub_DockerContainsDockerfile(t *testing.T) {
	sub, err := templates.Sub("docker")
	if err != nil {
		t.Fatalf("Sub(\"docker\") error: %v", err)
	}
	data, err := fs.ReadFile(sub, "Dockerfile")
	if err != nil {
		t.Fatalf("Dockerfile not found in docker sub-FS: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded Dockerfile is empty")
	}
}

func TestSub_DockerContainsEntrypoint(t *testing.T) {
	sub, err := templates.Sub("docker")
	if err != nil {
		t.Fatalf("Sub(\"docker\") error: %v", err)
	}
	data, err := fs.ReadFile(sub, "entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not found in docker sub-FS: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded entrypoint.sh is empty")
	}
}

func TestSub_UnknownDirReturnsError(t *testing.T) {
	sub, err := templates.Sub("nonexistent")
	if err != nil {
		// fs.Sub itself doesn't error on missing dirs in embed.FS;
		// the error surfaces on first read.
		return
	}
	_, err = fs.ReadFile(sub, "anything")
	if err == nil {
		t.Error("expected error reading from nonexistent subdirectory, got nil")
	}
}
