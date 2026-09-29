package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxMultiEdits caps one multi_edit call. 40 edits is far past anything a
// sane single refactor step needs, and it bounds worst-case approval work.
const maxMultiEdits = 40

// MultiEdit is one search/replace edit inside a multi_edit batch.
type MultiEdit struct {
	Path    string `json:"path"`    // workspace-relative
	Search  string `json:"search"`  // exact existing text (empty => create file)
	Replace string `json:"replace"` // replacement text
}

// MultiEdit applies SEVERAL search/replace edits, optionally across
// different files, in ONE tool call. This is the big-refactor primitive:
// one approval, one undo snapshot, one result summary instead of N round
// trips. The whole batch is validated FIRST (every search must match) and
// only then applied — a bad edit aborts before touching any file, so a
// half-applied refactor can never poison the tree.
func (e *Env) MultiEdit(edits []MultiEdit) Result {
	if len(edits) == 0 {
		return Result{Output: "multi_edit: empty edits array"}
	}
	if len(edits) > maxMultiEdits {
		return Result{Output: fmt.Sprintf("multi_edit: %d edits exceeds the %d cap — split into batches", len(edits), maxMultiEdits)}
	}

	// group per file, resolve once, validate paths early
	perFile := map[string][]MultiEdit{}
	order := []string{} // stable file order for the summary
	for i, ed := range edits {
		if strings.TrimSpace(ed.Path) == "" {
			return Result{Output: fmt.Sprintf("multi_edit: edit %d has empty path", i+1)}
		}
		if _, err := e.resolve(ed.Path); err != nil {
			return Result{Output: fmt.Sprintf("multi_edit: edit %d: %v", i+1, err)}
		}
		if _, seen := perFile[ed.Path]; !seen {
			order = append(order, ed.Path)
		}
		perFile[ed.Path] = append(perFile[ed.Path], ed)
	}

	// plan each file: read once, apply its edits on the in-memory copy.
	// Nothing is written until EVERY file planned successfully.
	plans := map[string]string{} // path -> new content
	mode := map[string]string{}  // path -> "created"|"exact"|"fuzzy <kind>"
	created := map[string]bool{}
	for _, path := range order {
		eds := perFile[path]
		// file creation: allowed only as a lone empty-search edit
		abs, _ := e.resolve(path)
		if len(eds) == 1 && strings.TrimSpace(eds[0].Search) == "" {
			if fileExists(abs) {
				return Result{Output: "multi_edit: " + path + " exists; empty search would replace everything"}
			}
			plans[path] = eds[0].Replace
			created[path] = true
			mode[path] = "created"
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return Result{Output: "multi_edit: " + path + ": " + err.Error()}
		}
		cur := string(b)
		for i, ed := range eds {
			if strings.TrimSpace(ed.Search) == "" {
				return Result{Output: fmt.Sprintf("multi_edit: %s edit %d: empty search is only valid for file creation", path, i+1)}
			}
			start, end := -1, -1
			kind := "exact"
			if j := strings.Index(cur, ed.Search); j >= 0 {
				start, end = j, j+len(ed.Search)
			} else {
				start, end, kind = fuzzyFind(cur, ed.Search)
				if start < 0 {
					return Result{Output: fmt.Sprintf("multi_edit: %s edit %d: search block not found (even fuzzily) — batch aborted, nothing written; re-read the file and retry", path, i+1)}
				}
			}
			cur = cur[:start] + ed.Replace + cur[end:]
			if kind != "exact" {
				kind = "fuzzy " + kind
			}
			mode[path] = kind
		}
		plans[path] = cur
	}

	// approve once for the whole batch, snapshot once
	names := order
	sort.Strings(names)
	if len(names) > 3 {
		names = append(names[:3], fmt.Sprintf(" +%d more", len(names)-3))
	}
	if !e.approve(riskSafe, fmt.Sprintf("multi-edit %d file(s): %s", len(order), strings.Join(names, ", "))) {
		return Result{Output: "user declined multi-edit."}
	}
	if e.OnSnapshot != nil {
		_ = e.OnSnapshot()
	}

	var out strings.Builder
	for _, path := range order {
		abs, _ := e.resolve(path)
		if created[path] {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return Result{OK: false, Output: "multi_edit: mkdir for " + path + ": " + err.Error() +
					" — earlier files in the batch were already written; run /undo to roll back"}
			}
		}
		if err := os.WriteFile(abs, []byte(plans[path]), 0o644); err != nil {
			return Result{OK: false, Output: "multi_edit: write failed at " + path + ": " + err.Error() +
				" — earlier files in the batch were already written; run /undo to roll back"}
		}
		fmt.Fprintf(&out, "%s %s\n", mode[path], path)
	}
	return Result{OK: true, Output: fmt.Sprintf("multi_edit: %d edit(s) across %d file(s)\n%s", len(edits), len(order), strings.TrimRight(out.String(), "\n"))}
}

// parseMultiEditArgs decodes the model-facing {"edits":[...]} payload.
func parseMultiEditArgs(args json.RawMessage) ([]MultiEdit, error) {
	var a struct {
		Edits []MultiEdit `json:"edits"`
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("missing edits array")
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, err
	}
	return a.Edits, nil
}
