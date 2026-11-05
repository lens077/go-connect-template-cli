package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEnsureWriteAllowedRejectsDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "local.go"), []byte("package local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureWriteAllowed(root, false); err == nil {
		t.Fatal("dirty worktree must block origin and file writes")
	}
	if err := ensureWriteAllowed(root, true); err != nil {
		t.Fatalf("allow-dirty should bypass only the worktree check: %v", err)
	}
}
