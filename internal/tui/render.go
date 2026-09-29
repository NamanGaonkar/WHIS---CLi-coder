package tui

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// Render cache: renderTranscript runs after EVERY message (keypress, pasted
// rune, streaming token, spinner tick). Re-rendering (glamour) every line
// each frame was the root cause of paste showing a typing animation, esc
// lagging (keys queued behind render backlog), and long agent runs appearing
// stuck (the TUI consumed one event per frame with a full re-render behind
// it, backpressuring the agent's event channel until emit() blocked the
// agent loop itself — provider-independent stall).
//
// Lines are keyed by CONTENT HASH (not index), so a replaced transcript
// (resume replay, /clear) can never serve stale renders: same bytes → same
// render (correct), different bytes → miss → re-render. gen bumps on theme
// change (styles are global), open tracks the collapsed-code state, width is
// part of the key so terminal resizes re-render cleanly.
//
// The model value-receiver copies share this global cache safely: Update is
// single-goroutine, and identical (content, open, width, theme) inputs
// produce identical renders.

const (
	renderMaxLines = 1200 // entries cap; beyond this the cache resets
)

type rcacheKey struct {
	h     uint64 // fnv-1a of kind + body
	gen   int    // theme generation
	width int    // render width
	sig   string // codeOpen state for md lines
}

// rc is the process-wide transcript render cache.
var rc = struct {
	entries map[rcacheKey]string
	gen     int
}{entries: make(map[rcacheKey]string)}

// rcReset drops the whole cache. Called on theme change (styles are global
// state, so every cached render is stale).
func rcReset() {
	rc.entries = make(map[rcacheKey]string)
	rc.gen++
}

// rcHash fingerprints a transcript line's identity.
func rcHash(kind, body string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return h.Sum64()
}

// rcLine memoizes renderOneLine for a transcript line.
func rcLine(m model, l line, vw int) string {
	sig := ""
	if l.kind == "md" {
		sig = codeSig(m, l.n)
	}
	k := rcacheKey{h: rcHash(l.kind, l.body), gen: rc.gen, width: vw, sig: sig}
	if v, ok := rc.entries[k]; ok {
		return v
	}
	out := renderOneLine(m, l, vw)
	if len(rc.entries) >= renderMaxLines {
		// overflow: wholesale reset is simpler than an LRU and self-heals
		// on the next frame (only the changed lines re-render once).
		rcReset()
		rc.gen-- // keep the current theme generation; only drop entries
	}
	rc.entries[k] = out
	return out
}

// codeSig summarizes the per-segment open/closed state that
// renderCollapsible depends on for md line n, so the render cache can key
// on it (codeOpen toggles produce a different signature → re-render).
func codeSig(m model, n int) string {
	var ks []int
	for k := range m.codeOpen {
		if k/1000 == n+1 { // high bits encode the line, low bits the segment
			ks = append(ks, k)
		}
	}
	if len(ks) == 0 {
		return ""
	}
	sort.Ints(ks)
	var b strings.Builder
	for _, k := range ks {
		fmt.Fprintf(&b, "%d=%t,", k%1000, m.codeOpen[k])
	}
	return b.String()
}

// renderOneLine renders exactly one transcript line (no cache). Extracted
// verbatim from the old renderTranscript loop.
func renderOneLine(m model, l line, vw int) string {
	switch l.kind {
	case "user":
		return badgeStyle.Render("you") + okStyle.Render(" "+l.body)
	case "md":
		return renderCollapsible(l.body, vw, m.codeOpenFor(l.n))
	case "plan":
		return planStyle.Render(truncateLines(l.body, vw-4, 12))
	case "tool":
		if isMCPToolLine(l.body) {
			// external MCP call: amber diamond marker so external side
			// effects are visually distinct from native tools
			return wrapANSI(warnStyle.Render("◆ "+l.body), vw)
		}
		return wrapANSI(toolStyle.Render("> "+l.body), vw)
	case "toolout":
		// web_search digests, command output: these are the long grey
		// blocks. They used to be HARD-CLIPPED mid-line, which chopped URLs
		// and search snippets into broken ragged fragments with uneven
		// spacing. ANSI-aware WRAPPING keeps every byte on screen, color
		// running continuously down the wrapped rows.
		return wrapANSI(diffStyle(l.body), vw)
	case "done":
		// elapsed is frozen into the line at creation (stamp): render-time
		// reads would drift across runs and poison the memo cache
		elapsed := l.stamp
		if elapsed == "" {
			elapsed = fmtDur(m.runLast)
		}
		return doneStyle.Render(" DONE ") + doneTextStyle.Render(" "+l.body+" ") + dimStyle.Render(" "+elapsed)
	case "info":
		return wrapANSI(dimStyle.Render("· "+l.body), vw)
	case "error":
		return wrapANSI(errStyle.Render("x "+l.body), vw)
	}
	return ""
}

// renderTranscriptCached builds the viewport content from cached per-line
// renders. Semantically identical to the old renderTranscript; only faster
// (unchanged lines come from the memo cache instead of re-running glamour).
//
// Spacing: prose blocks (md/user/done/plan and the streaming tail) keep a
// full blank line between them, but runs of UTILITY lines (tool calls,
// tool output, info, error) are packed tightly — a web_search dump or a
// tool-call streak used to render with a blank line before AND after every
// grey line, which read as broken/unprofessional gaps. A utility run joins
// with single newlines and gets ONE blank line separating it from prose.
func (m model) renderTranscriptCached(vw int) string {
	isUtil := func(l line) bool {
		switch l.kind {
		case "tool", "toolout", "info", "error":
			return true
		}
		return false
	}
	type block struct {
		util bool
		rows []string
	}
	var blocks []block
	flush := func(util bool, rows []string) {
		if len(rows) > 0 {
			blocks = append(blocks, block{util: util, rows: rows})
		}
	}
	var cur []string
	curUtil := false
	for i, l := range m.lines {
		u := isUtil(l)
		if i == 0 || u != curUtil {
			flush(curUtil, cur)
			cur = nil
			curUtil = u
		}
		cur = append(cur, rcLine(m, l, vw))
	}
	flush(curUtil, cur)
	if m.streamBuf != "" {
		if m.status.Spinning {
			// while working: show a live line count, not the dumping text
			lines := strings.Count(strings.TrimSpace(m.streamBuf), "\n") + 1
			flush(false, []string{workingStyle.Render(fmt.Sprintf("... composing reply (%d lines so far)", lines))})
		} else {
			flush(false, []string{renderCollapsible(m.streamBuf, vw, m.codeOpenFor(-1))})
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	// assemble: ANSI-aware wrap per row (memoized), utility blocks joined
	// tight inside, one blank line between blocks. Lines are pre-wrapped by
	// their renderer (wrapANSI) so this pass is a no-op safety net that
	// preserves color across continuation rows instead of chopping them.
	var out []string
	for _, b := range blocks {
		clipped := make([]string, 0, len(b.rows))
		for _, p := range b.rows {
			clipped = append(clipped, clipPartCached(p, vw))
		}
		sep := "\n\n"
		if b.util {
			sep = "\n"
		}
		out = append(out, strings.Join(clipped, sep))
	}
	return strings.Join(out, "\n\n")
}

// clipPartCached memoizes the width-fitting of an already-rendered part.
// Keyed by the full rendered string — hashing is far cheaper than the
// per-line ANSI-aware wrap + visWidth measurement. Wraps (never clips):
// chopping grey tool/web output mid-line is what broke the transcript
// before; every byte must stay visible.
var clipCache = map[clipKey]string{}

type clipKey struct {
	s   string
	vw  int
	gen int
}

func clipPartCached(p string, vw int) string {
	k := clipKey{s: p, vw: vw, gen: rc.gen}
	if v, ok := clipCache[k]; ok {
		return v
	}
	out := wrapANSI(p, vw)
	if len(clipCache) >= renderMaxLines*2 {
		clipCache = map[clipKey]string{}
	}
	clipCache[k] = out
	return out
}
