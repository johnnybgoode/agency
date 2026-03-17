package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnnybgoode/agency/internal/state"
)

// ---- Helper: atomicCopy ----

func TestAtomicCopy_CreatesFileWithContent(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "subdir", "dst.txt")
	if err := atomicCopy(src, dst, 0o644); err != nil {
		t.Fatalf("atomicCopy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// ---- Helper: sameContent ----

func TestSameContent_ReturnsTrueForIdenticalFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	_ = os.WriteFile(a, []byte("same"), 0o644)
	_ = os.WriteFile(b, []byte("same"), 0o644)
	same, err := sameContent(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("expected same=true for identical files")
	}
}

func TestSameContent_ReturnsFalseForDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	_ = os.WriteFile(a, []byte("aaa"), 0o644)
	_ = os.WriteFile(b, []byte("bbb"), 0o644)
	same, err := sameContent(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Error("expected same=false for different files")
	}
}

// ---- FindByName ----

func TestFindByName_ReturnsWorkspaceOnCaseInsensitiveMatch(t *testing.T) {
	mgr := &Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{
				"ws-1": {ID: "ws-1", Name: "Alpha"},
			},
		},
	}
	ws := mgr.FindByName("alpha")
	if ws == nil {
		t.Fatal("expected to find workspace, got nil")
	}
	if ws.ID != "ws-1" {
		t.Errorf("got ID %q, want %q", ws.ID, "ws-1")
	}
}

func TestFindByName_ReturnsNilWhenNotFound(t *testing.T) {
	mgr := &Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{
				"ws-1": {ID: "ws-1", Name: "Alpha"},
			},
		},
	}
	if ws := mgr.FindByName("beta"); ws != nil {
		t.Errorf("expected nil, got %+v", ws)
	}
}

// ---- syncFile ----

type syncFileCase struct {
	name          string
	containerData []byte
	containerAge  time.Duration // relative to now; positive = newer than host
	hostData      []byte        // nil = file doesn't exist on host
	hostAge       time.Duration // relative to now; positive = newer than container
	force         bool
	dryRun        bool
	wantStatus    string // "copied", "skipped", "unchanged"
}

func TestSyncFile_FileStatuses(t *testing.T) {
	now := time.Now()
	cases := []syncFileCase{
		{
			name:          "new file",
			containerData: []byte("new"),
			hostData:      nil,
			wantStatus:    "copied",
		},
		{
			name:          "identical file",
			containerData: []byte("same"),
			hostData:      []byte("same"),
			wantStatus:    "unchanged",
		},
		{
			name:          "container newer",
			containerData: []byte("newer"),
			containerAge:  2 * time.Hour,
			hostData:      []byte("older"),
			hostAge:       0,
			wantStatus:    "copied",
		},
		{
			name:          "host newer without force",
			containerData: []byte("old-container"),
			containerAge:  0,
			hostData:      []byte("new-host"),
			hostAge:       2 * time.Hour,
			force:         false,
			wantStatus:    "skipped",
		},
		{
			name:          "host newer with force",
			containerData: []byte("old-container"),
			containerAge:  0,
			hostData:      []byte("new-host"),
			hostAge:       2 * time.Hour,
			force:         true,
			wantStatus:    "copied",
		},
		{
			name:          "dry-run new file not written",
			containerData: []byte("data"),
			hostData:      nil,
			dryRun:        true,
			wantStatus:    "copied", // status says copied but file not written to disk
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			hostHome := t.TempDir()
			mgr := &Manager{}

			srcPath := filepath.Join(tmpDir, "test.txt")
			srcMtime := now.Add(tc.containerAge)
			if err := os.WriteFile(srcPath, tc.containerData, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(srcPath, srcMtime, srcMtime); err != nil {
				t.Fatal(err)
			}

			if tc.hostData != nil {
				hostPath := filepath.Join(hostHome, "test.txt")
				hostMtime := now.Add(tc.hostAge)
				if err := os.WriteFile(hostPath, tc.hostData, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(hostPath, hostMtime, hostMtime); err != nil {
					t.Fatal(err)
				}
			}

			info, err := os.Stat(srcPath)
			if err != nil {
				t.Fatal(err)
			}
			opts := SyncOpts{Force: tc.force, DryRun: tc.dryRun}
			result := &SyncResult{}
			mgr.syncFile(srcPath, info, tmpDir, hostHome, opts, result)

			switch tc.wantStatus {
			case "copied":
				if len(result.Copied) != 1 {
					t.Errorf("expected 1 copied, got copied=%v skipped=%v unchanged=%v errors=%v",
						result.Copied, result.Skipped, result.Unchanged, result.Errors)
				}
				if !tc.dryRun {
					hostPath := filepath.Join(hostHome, "test.txt")
					if _, err := os.Stat(hostPath); os.IsNotExist(err) {
						t.Error("expected file to be written on host, but it wasn't")
					}
				}
				if tc.dryRun && tc.hostData == nil {
					hostPath := filepath.Join(hostHome, "test.txt")
					if _, err := os.Stat(hostPath); err == nil {
						t.Error("dry-run: file should not have been written to host")
					}
				}
			case "skipped":
				if len(result.Skipped) != 1 {
					t.Errorf("expected 1 skipped, got copied=%v skipped=%v unchanged=%v errors=%v",
						result.Copied, result.Skipped, result.Unchanged, result.Errors)
				}
			case "unchanged":
				if len(result.Unchanged) != 1 {
					t.Errorf("expected 1 unchanged, got copied=%v skipped=%v unchanged=%v errors=%v",
						result.Copied, result.Skipped, result.Unchanged, result.Errors)
				}
			}
		})
	}
}

// TestSyncFile_DenylistExcludesTopLevelDirs verifies that files under
// denylisted top-level directories are silently ignored.
func TestSyncFile_DenylistExcludesTopLevelDirs(t *testing.T) {
	for _, denied := range []string{".cache", ".npm", ".nvm", ".local", "subagents", ".shared-base"} {
		t.Run(denied, func(t *testing.T) {
			tmpDir := t.TempDir()
			hostHome := t.TempDir()
			mgr := &Manager{}

			deniedDir := filepath.Join(tmpDir, denied)
			if err := os.MkdirAll(deniedDir, 0o750); err != nil {
				t.Fatal(err)
			}
			srcPath := filepath.Join(deniedDir, "file.txt")
			if err := os.WriteFile(srcPath, []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(srcPath)
			if err != nil {
				t.Fatal(err)
			}

			result := &SyncResult{}
			mgr.syncFile(srcPath, info, tmpDir, hostHome, SyncOpts{}, result)

			if len(result.Copied)+len(result.Skipped)+len(result.Unchanged) > 0 {
				t.Errorf("denied dir %q: expected no results, got copied=%v skipped=%v unchanged=%v",
					denied, result.Copied, result.Skipped, result.Unchanged)
			}
		})
	}
}

// TestSyncHome_ErrorsOnMissingWorkspace verifies that SyncHome returns an error
// when the workspace ID is not found in state.
func TestSyncHome_ErrorsOnMissingWorkspace(t *testing.T) {
	mgr := &Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{},
		},
	}
	_, err := mgr.SyncHome(context.TODO(), "nonexistent", SyncOpts{})
	if err == nil {
		t.Error("expected error for missing workspace, got nil")
	}
}

// TestSyncHome_ErrorsOnMissingSandboxID verifies that SyncHome returns an error
// when the workspace has no SandboxID set.
func TestSyncHome_ErrorsOnMissingSandboxID(t *testing.T) {
	mgr := &Manager{
		State: &state.State{
			Workspaces: map[string]*state.Workspace{
				"ws-1": {ID: "ws-1", Name: "test", SandboxID: ""},
			},
		},
	}
	_, err := mgr.SyncHome(context.TODO(), "ws-1", SyncOpts{})
	if err == nil {
		t.Error("expected error for missing SandboxID, got nil")
	}
}
