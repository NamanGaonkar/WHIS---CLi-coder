package tui

import "testing"

// TestCleanPlanSquashesGaps locks the reasoning-block formatting: edge
// blanks trimmed, internal blank runs squashed to one line — no more airy
// double-gaps in chat after every tool step.
func TestCleanPlanSquashesGaps(t *testing.T) {
	in := "\n\nthinking about the search\n\n\n\nfound two candidates\n  \n\t\n"
	want := "thinking about the search\n\nfound two candidates"
	if got := cleanPlan(in); got != want {
		t.Fatalf("cleanPlan:\n got %q\nwant %q", got, want)
	}
	if got := cleanPlan("   \n \n"); got != "" {
		t.Fatalf("blank-only reasoning should become empty, got %q", got)
	}
}

// TestTruncateLinesRuneSafe guards the byte-slicing bug: cutting at a byte
// offset used to split multi-byte runes (and garble the line). Width is now
// in runes.
func TestTruncateLinesRuneSafe(t *testing.T) {
	in := "héllo wörld ünïcode ✓✓✓" // multi-byte runes throughout
	got := truncateLines(in, 10, 8)
	if got[:3] == "h\xef" || len(got) == 0 {
		t.Fatalf("truncate split inside a rune: %q", got)
	}
	if want := "héllo wörl..."; got != want { // 10 runes + ellipsis
		t.Fatalf("truncateLines:\n got %q\nwant %q", got, want)
	}
}
