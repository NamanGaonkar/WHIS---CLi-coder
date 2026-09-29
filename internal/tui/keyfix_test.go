package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestQuestionMarkTypesNormally locks the Shift+/ fix: "?" shares the
// physical key with "/" and must land in the input box as a rune — never
// hijack into the theme picker. Themes are on ctrl+t and /themes.
func TestQuestionMarkTypesNormally(t *testing.T) {
	m := newTestModel(t)
	m.splash = false

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m2 := mm.(model)
	if m2.over.mode != overlayNone {
		t.Fatal("? must not open any menu")
	}
	if got := m2.input.Value(); got != "?" {
		t.Fatalf("? must type into the box, got %q", got)
	}

	// ctrl+t still opens themes
	m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if m3.(model).over.mode != overlayThemes {
		t.Fatal("ctrl+t should still open the theme menu")
	}
}
