package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command in dir with a throwaway identity so commits work.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{
		"-c", "user.name=whis-test", "-c", "user.email=whis@test",
	}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestUndoRestoresChangedFilesOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")

	// baseline the user commits themselves
	mine := filepath.Join(root, "mine.txt")
	if err := os.WriteFile(mine, []byte("user file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "user baseline")

	// agent snapshots, then "edits" a file and drops a new one
	a := NewUnbound(root, true)
	if err := snapshotWorkspace(root); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(mine, []byte("clobbered by agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newF := filepath.Join(root, "generated.txt")
	if err := os.WriteFile(newF, []byte("agent output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// unrelated user commit AFTER the snapshot must survive undo (the user
	// stages only their own file — they never commit the agent's junk)
	other := filepath.Join(root, "later.txt")
	if err := os.WriteFile(other, []byte("committed after snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "later.txt")
	gitRun(t, root, "commit", "-q", "-m", "user work after snapshot")

	msg := Undo(a)
	if !strings.Contains(msg, "undone: restored 1 file(s)") || !strings.Contains(msg, "removed 1 file(s)") {
		t.Fatalf("unexpected undo message: %q", msg)
	}

	b, err := os.ReadFile(mine)
	if err != nil || strings.TrimSpace(string(b)) != "user file" { // TrimSpace: autocrlf may rewrite EOLs on checkout
		t.Fatalf("edited file not restored: %q (%v)", string(b), err)
	}
	if _, err := os.Stat(newF); !os.IsNotExist(err) {
		t.Fatalf("agent-created file was not rolled back")
	}
	b, err = os.ReadFile(other)
	if err != nil || strings.TrimSpace(string(b)) != "committed after snapshot" {
		t.Fatalf("post-snapshot user commit was damaged by undo: %q (%v)", string(b), err)
	}
}

func TestUndoNothingToUndo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "commit", "-q", "-m", "empty", "--allow-empty")

	a := NewUnbound(root, true)
	if msg := Undo(a); !strings.Contains(msg, "no whis snapshot") {
		t.Fatalf("expected no-snapshot message, got: %q", msg)
	}
}
