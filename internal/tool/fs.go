package tool

import (
	"fmt"
	"os"
	"strings"
)

// ReadRange implements read_range: numbered line slice with a 120-line cap.
func (e *Env) ReadRange(path string, start, end int, force bool) Result {
	abs, err := e.resolve(path)
	if err != nil {
		return Result{Output: err.Error()}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Output: err.Error()}
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	total := len(lines)
	if start < 1 {
		start = 1
	}
	if end == 0 || end > total {
		end = total
	}
	if start > end {
		return Result{Output: fmt.Sprintf("invalid range %d-%d (file has %d lines)", start, end, total)}
	}
	n := end - start + 1
	if n > MaxReadLines && !force {
		return Result{Output: fmt.Sprintf("refused: %d lines exceeds %d-line cap; request a smaller range or set force=true", n, MaxReadLines)}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s [%d/%d lines]\n", path, n, total)
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&out, "%4d| %s\n", i+1, lines[i])
	}
	return Result{OK: true, Output: strings.TrimRight(out.String(), "\n")}
}

// ApplyPatch implements apply_patch: exact or fuzzy SEARCH/REPLACE editing.
// Before mutating it requests approval and takes an undo snapshot.
func (e *Env) ApplyPatch(path, search, replace string) Result {
	abs, err := e.resolve(path)
	if err != nil {
		return Result{Output: err.Error()}
	}

	// empty SEARCH creates a new file (checked before any read)
	if strings.TrimSpace(search) == "" {
		if fileExists(abs) {
			return Result{Output: "file exists; empty search would replace everything"}
		}
		if !e.approve("create file: " + path) {
			return Result{Output: "user declined file creation."}
		}
		if e.OnSnapshot != nil {
			_ = e.OnSnapshot()
		}
		if err := os.WriteFile(abs, []byte(replace), 0o644); err != nil {
			return Result{Output: err.Error()}
		}
		return Result{OK: true, Output: "created " + path + " (" + fmt.Sprint(len(strings.Split(replace, "\n"))) + " lines)"}
	}

	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Output: err.Error()}
	}
	src := string(b)

	// exact match first
	start, end := -1, -1
	mode := "exact"
	if i := strings.Index(src, search); i >= 0 {
		start, end = i, i+len(search)
	} else {
		start, end, mode = fuzzyFind(src, search)
		if start < 0 {
			return Result{Output: "search block not found in " + path + " (even fuzzily); re-run read_range and retry with exact text"}
		}
	}
	if !e.approve("edit file: " + path + " (" + mode + " match, " + fmt.Sprint(strings.Count(search, "\n")+1) + " lines)") {
		return Result{Output: "user declined file edit."}
	}
	if e.OnSnapshot != nil {
		_ = e.OnSnapshot()
	}
	patched := src[:start] + replace + src[end:]
	if err := os.WriteFile(abs, []byte(patched), 0o644); err != nil {
		return Result{Output: err.Error()}
	}
	note := ""
	if mode != "exact" {
		note = " [fuzzy " + mode + "]"
	}
	return Result{OK: true, Output: "patched " + path + note}
}

// fuzzyFind locates the best approximate match for needle in hay.
// Returns (startByte, endByte, mode) or (-1, 0, "").
func fuzzyFind(hay, needle string) (int, int, string) {
	hayLines := strings.Split(hay, "\n")
	needleLines := strings.Split(needle, "\n")
	n := len(needleLines)
	if n == 0 || len(hayLines) < n {
		return -1, 0, ""
	}
	// exact line-slice match ignoring leading/trailing whitespace per line
	for i := 0; i+n <= len(hayLines); i++ {
		match := true
		for j := 0; j < n; j++ {
			if strings.TrimSpace(hayLines[i+j]) != strings.TrimSpace(needleLines[j]) {
				match = false
				break
			}
		}
		if match {
			s, e := lineSpan(hay, i, n)
			return s, e, "whitespace"
		}
	}
	// Levenshtein scan: find window with minimal edit distance ≤ ~2/line
	best, bestDist, bestMode := -1, -1, ""
	for i := 0; i+n <= len(hayLines); i++ {
		dist := 0
		for j := 0; j < n; j++ {
			dist += levenshtein(hayLines[i+j], needleLines[j])
			if dist > 2*n && best >= 0 {
				break
			}
		}
		if best < 0 || dist < bestDist {
			best, bestDist, bestMode = i, dist, "fuzzy"
		}
	}
	threshold := 2 * n // tolerate ~2 edits per line of drift
	if best >= 0 && bestDist <= threshold {
		s, e := lineSpan(hay, best, n)
		return s, e, bestMode
	}
	return -1, 0, ""
}

// lineSpan returns the byte span covering lines i..i+n-1 without consuming
// the final newline, so replacement semantics match exact Index() splices.
func lineSpan(s string, i, n int) (int, int) {
	start := lineOffset(s, i)
	end := start
	for k := 0; k < n; k++ {
		idx := strings.IndexByte(s[end:], '\n')
		if idx < 0 {
			return start, len(s) // last line has no trailing newline
		}
		if k < n-1 {
			end += idx + 1 // consume interior newlines
		} else {
			end += idx // stop before the last matched line's newline
		}
	}
	return start, end
}

// lineOffset returns the byte offset of line index i (0-based) in s.
func lineOffset(s string, i int) int {
	off := 0
	for k := 0; k < i; k++ {
		idx := strings.IndexByte(s[off:], '\n')
		if idx < 0 {
			return off
		}
		off += idx + 1
	}
	return off
}

// levenshtein computes edit distance between two strings.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
