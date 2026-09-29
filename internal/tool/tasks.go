package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TaskTracker is the agent's cross-turn plan: a checklist it maintains
// while working on multi-step jobs. Stored as plain text in .whis/tasks.md
// inside the workspace so it survives restarts, diffs like code, and never
// needs a database. Overwrite-per-call keeps it token-cheap: one small
// write syncs the whole plan.
type TaskTracker struct{ Root string }

const taskFile = ".whis/tasks.md"

// taskPath resolves the tracker file inside the workspace.
func (t TaskTracker) taskPath() (string, error) {
	abs := filepath.Join(t.Root, taskFile)
	// containment check mirrors Env.resolve
	rootAbs, err := filepath.Abs(t.Root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return abs, nil
}

// Load returns the current task list text ("" when none exists).
func (t TaskTracker) Load() string {
	abs, err := t.taskPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

// Write replaces the whole checklist. Returns a short agent-facing ack.
func (t TaskTracker) Write(body string) Result {
	abs, err := t.taskPath()
	if err != nil {
		return Result{Output: err.Error()}
	}
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		// empty write = plan cleared; remove the file
		_ = os.Remove(abs)
		return Result{OK: true, Output: "task list cleared"}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{Output: err.Error()}
	}
	if err := os.WriteFile(abs, []byte(body+"\n"), 0o644); err != nil {
		return Result{Output: err.Error()}
	}
	lines := strings.Count(body, "\n") + 1
	return Result{OK: true, Output: fmt.Sprintf("task list saved (%d lines) — keep it updated as you work", lines)}
}

// task_tracker implements the agent-facing tool: get shows the current
// list, write replaces it. The list is injected into the system prompt
// context by the agent, so the model always sees its own plan.
func (e *Env) taskTracker(action, body string) Result {
	t := TaskTracker{Root: e.Root}
	switch action {
	case "write":
		return t.Write(body)
	case "get", "":
		cur := t.Load()
		if cur == "" {
			return Result{OK: true, Output: "no task list yet — write one with {\"action\":\"write\",\"body\":\"- [ ] step...\"}"}
		}
		return Result{OK: true, Output: cur}
	default:
		return Result{Output: "task_tracker: unknown action " + action + " (get | write)"}
	}
}
