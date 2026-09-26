package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFuzzyFindExactWhitespace(t *testing.T) {
	hay := "func a() {\n\treturn 1\n}\n\nfunc b() {}\n"
	start, end, mode := fuzzyFind(hay, "func a() {\n    return 1\n}")
	if start < 0 || end <= start {
		t.Fatal("whitespace-tolerant match failed")
	}
	if mode != "whitespace" {
		t.Fatalf("mode = %q, want whitespace", mode)
	}
	if got := hay[start:end]; got != "func a() {\n\treturn 1\n}" {
		t.Fatalf("span = %q", got)
	}
}

func TestFuzzyFindDriftedLines(t *testing.T) {
	hay := "func compute(x int) int {\n\ty := x * 2\n\treturn y + 1\n}\n"
	// model's search block drifted: variable renamed + constant changed
	needle := "func compute(x int) int {\n\tz := x * 2\n\treturn z + 2\n}"
	start, end, mode := fuzzyFind(hay, needle)
	if start < 0 || mode != "fuzzy" {
		t.Fatalf("drifted match failed: start=%d mode=%q", start, mode)
	}
	if end <= start || end > len(hay) {
		t.Fatalf("bad span [%d,%d) len=%d", start, end, len(hay))
	}
}

func TestFuzzyFindRejectsWildDrift(t *testing.T) {
	hay := "package main\n\nfunc main() {}\n"
	if s, _, _ := fuzzyFind(hay, "totally\ndifferent\ncontent"); s >= 0 {
		t.Fatal("should not match unrelated content")
	}
}

func TestApplyPatchAndReadRange(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	e.AutoApprove = true

	// create
	r := e.Execute("apply_patch", []byte(`{"path":"a.go","search":"","replace":"package a\n\nfunc Hi() { println(1) }\n"}`))
	if !r.OK {
		t.Fatalf("create failed: %s", r.Output)
	}
	// exact edit
	r = e.Execute("apply_patch", []byte(`{"path":"a.go","search":"func Hi() { println(1) }","replace":"func Hi() { println(2) }"}`))
	if !r.OK || !strings.Contains(r.Output, "patched") {
		t.Fatalf("edit failed: %s", r.Output)
	}
	// fuzzy edit (whitespace drift)
	r = e.Execute("apply_patch", []byte(`{"path":"a.go","search":"func Hi() {  println(2)  }","replace":"func Hi() { println(3) }"}`))
	if !r.OK {
		t.Fatalf("fuzzy edit failed: %s", r.Output)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(b), "println(3)") {
		t.Fatalf("patch not applied: %s", b)
	}

	// read_range + cap
	r = e.Execute("read_range", []byte(`{"path":"a.go","start":1,"end":10}`))
	if !r.OK || !strings.Contains(r.Output, "1|") {
		t.Fatalf("read_range failed: %s", r.Output)
	}
}

func TestReadRangeBlocksLargeReads(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	e.AutoApprove = true
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line\n")
	}
	_ = os.WriteFile(filepath.Join(dir, "big.txt"), []byte(sb.String()), 0o644)

	r := e.Execute("read_range", []byte(`{"path":"big.txt","start":1,"end":200}`))
	if r.OK {
		t.Fatal("expected >120-line read to be blocked")
	}
	r = e.Execute("read_range", []byte(`{"path":"big.txt","start":1,"end":200,"force":true}`))
	if !r.OK {
		t.Fatalf("forced read should pass: %s", r.Output)
	}
}

func TestLocateSymbolGo(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	src := "package x\n\nfunc Alpha() {\n\tdoWork()\n}\n\ntype Beta struct {\n\tField int\n}\n"
	_ = os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644)

	r := e.Execute("locate_symbol", []byte(`{"file":"x.go","name":"Alpha"}`))
	if !r.OK || !strings.Contains(r.Output, "func Alpha()") || !strings.Contains(r.Output, "doWork()") {
		t.Fatalf("locate_symbol failed: %s", r.Output)
	}
	// whole-file content must NOT be echoed beyond the symbol
	if strings.Contains(r.Output, "type Beta struct") {
		t.Fatal("locate_symbol leaked beyond symbol body")
	}
}

func TestSearchAndTree(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	e.AutoApprove = true
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "junk"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "junk", "x.js"), []byte("func fake(){}\n"), 0o644)

	r := e.Execute("search_codebase", []byte(`{"pattern":"func main"}`))
	if !r.OK || !strings.Contains(r.Output, "main.go:2") {
		t.Fatalf("search failed: %s", r.Output)
	}
	if strings.Contains(r.Output, "fake") {
		t.Fatal("node_modules not pruned")
	}
	r = e.Execute("list_tree", []byte(`{}`))
	if !r.OK || !strings.Contains(r.Output, "main.go") || strings.Contains(r.Output, "node_modules") {
		t.Fatalf("tree failed: %s", r.Output)
	}
}

func TestPathEscape(t *testing.T) {
	dir := t.TempDir()
	e := NewEnv(dir)
	r := e.Execute("read_range", []byte(`{"path":"../../etc/passwd","start":1,"end":2}`))
	if r.OK {
		t.Fatal("path escape should be refused")
	}
}
