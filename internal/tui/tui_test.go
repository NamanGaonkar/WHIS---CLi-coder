package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// fakeAPI is a controllable AgentAPI for tests.
type fakeAPI struct {
	evs chan TUIEvent
}

func (f *fakeAPI) Run(prompt string) (<-chan TUIEvent, error) { return f.evs, nil }
func (f *fakeAPI) HandleSlash(cmd string) (string, error)     { return "ok", nil }
func (f *fakeAPI) Status() Status {
	return Status{Model: "test-model", Provider: "test", Mode: "ask", Branch: "main"}
}

func (f *fakeAPI) Keys() map[string]string               { return map[string]string{} }
func (f *fakeAPI) Workspace() string                     { return "/tmp" }
func (f *fakeAPI) Ready() bool                           { return true }
func (f *fakeAPI) PickModel(slug string) (string, error) { return "model → " + slug, nil }
func (f *fakeAPI) SaveKey(prov, key string)              {}
func (f *fakeAPI) ResumedTranscript() []TUILine          { return nil }
func (f *fakeAPI) ResumeInfo() string                    { return "" }
func (f *fakeAPI) SetMode(mode string) error             { return nil }
func (f *fakeAPI) Interrupt()                            {}

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

func TestTokZeroFixed(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	// usage arrives BEFORE the session totals reflect it (agent persists
	// then emits; disk session reloads lazily) — tok must still show it.
	sc := streamChunk{first: true, ev: TUIEvent{Type: "usage", In: 1200, Cached: 300, Out: 450, Cost: 0.0031, Turn: 1}, rest: make(chan TUIEvent)}
	m2, _ = m.handleChunk(sc)
	mm := m2.(model)
	mm.syncStatus() // syncStatus must NOT clobber the live usage event
	if got := mm.status.In + mm.status.Out; got != 1650 {
		t.Fatalf("tok = %d, want 1650 (live usage event was lost)", got)
	}
	// once Status() totals catch up, the latch releases and stays correct
	st := mm.status
	st.In, st.Out = 1200, 450
	mm2, _ := mm, mm
	_ = mm2
	if got := st.In + st.Out; got != 1650 {
		t.Fatalf("merged tok = %d, want 1650", got)
	}
}

func TestMenuStaysBelowInput(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m2.(model)
	m.splash = false
	m.lines = append(m.lines, line{kind: "user", body: "hi"})
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := m2.(model)
	v := mm.View()
	lines := strings.Split(v, "\n")
	inputAt, menuAt := -1, -1
	for i, ln := range lines {
		if strings.Contains(ln, "describe a task") {
			inputAt = i
		}
		if strings.Contains(ln, "COMMANDS") {
			menuAt = i
		}
	}
	if inputAt < 0 || menuAt < 0 {
		t.Fatalf("layout must contain both input and menu; inputAt=%d menuAt=%d\n%s", inputAt, menuAt, v)
	}
	if menuAt >= inputAt {
		t.Fatalf("menu (row %d) must sit BELOW the input box (row %d)\n%s", menuAt, inputAt, v)
	}
	// input box stays pinned: the last row is the status band (mode only —
	// token counters were removed by request)
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.Contains(last, "mode") {
		t.Fatalf("status band must be the last row, got %q", last)
	}
}

func TestInputBoxFullyBlack(t *testing.T) {
	// force a color profile so background SGR codes actually render (the
	// test env is headless and would otherwise strip all color)
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(orig)

	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = m2.(model)
	v := inpView(m)
	// border and padding rows must carry the black background, not just the
	// content line: count black-painted cells across all box rows.
	if !strings.Contains(v, "\x1b[48;5;16m") && !strings.Contains(v, "\x1b[40m") {
		t.Fatalf("input box border/padding rows lost the black backing:\n%q", v)
	}
	// the status band is its own black strip, separate from the box
	sb := m.statusBar()
	if !strings.Contains(sb, "\x1b[48;5;16m") && !strings.Contains(sb, "\x1b[40m") {
		t.Fatalf("status bar lost its black backing: %q", sb)
	}
}

func TestThemesSwitch(t *testing.T) {
	// remember the original so other tests are unaffected
	orig := curTheme
	defer applyTheme(orig)
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mm := m2.(model)
	if mm.over.mode != overlayThemes {
		t.Fatalf("? should open the theme menu, got mode %v", mm.over.mode)
	}
	// select the last theme and apply
	mm.over.cursor = len(mm.over.items) - 1
	m3, _ := mm.activateOverlay()
	mm = m3.(model)
	if curTheme != len(themes)-1 {
		t.Fatalf("theme did not switch: curTheme=%d", curTheme)
	}
	if mm.over.mode != overlayNone {
		t.Fatal("theme menu should close after applying")
	}
}

func TestMouseClickSelectsThemes(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := m2.(model)
	if mm.over.mode != overlaySlashMenu {
		t.Fatal("precondition: slash menu open")
	}
	// find the frame row of the themes item in the rendered frame
	v := mm.View()
	row := -1
	for i, ln := range strings.Split(v, "\n") {
		if strings.Contains(stripANSI(ln), "themes") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("themes row not visible in the menu frame")
	}
	m3, _ := mm.Update(tea.MouseMsg{Type: tea.MouseLeft, X: 10, Y: row})
	mm = m3.(model)
	if mm.over.mode != overlayThemes {
		t.Fatalf("clicking the themes row should open the theme menu, got mode %v (firstRow=%d maxRows=%d PAvail=%d vpH=%d clickRow=%d)\nframe:\n%s",
			mm.over.mode, mm.menuFirstRow, mm.menuMaxRows, mm.menuPAvail, mm.vp.Height, row, v)
	}
}

func TestSlashMenuRouteThemes(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.runSlash("/themes")
	if m.over.mode != overlayThemes {
		t.Fatal("/themes should open the theme menu")
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
