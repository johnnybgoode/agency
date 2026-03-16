package agencyimage_test

import (
	"io/fs"
	"testing"

	"github.com/johnnybgoode/agency/internal/agencyimage"
)

func TestBuildContextFS_ContainsDockerfile(t *testing.T) {
	data, err := fs.ReadFile(agencyimage.BuildContextFS, "Dockerfile")
	if err != nil {
		t.Fatalf("Dockerfile not found in embedded build context: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded Dockerfile is empty")
	}
}

func TestBuildContextFS_ContainsEntrypoint(t *testing.T) {
	data, err := fs.ReadFile(agencyimage.BuildContextFS, "docker-entrypoint.sh")
	if err != nil {
		t.Fatalf("docker-entrypoint.sh not found in embedded build context: %v", err)
	}
	if len(data) == 0 {
		t.Error("embedded docker-entrypoint.sh is empty")
	}
}
