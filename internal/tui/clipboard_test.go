package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

// TestCtrlYCopiesLastReply presses ctrl+y on a finished conversation and
// expects the transcript to gain a "copied" confirmation info line.
func TestCtrlYCopiesLastReply(t *testing.T) {
	m := newTestModel(t)
	m.splash = false
	m.lines = []line{
		{kind: "user", body: "hello"},
		{kind: "md", body: "here is the answer"},
	}

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := mm.(model)

	found := false
	for _, l := range m2.lines {
		if l.kind == "info" && strings.Contains(stripANSI(l.body), "copied") {
			found = true
		}
	}
	if !found {
		t.Fatal("ctrl+y should append a copied confirmation line")
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
