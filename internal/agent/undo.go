package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// snapshotWorkspace creates a restore point before mutations. Prefers a git
// marker ref (refs/whis/undo -> a no-pollution commit of the current tree);
// falls back to a plain copy of workspace files into ~/.whis/undo/<stamp>/.
// The ref survives unrelated user commits (unlike log-grep heuristics) and
// lives outside refs/heads so it never shows up in the user's git log.
// Alongside it we save a manifest of the untracked files that existed BEFORE
// the agent ran — untracked files are invisible to git diff, and undo needs
// to know which new files it may delete (only the agent's own).
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
			// nothing staged (clean tree) is fine; real failures fall back
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return fallbackSnapshot(root)
			}
			return nil
		}
		// point the whis undo marker at the snapshot commit
		ref := exec.Command("git", "update-ref", "refs/whis/undo", "HEAD")
		ref.Dir = root
		if err := ref.Run(); err == nil {
			if sha := gitOut(root, "rev-parse", "HEAD"); sha != "" {
				others := gitOut(root, "ls-files", "--others", "--exclude-standard")
				_ = os.WriteFile(filepath.Join(undoDir(), sha+".untracked"), []byte(others), 0o644)
			}
		}
		return nil
	}
	return fallbackSnapshot(root)
}

// undoRef resolves the snapshot to roll back to: the whis marker ref first,
// then (legacy) the newest whis-shadow commit found by log grep.
func undoRef(root string) string {
	if s := gitOut(root, "rev-parse", "--verify", "refs/whis/undo"); s != "" {
		return s
	}
	return gitOut(root, "log", "--grep=whis-shadow-", "-1", "--format=%H")
}

// Undo rolls back the agent's changes SINCE the last snapshot. Only the
// changed paths are touched: the user's own edits, their staged work and
// their HEAD are never moved, and files they committed after the snapshot
// are treated as theirs and skipped.
func Undo(a *Agent) string {
	root := a.Root
	if isGit(root) {
		snap := undoRef(root)
		if snap == "" {
			return "no whis snapshot found — nothing to undo"
		}
		// what differs between the snapshot and the CURRENT WORKTREE — this
		// catches the agent's uncommitted edits AND deletions, which a
		// commit-to-commit diff (HEAD vs snap) would miss entirely.
		var restore []string
		if out := gitOut(root, "diff", "--name-only", "-z", snap); out != "" {
			inSnap := snapTree(root, snap)
			for _, p := range strings.Split(out, "\x00") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if _, ok := inSnap[p]; ok {
					restore = append(restore, p)
				}
				// tracked today but absent from the snapshot = committed by
				// the user afterwards — theirs, never touched.
			}
		}
		// new untracked files are invisible to git diff; the manifest taken
		// at snapshot time tells us which ones the agent created.
		removed, canSweep := sweepList(root, snap)
		if len(restore) == 0 && (!canSweep || len(removed) == 0) {
			return "nothing to undo — workspace already matches the last snapshot"
		}
		if len(restore)+len(removed) > 2000 {
			return fmt.Sprintf("undo aborted: %d files differ from the snapshot (implausible — refusing to bulk-restore)", len(restore)+len(removed))
		}
		// restore in arg-safe batches (Windows command-line length limits)
		const batch = 200
		for i := 0; i < len(restore); i += batch {
			hi := i + batch
			if hi > len(restore) {
				hi = len(restore)
			}
			r := exec.Command("git", append([]string{"checkout", snap, "--"}, restore[i:hi]...)...)
			r.Dir = root
			if err := r.Run(); err != nil {
				return "undo failed: " + err.Error()
			}
		}
		for _, p := range removed {
			_ = os.Remove(filepath.Join(root, p))
		}
		listed := restore
		if len(listed) > 8 {
			listed = append(append([]string{}, restore[:8]...), fmt.Sprintf("... (+%d more)", len(restore)-8))
		}
		msg := fmt.Sprintf("undone: restored %d file(s) to the last snapshot", len(restore))
		if len(removed) > 0 {
			msg += fmt.Sprintf(", removed %d file(s) whis created", len(removed))
		}
		if len(listed) > 0 {
			msg += ":\n  " + strings.Join(listed, "\n  ")
		}
		return msg
	}
	return fallbackUndo(root)
}

// gitOut runs a git command and returns trimmed stdout, or "" on any error.
func gitOut(root string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// snapTree lists every path contained in the snapshot commit.
func snapTree(root, snap string) map[string]bool {
	set := map[string]bool{}
	for _, p := range strings.Split(gitOut(root, "ls-tree", "-r", "--name-only", "-z", snap), "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// sweepList returns the untracked files that appeared AFTER the snapshot
// (i.e. created by the agent) for undo to delete. The snapshot's manifest
// is required: without one (legacy snapshot) nothing is deleted, to stay
// safe with pre-existing untracked user files.
func sweepList(root, snap string) (removed []string, ok bool) {
	b, err := os.ReadFile(filepath.Join(undoDir(), snap+".untracked"))
	if err != nil {
		return nil, false
	}
	before := map[string]bool{}
	for _, ln := range strings.Split(string(b), "\n") {
		if p := strings.TrimSpace(ln); p != "" {
			before[p] = true
		}
	}
	for _, ln := range strings.Split(gitOut(root, "ls-files", "--others", "--exclude-standard"), "\n") {
		p := strings.TrimSpace(ln)
		if p == "" || before[p] {
			continue
		}
		removed = append(removed, p)
	}
	return removed, true
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
