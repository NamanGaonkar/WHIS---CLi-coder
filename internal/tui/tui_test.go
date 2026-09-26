package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeAPI is a controllable AgentAPI for tests.
type fakeAPI struct {
	evs chan TUIEvent
}

func (f *fakeAPI) Run(prompt string) (<-chan TUIEvent, error) { return f.evs, nil }
func (f *fakeAPI) HandleSlash(cmd string) (string, error)     { return "ok", nil }
func (f *fakeAPI) Status() Status {
	return Status{Model: "test-model", Provider: "test", Branch: "main"}
}
func (f *fakeAPI) Keys() map[string]string               { return map[string]string{} }
func (f *fakeAPI) Workspace() string                     { return "/tmp" }
func (f *fakeAPI) Ready() bool                           { return true }
func (f *fakeAPI) PickModel(slug string) (string, error) { return "model → " + slug, nil }
func (f *fakeAPI) SaveKey(prov, key string)              {}

func newTestModel(t *testing.T) model {
	t.Helper()
	m := New(&fakeAPI{evs: make(chan TUIEvent)}).(model)
	// simulate a reasonable window
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m2.(model)
}

func TestSplashRendersCenteredBanner(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "█") {
		t.Fatal("splash should render the banner block art")
	}
	if !strings.Contains(v, "T O K E N") {
		t.Fatal("splash should render the tagline")
	}
	// banner must be horizontally centered, not glued to the left edge
	lines := strings.Split(v, "\n")
	firstArt := -1
	for i, ln := range lines {
		if strings.Contains(ln, "█") {
			firstArt = i
			break
		}
	}
	if firstArt < 0 {
		t.Fatal("no banner line found")
	}
	lead := len(lines[firstArt]) - len(strings.TrimLeft(lines[firstArt], " "))
	if lead < 10 {
		t.Fatalf("banner looks left-aligned: leading spaces = %d", lead)
	}
}

func TestViewNeverPanicsAtAnySize(t *testing.T) {
	sizes := []tea.WindowSizeMsg{
		{Width: 0, Height: 0}, {Width: 10, Height: 3}, {Width: 40, Height: 8},
		{Width: 80, Height: 24}, {Width: 200, Height: 60},
	}
	for _, sz := range sizes {
		m := New(&fakeAPI{evs: make(chan TUIEvent)}).(model)
		m2, _ := m.Update(sz)
		_ = m2.View()                                      // recover guard inside safeView must hold
		m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter}) // empty input, no-op
		_ = m3.View()
	}
}

func TestSplashToSessionTransition(t *testing.T) {
	m := newTestModel(t)
	if !m.splash {
		t.Fatal("should start in splash mode")
	}
	m.input.SetValue("hello world")
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(model)
	if mm.splash {
		t.Fatal("enter with prompt should dismiss splash")
	}
	if len(mm.lines) == 0 || mm.lines[0].body != "hello world" {
		t.Fatalf("prompt should be recorded, got %v", mm.lines)
	}
}

func TestApprovalFlow(t *testing.T) {
	m := newTestModel(t)
	m.splash = false // approvals only arrive mid-session, after splash is dismissed
	ch := make(chan TUIEvent, 1)
	answered := false
	sc := streamChunk{first: true, ev: TUIEvent{Type: "approval", Text: "run command: go test",
		Approve: func(ok bool) { answered = true; _ = ch }}, rest: make(chan TUIEvent)}
	m2, _ := m.handleChunk(sc)
	mm := m2.(model)
	if mm.approval == "" {
		t.Fatal("approval modal should be set")
	}
	// answer yes
	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	mmm := m3.(model)
	if mmm.approval != "" {
		t.Fatal("y should clear the approval modal")
	}
	if !answered {
		t.Fatal("approve callback should fire")
	}
}
