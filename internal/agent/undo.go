package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// snapshotWorkspace creates a restore point before mutations. Prefers a git
// shadow commit (no working-tree disruption); falls back to a plain tar-like
// copy of tracked text files into ~/.whis/undo/<stamp>/.
func snapshotWorkspace(root string) error {
	if isGit(root) {
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			return fallbackSnapshot(root)
		}
		id := fmt.Sprintf("whis-shadow-%s", time.Now().Format("150405.000"))
		c := exec.Command("git", "commit", "-m", id, "--no-verify", "--allow-empty")
		c.Dir = root
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=whis", "GIT_AUTHOR_EMAIL=whis@local",
			"GIT_COMMITTER_NAME=whis", "GIT_COMMITTER_EMAIL=whis@local")
		if err := c.Run(); err != nil {
			// nothing staged or commit failed; still fine
			return nil
		}
		return nil
	}
	return fallbackSnapshot(root)
}

// Undo rolls back to the last restore point.
func Undo(a *Agent) string {
	root := a.Root
	if isGit(root) {
		// find last whis-shadow commit and hard-reset the *files* changed since
		c := exec.Command("git", "log", "--grep=whis-shadow-", "-1", "--format=%H")
		c.Dir = root
		out, err := c.Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return "no whis snapshot found"
		}
		// Undo strategy: soft approach — diff working tree vs the snapshot and
		// restore only paths present in it, without touching newer commits.
		// Simpler robust approach: reset --hard to pre-shadow parent is too
		// destructive; instead checkout the shadow's parent tree over the worktree.
		parent := strings.TrimSpace(string(out)) + "^"
		r := exec.Command("git", "checkout", parent, "--", ".")
		r.Dir = root
		if err := r.Run(); err != nil {
			return "undo failed: " + err.Error()
		}
		return "rolled back to snapshot"
	}
	return fallbackUndo(root)
}

func isGit(root string) bool {
	st, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

func fallbackSnapshot(root string) error {
	dst := filepath.Join(undoDir(), time.Now().Format("20060102-150405.000"))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return copyTree(root, dst)
}

func fallbackUndo(root string) string {
	dirs, err := os.ReadDir(undoDir())
	if err != nil || len(dirs) == 0 {
		return "no snapshots to undo"
	}
	latest := dirs[len(dirs)-1]
	src := filepath.Join(undoDir(), latest.Name())
	if err := restoreTree(src, root); err != nil {
		return "undo failed: " + err.Error()
	}
	return "restored snapshot " + latest.Name()
}

func undoDir() string {
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, ".whis", "undo")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// copyTree copies source files (skipping junk dirs) preserving relative paths.
func copyTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", ".venv", "vendor", "__pycache__", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, b, 0o644)
	})
}

// restoreTree copies snapshot files back over the workspace.
func restoreTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
