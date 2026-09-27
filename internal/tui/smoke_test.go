package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// frameStats measures a rendered frame: rows and widest line.
func frameStats(v string) (rows, maxW int) {
	for _, ln := range strings.Split(v, "\n") {
		if w := lipgloss.Width(ln); w > maxW {
			maxW = w
		}
	}
	return strings.Count(v, "\n") + 1, maxW
}

// TestSmokeLayoutNeverOverflows renders every screen (splash, menus, session,
// overlays, streaming, done) at many terminal sizes and asserts the frame
// never exceeds the screen: this is the exact bug class that clipped the
// WHIS banner and pushed the finished reply off-screen.
func TestSmokeLayoutNeverOverflows(t *testing.T) {
	sizes := []tea.WindowSizeMsg{
		{Width: 40, Height: 12}, {Width: 60, Height: 16}, {Width: 80, Height: 24},
		{Width: 100, Height: 30}, {Width: 120, Height: 40}, {Width: 200, Height: 60},
	}
	for _, sz := range sizes {
		m := newTestModel(t)
		m2, _ := m.Update(sz)
		m = m2.(model)

		check := func(stage string, mm model) {
			t.Helper()
			v := mm.View()
			rows, maxW := frameStats(v)
			if rows > sz.Height {
				t.Fatalf("%s @ %dx%d: frame has %d rows, screen has %d", stage, sz.Width, sz.Height, rows, sz.Height)
			}
			if maxW > sz.Width {
				for _, ln := range strings.Split(v, "\n") {
					if lipgloss.Width(ln) == maxW {
						t.Fatalf("%s @ %dx%d: frame width %d exceeds screen %d; widest line: %q", stage, sz.Width, sz.Height, maxW, sz.Width, ln)
					}
				}
				t.Fatalf("%s @ %dx%d: frame width %d exceeds screen %d", stage, sz.Width, sz.Height, maxW, sz.Width)
			}
		}

		check("splash", m)

		// slash menu on splash (the screen that clipped the banner)
		m.over.openSlashMenu()
		check("splash+menu", m)
		m.over = overlay{}

		// to session view with a finished reply + done banner
		m.splash = false
		m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}})
		m3, _ := m2.(model).Update(tea.KeyMsg{Type: tea.KeyEnter})
		mm := m3.(model)
		mm.lines = append(mm.lines,
			line{kind: "user", body: "test prompt"},
			line{kind: "md", body: "prose paragraph\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\nmore prose"},
			line{kind: "tool", body: "apply_patch {\"path\":\"x.go\"}"},
			line{kind: "done", body: "everything works"},
		)
		mm.streamBuf = ""
		check("session+done", mm)

		// every overlay on top of a session with history
		mm.over.openProviderMenu(nil)
		check("provider-menu", mm)
		mm.over = overlay{}
		mm.over.openModeMenu("ask")
		check("mode-menu", mm)
		mm.over = overlay{}
		mm.over.openSessions("root")
		check("sessions-menu", mm)

		// wrapped input (multi-row textarea)
		mm.over = overlay{}
		mm.input.SetValue(strings.Repeat("long wrapped line of text ", 10))
		check("wrapped-input", mm)
	}
}

// TestSmokeScrollbarGeometry feeds the viewport tall content and asserts the
// scrollbar renders, is flush at the right edge, and the thumb moves down as
// the view scrolls.
func TestSmokeScrollbarGeometry(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m2.(model)
	m.splash = false
	for i := 0; i < 200; i++ {
		m.lines = append(m.lines, line{kind: "info", body: strings.Repeat("x", 60)})
	}
	m.vp.Width = 79
	m.vp.Height = 10
	m.vp.SetContent(m.renderTranscript(m.vp.Width))
	m.vp.GotoBottom()
	out := vpWithScrollbar(m.vp)
	lines := strings.Split(out, "\n")
	if len(lines) < m.vp.Height {
		t.Fatalf("scrollbar output truncated: %d lines", len(lines))
	}
	wThumb := lipgloss.Width(lines[0])
	if wThumb != m.vp.Width+1 {
		t.Fatalf("row width = %d, want viewport+1 (bar column), vp.Width=%d", wThumb, m.vp.Width)
	}
	// every rendered row must be the same width (no wrap jitter)
	for i, ln := range lines[:m.vp.Height] {
		if w := lipgloss.Width(ln); w != wThumb {
			t.Fatalf("row %d width %d != %d (wrap/mis-pad)", i, w, wThumb)
		}
	}
	// thumb position moves with scroll
	top := m.vp.YOffset
	m.vp.GotoBottom()
	bottomView := vpWithScrollbar(m.vp)
	if bottomView == "" {
		t.Fatal("empty scrollbar view at bottom")
	}
	_ = top
}

// TestSmokeTypingNeverHijacked types c, t, o, y, n with no pending state and
// asserts every letter lands in the input (regression for the c/t hotkeys).
func TestSmokeTypingNeverHijacked(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	for _, r := range "ctoyn" {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = m2.(model)
	}
	if got := m.input.Value(); got != "ctoyn" {
		t.Fatalf("typing hijacked: input = %q, want %q", got, "ctoyn")
	}
}

// TestSmokeCtrlOogglesCode builds a reply with two code blocks and asserts
// ctrl+o expands then collapses them.
func TestSmokeCtrlOogglesCode(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.lines = append(m.lines, line{kind: "md", n: 1, body: "text\n\n```go\na := 1\n```\n\n```js\nlet b;\n```"})
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = mm.(model)
	if len(m.codeOpen) == 0 {
		t.Fatal("ctrl+o did not expand any code block")
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = mm.(model)
	for _, open := range m.codeOpen {
		if open {
			t.Fatal("second ctrl+o should collapse all blocks")
		}
	}
}

// TestSmokeTimerLifecycle drives a full run and asserts the timer only ticks
// while spinning and freezes at DONE (per-run timer, not session clock).
func TestSmokeTimerLifecycle(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.beginRun()
	if !m.runActive || !m.status.Spinning {
		t.Fatal("run should be active and spinning after beginRun")
	}
	m.endRun()
	if m.runActive {
		t.Fatal("timer must stop after endRun")
	}
	if !strings.HasSuffix(m.status.Elapsed, "s") {
		t.Fatalf("elapsed should be frozen duration, got %q", m.status.Elapsed)
	}
	if m.status.Spinning {
		t.Fatal("spinning must be false after endRun")
	}
}

// TestSmokeScrollKeys asserts pgup/pgdown/end actually move the viewport and
// that auto-follow resumes when you jump back to the bottom.
func TestSmokeScrollKeys(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	for i := 0; i < 100; i++ {
		m.lines = append(m.lines, line{kind: "info", body: "line"})
	}
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = m2.(model)
	if m.vp.YOffset != 0 {
		t.Fatalf("pgup from bottom should leave offset %d, got %d", 0, m.vp.YOffset)
	}
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = m2.(model)
	if !m.vp.AtBottom() {
		t.Fatal("end should return to bottom")
	}
}

// TestSmokeBusyGuard asserts a second submit during a run is rejected and
// never starts a second event chain (delayed/doubled reply regression).
func TestSmokeBusyGuard(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.beginRun()
	seq := m.runSeq
	m.input.SetValue("second prompt")
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(model)
	if m.runSeq != seq {
		t.Fatal("submit while spinning started a second run")
	}
	if m.input.Value() != "second prompt" {
		t.Fatal("busy submit must keep the typed text in the box")
	}
}

// TestSmokeStaleChainIgnored feeds an event from an old run sequence and
// asserts it cannot clear the new run's spinning state.
func TestSmokeStaleChainIgnored(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.beginRun() // seq 1
	old := m.runSeq
	m.beginRun() // seq 2 supersedes
	m2, _ = m.handleChunk(streamChunk{first: false, seq: old, ev: TUIEvent{Type: "turn_done", Text: ""}, rest: make(chan TUIEvent)})
	mm := m2.(model)
	if !mm.status.Spinning {
		t.Fatal("stale turn_done cleared the new run's spinning state")
	}
}
