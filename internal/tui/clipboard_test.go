package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestLastTranscriptTextPrefersReply checks the picker: newest assistant
// markdown wins over tool output, banners and info lines; user prompts are
// also copyable when no reply has landed yet.
func TestLastTranscriptTextPrefersReply(t *testing.T) {
	m := newTestModel(t)
	m.lines = []line{
		{kind: "user", body: "fix the login bug"},
		{kind: "tool", body: "apply_patch internal/x.go"},
		{kind: "toolout", body: "OK"},
		{kind: "md", body: "rendered \x1b[38;5;1mANSI\x1b[0m reply body"},
		{kind: "done", body: "patched 2 files"},
		{kind: "info", body: "agent is still working"},
	}

	// agent.LastAssistantText() comes from the fakeAPI; when it returns a
	// non-empty source we must get exactly that (source over render).
	m.agent = &fakeAPI{evs: make(chan TUIEvent), lastReply: "raw md **source**"}
	txt, ok := m.lastTranscriptText()
	if !ok || txt != "raw md **source**" {
		t.Fatalf("want raw assistant source, got %q ok=%v", txt, ok)
	}

	// with no stored reply, md falls back to its (ANSI-stripped) body
	m.agent = &fakeAPI{evs: make(chan TUIEvent)}
	txt, ok = m.lastTranscriptText()
	if !ok || txt != "rendered ANSI reply body" {
		t.Fatalf("want stripped md body, got %q ok=%v", txt, ok)
	}

	// user line is copyable when there is no assistant md at all
	m.lines = []line{{kind: "user", body: "just my prompt"}}
	txt, ok = m.lastTranscriptText()
	if !ok || txt != "just my prompt" {
		t.Fatalf("want user prompt, got %q ok=%v", txt, ok)
	}

	// done banners are never copy targets, but tool output is — copying a
	// failing build/test log is a real use case when no reply landed yet
	m.lines = []line{{kind: "done", body: "DONE"}}
	if _, ok := m.lastTranscriptText(); ok {
		t.Fatal("done-only transcript should not be copyable")
	}
	m.lines = []line{{kind: "done", body: "DONE"}, {kind: "toolout", body: "build failed: x.go:7"}}
	if txt, ok := m.lastTranscriptText(); !ok || txt != "build failed: x.go:7" {
		t.Fatalf("tool output should be copyable, got %q ok=%v", txt, ok)
	}
}

// TestCopyTargetsAreSelectable covers /copy prompt and /copy output plus
// the arg-less default (reply).
func TestCopyTargetsAreSelectable(t *testing.T) {
	m := newTestModel(t)
	m.splash = false
	m.agent = &fakeAPI{evs: make(chan TUIEvent), lastReply: "the **answer**", lastUser: "my original question"}
	m.lines = []line{
		{kind: "user", body: "my original question"},
		{kind: "toolout", body: "PASS 12 tests"},
		{kind: "md", body: "the answer"},
	}

	// default: reply
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := mm.(model)
	if !strings.Contains(stripANSI(m2.lines[len(m2.lines)-1].body), "last reply") {
		t.Fatalf("default copy should target the reply, got: %q", stripANSI(m2.lines[len(m2.lines)-1].body))
	}

	// /copy prompt: newest user prompt from the session store
	m3, _ := m2.copyLast("prompt")
	if !strings.Contains(stripANSI(m3.(model).lines[len(m3.(model).lines)-1].body), "your last prompt") {
		t.Fatal("/copy prompt should acknowledge the prompt target")
	}

	// /copy output: newest toolout line
	m4, _ := m2.copyLast("output")
	if !strings.Contains(stripANSI(m4.(model).lines[len(m4.(model).lines)-1].body), "last command output") {
		t.Fatal("/copy output should acknowledge the output target")
	}

	// unknown target errors honestly (no silent fallback to reply)
	m5, _ := m2.copyLast("banana")
	last := stripANSI(m5.(model).lines[len(m5.(model).lines)-1].body)
	if m5.(model).lines[len(m5.(model).lines)-1].kind != "error" || !strings.Contains(last, "unknown target") {
		t.Fatalf("unknown target should error, got %q", last)
	}
}

// TestCopyEmptyTranscriptErrors makes the failure honest instead of silent.
func TestCopyEmptyTranscriptErrors(t *testing.T) {
	m := newTestModel(t)
	m.splash = false
	m.lines = []line{{kind: "done", body: "DONE"}}

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := mm.(model)
	found := false
	for _, l := range m2.lines {
		if l.kind == "error" && strings.Contains(stripANSI(l.body), "nothing to copy") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a 'nothing to copy' error line")
	}
}

// TestSlashMenuHasCopyRow keeps the menu row and its value in sync.
func TestSlashMenuHasCopyRow(t *testing.T) {
	m := newTestModel(t)
	m.over.openSlashMenu()
	found := false
	for _, it := range m.over.items {
		if it.value == "/copy" {
			found = true
		}
	}
	if !found {
		t.Fatal("slash menu is missing the /copy row")
	}
}

// TestCopySelectionOpencodeParity pins the opencode selection model end to
// end: release copies + toasts but KEEPS the selection visible; ctrl+c
// copies again without quitting; esc clears it and reaches Interrupt.
func TestCopySelectionOpencodeParity(t *testing.T) {
	// force a color profile so the inverse-video selection SGR actually
	// renders (the test env is headless and would otherwise strip styling)
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	var got string
	old := clipboardWriter
	clipboardWriter = func(s string) error { got = s; return nil }
	defer func() { clipboardWriter = old }()

	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = m2.(model)
	m.splash = false
	for i := 0; i < 30; i++ {
		m.lines = append(m.lines, line{kind: "info", body: fmt.Sprintf("sel-line-%02d", i)})
	}
	k, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = k.(model)

	// drag: press on row 2, motion to row 4, release (each Update result
	// must be chained back or the next event lands on the pre-drag model)
	k, _ = m.Update(tea.MouseMsg{Type: tea.MouseLeft, Action: tea.MouseActionPress, Y: 2})
	m = k.(model)
	k, _ = m.Update(tea.MouseMsg{Type: tea.MouseMotion, Action: tea.MouseActionMotion, Y: 4})
	m = k.(model)
	k, _ = m.Update(tea.MouseMsg{Type: tea.MouseRelease, Action: tea.MouseActionRelease, Y: 4})
	m = k.(model)
	if m.toast != "copied to clipboard" {
		t.Fatalf("release should toast %q, got %q", "copied to clipboard", m.toast)
	}
	// clipboard must hold exactly viewport rows 2..4 (the visible tail of
	// the transcript lives in vpLines; row 2 is NOT transcript line 2)
	want := ""
	for i := 2; i <= 4; i++ {
		want += stripANSI(m.vpLines[i]) + "\n"
	}
	want = strings.TrimRight(want, "\n")
	if got != want {
		t.Fatalf("clipboard got %q, want the selected viewport rows %q", got, want)
	}
	if !m.selActive() {
		t.Fatal("release cleared the selection — highlight must stay visible (opencode parity)")
	}
	sl, sh := m.selRows()
	if sl != 2 || sh != 4 {
		t.Fatalf("selection rows = [%d,%d], want [2,4]", sl, sh)
	}
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatal("selected rows are not painted (no inverse-video SGR in frame)")
	}

	// ctrl+c re-copies the live selection and must NOT quit
	k, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = k.(model)
	if m.quitting {
		t.Fatal("ctrl+c with a live selection must copy, not quit")
	}

	// esc clears the selection FIRST (press 1), and only then reaches
	// Interrupt (press 2)
	if !m.status.Spinning {
		m.status.Spinning = true
	}
	m.agent = &syncInterruptAPI{}
	k, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = k.(model)
	if m.selActive() {
		t.Fatal("esc did not clear the selection")
	}
	k, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = k.(model)
	if !m.agent.(*syncInterruptAPI).hit {
		t.Fatal("esc should reach Interrupt after the selection is cleared")
	}
}

// TestCtrlCWithoutSelectionQuits keeps the plain quit path intact.
func TestCtrlCWithoutSelectionQuits(t *testing.T) {
	m := newTestModel(t)
	k, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := k.(model)
	if !m2.quitting {
		t.Fatal("ctrl+c with no selection must quit")
	}
}
