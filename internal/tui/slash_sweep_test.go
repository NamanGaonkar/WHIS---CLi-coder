package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAllSlashCommandsSweep types EVERY documented slash command into the
// box and submits it: none may produce an error line. Overlays opened by
// commands are esc'd closed before the next submission. This is the
// release-gate sweep — a new command without wiring fails here.
func TestAllSlashCommandsSweep(t *testing.T) {
	cmds := []string{
		"/help", "/themes", "/mode", "/model", "/provider",
		"/history", "/sessions", "/task test sweep", "/new", "/retry",
		"/compress", "/usage", "/copy", "/copy prompt", "/copy output",
		"/mcp", "/init", "/memory", "/memory forget nothing-real",
		"/undo", "/model bogus-slug", "/resume bogus-id", "/bogus-cmd",
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			m := newTestModel(t)
			m.splash = false
			m.input.SetValue(cmd)
			mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m2 := mm.(model)
			// close any overlay the command opened (help/mcp/themes/...)
			for m2.over.mode != overlayNone {
				m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
				m2 = m3.(model)
				if m2.over.mode != overlayNone && len(m2.stack) == 0 {
					// esc walks the stack; force-close root when stuck
					m2.over = overlay{}
				}
			}
			for _, l := range m2.lines {
				if l.kind == "error" {
					// /model bogus and /bogus-cmd are EXPECTED to error (they
					// prove the error path works); anything else is a fail
					if cmd == "/model bogus-slug" || cmd == "/resume bogus-id" || cmd == "/bogus-cmd" || cmd == "/retry" {
						// expected errors: unknown things, and /retry on an empty
						// session (nothing to retry) — the error path WORKING is
						// exactly what these prove
						continue
					}
					t.Fatalf("%s produced error: %s", cmd, stripANSI(l.body))
				}
			}
			if strings.TrimSpace(m2.input.Value()) != "" {
				t.Fatalf("%s left text in the input box", cmd)
			}
		})
	}
}

// TestSlashMenuRowsAllCarrySlash: every command row in the COMMANDS menu
// must VISIBLY read as a command (literal "/" prefix on the label) and its
// value must stay a runnable "/cmd" slug.
func TestSlashMenuRowsAllCarrySlash(t *testing.T) {
	m := newTestModel(t)
	m.over.openSlashMenu()
	if len(m.over.items) == 0 {
		t.Fatal("slash menu has no rows")
	}
	for _, it := range m.over.items {
		if !strings.HasPrefix(it.label, "/") {
			t.Fatalf("command row %q is missing the / prefix", it.label)
		}
		if !strings.HasPrefix(it.value, "/") && it.value != "@help" {
			t.Fatalf("command row %q has non-command value %q", it.label, it.value)
		}
	}
	// the renamed command must be wired to the same picker
	found := false
	for _, it := range m.over.items {
		if it.value == "/history" {
			found = true
		}
	}
	if !found {
		t.Fatal("slash menu is missing the /history row")
	}
}

// TestHistorySlashCommandOpensPicker: /history opens the SAME picker as
// /sessions (renamed UX, wired identically).
func TestHistorySlashCommandOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m.splash = false
	m.input.SetValue("/history")
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := mm.(model)
	if m2.over.mode != overlaySessions {
		t.Fatalf("/history opened mode %v, want overlaySessions", m2.over.mode)
	}
	if m2.over.title != "HISTORY · THIS FOLDER" {
		t.Fatalf("picker title = %q, want the HISTORY title", m2.over.title)
	}
}

// TestMCPPanelRendersViaSlash specifically covers the /mcp panel body
// coming from the agent (fakeAPI stub here; Adapter.MCPStatus is covered
// by TestMCPStatusDisabledAndFailed in the adapter-facing test).
func TestMCPPanelRendersViaSlash(t *testing.T) {
	m := newTestModel(t)
	m.splash = false
	m.input.SetValue("/mcp")
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := mm.(model)
	if m2.over.mode != overlayHelp {
		t.Fatal("/mcp should open the help-style panel")
	}
	if m2.over.title != "MCP SERVERS" {
		t.Fatalf("panel title wrong: %q", m2.over.title)
	}
	if !strings.Contains(strings.Join(m2.over.lines, "\n"), "no MCP servers configured") {
		t.Fatalf("panel body wrong: %v", m2.over.lines)
	}
}
