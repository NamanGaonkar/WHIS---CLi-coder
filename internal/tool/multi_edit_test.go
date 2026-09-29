package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMultiEditBatchAcrossFiles covers the big-refactor primitive: several
// edits across several files in one call, one snapshot, summary output.
func TestMultiEditBatchAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	e.AutoApprove = true
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\nfunc old() {}\n")
	write("b.go", "package b\n\nfunc caller() { old() }\n")

	res := e.MultiEdit([]MultiEdit{
		{Path: "a.go", Search: "func old() {}", Replace: "func renamed() {}"},
		{Path: "b.go", Search: "{ old() }", Replace: "{ renamed() }"},
	})
	if !res.OK {
		t.Fatalf("multi_edit failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "2 edit(s) across 2 file(s)") {
		t.Fatalf("bad summary: %s", res.Output)
	}
	b1, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	b2, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(b1), "func renamed()") || strings.Contains(string(b1), "func old()") {
		t.Fatalf("a.go not patched: %s", b1)
	}
	if !strings.Contains(string(b2), "renamed() }") {
		t.Fatalf("b.go not patched: %s", b2)
	}
}

// TestMultiEditAllOrNothing locks the safety property: a missing search
// block aborts the WHOLE batch and writes nothing.
func TestMultiEditAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := e.MultiEdit([]MultiEdit{
		{Path: "x.go", Search: "line one", Replace: "LINE ONE"},
		{Path: "x.go", Search: "line missing", Replace: "nope"},
	})
	if res.OK {
		t.Fatal("batch with a bad edit must fail")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "line one\nline two\n" {
		t.Fatalf("file was modified despite abort: %q", b)
	}
	if !strings.Contains(res.Output, "nothing written") {
		t.Fatalf("abort message should say nothing written: %s", res.Output)
	}
}

// TestMultiEditCreatesFile covers empty-search file creation inside a batch.
func TestMultiEditCreatesFile(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	e.AutoApprove = true
	res := e.MultiEdit([]MultiEdit{{Path: "new/dir-file.go", Search: "", Replace: "package fresh\n"}})
	if !res.OK || !strings.Contains(res.Output, "created") {
		t.Fatalf("create failed: %s", res.Output)
	}
	b, err := os.ReadFile(filepath.Join(dir, "new", "dir-file.go"))
	if err != nil || string(b) != "package fresh\n" {
		t.Fatalf("created file wrong: %v %q", err, b)
	}
	// creating over an existing file must refuse
	res = e.MultiEdit([]MultiEdit{{Path: "new/dir-file.go", Search: "", Replace: "overwrite"}})
	if res.OK {
		t.Fatal("empty search over existing file must refuse")
	}
}

// TestTaskTrackerPersistAndReload covers the checklist: write, reload from
// disk (new tracker instance = restart survival), clear.
func TestTaskTrackerPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)

	res := e.taskTracker("write", "- [ ] step one\n- [ ] step two")
	if !res.OK {
		t.Fatalf("write failed: %s", res.Output)
	}
	// fresh instance reads it back (survives restarts)
	got := (TaskTracker{Root: dir}).Load()
	if !strings.Contains(got, "- [ ] step one") || !strings.Contains(got, "- [ ] step two") {
		t.Fatalf("reload lost content: %q", got)
	}
	// the get action returns it too
	res = e.taskTracker("get", "")
	if !res.OK || !strings.Contains(res.Output, "step one") {
		t.Fatalf("get failed: %s", res.Output)
	}
	// empty write clears (file removed)
	res = e.taskTracker("write", "")
	if !res.OK {
		t.Fatalf("clear failed: %s", res.Output)
	}
	if (TaskTracker{Root: dir}).Load() != "" {
		t.Fatal("clear should empty the tracker")
	}
	if _, err := os.Stat(filepath.Join(dir, taskFile)); !os.IsNotExist(err) {
		t.Fatal("cleared tracker file should be removed")
	}
}
