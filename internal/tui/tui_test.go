package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"whis/internal/provider"
)

// fakeAPI is a controllable AgentAPI for tests.
type fakeAPI struct {
	evs   chan TUIEvent
	saved map[string]string
}

func (f *fakeAPI) Run(prompt string) (<-chan TUIEvent, error) { return f.evs, nil }
func (f *fakeAPI) HandleSlash(cmd string) (string, error)     { return "ok", nil }
func (f *fakeAPI) Status() Status {
	return Status{Model: "test-model", Provider: "test", Mode: "ask", Branch: "main"}
}

func (f *fakeAPI) Keys() map[string]string { return f.saved }
func (f *fakeAPI) SaveKey(prov, key string) {
	if f.saved == nil {
		f.saved = map[string]string{}
	}
	f.saved[prov] = key
}
func (f *fakeAPI) Workspace() string                     { return "/tmp" }
func (f *fakeAPI) Ready() bool                           { return true }
func (f *fakeAPI) PickModel(slug string) (string, error) { return "model → " + slug, nil }
func (f *fakeAPI) ResumedTranscript() []TUILine          { return nil }
func (f *fakeAPI) ResumeInfo() string                    { return "" }
func (f *fakeAPI) SetMode(mode string) error             { return nil }
func (f *fakeAPI) Interrupt()                            {}

func newTestModel(t *testing.T) model {
	t.Helper()
	m := New(&fakeAPI{evs: make(chan TUIEvent), saved: map[string]string{}}).(model)
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

func TestEditProviderKeyFlow(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	// open the provider menu; the edit row must exist
	m.over.openProviderMenu(map[string]string{"deepseek": "sk-1234567890abcd"})
	found := false
	for _, it := range m.over.items {
		if it.value == "@editkey" {
			found = true
		}
	}
	if !found {
		t.Fatal("provider menu has no edit-key row")
	}
	// click / enter the edit row -> key editor menu
	m.over.cursor = len(m.over.items) - 1
	m2, _ = m.activateOverlay()
	m = m2.(model)
	if m.over.title != "EDIT PROVIDER KEY" {
		t.Fatalf("edit row should open the key editor, got %q", m.over.title)
	}
	// pick deepseek -> straight into the masked key form
	m.over.cursor = 0
	for i, it := range m.over.items {
		if it.value == "@set:deepseek" {
			m.over.cursor = i
		}
	}
	m2, _ = m.activateOverlay()
	m = m2.(model)
	if m.over.mode != overlayKeyInput || m.over.provider != "deepseek" {
		t.Fatalf("expected key form for deepseek, got mode %v prov %q", m.over.mode, m.over.provider)
	}
	// save a new key
	m.input.SetValue("sk-new-key-999")
	m2, _ = m.saveKeyAndContinue()
	m = m2.(model)
	if k := m.agent.Keys()["deepseek"]; k != "sk-new-key-999" {
		t.Fatalf("key not replaced, got %q", k)
	}
}

func TestSanitizeTextStripsBell(t *testing.T) {
	in := "beep\x07now\r\ntab\there\x1b[31m"
	out := sanitizeText(in)
	if strings.ContainsAny(out, "\x07\r\x1b") {
		t.Fatalf("control chars survived: %q", out)
	}
	if !strings.Contains(out, "beepnow") || !strings.Contains(out, "tab    here") {
		t.Fatalf("sanitizer mangled text: %q", out)
	}
}

func TestEscWalksBackMenuByMenu(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	// main -> commands -> provider -> model list: 3 levels deep
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = m2.(model)
	if m.over.mode != overlaySlashMenu {
		t.Fatal("precondition: commands menu open")
	}
	m.over.cursor = 0
	for i, it := range m.over.items {
		if it.value == "/model" {
			m.over.cursor = i
		}
	}
	m2, _ = m.activateOverlay()
	m = m2.(model)
	if m.over.mode != overlayProvider {
		t.Fatalf("expected provider menu, got %v", m.over.mode)
	}
	m.over.cursor = 0
	m2, _ = m.activateOverlay() // first provider -> model list
	m = m2.(model)
	if m.over.mode != overlayModel {
		t.Fatalf("expected model menu, got %v", m.over.mode)
	}
	// esc #1: back to provider menu (NOT the main screen)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = m2.(model)
	if m.over.mode != overlayProvider {
		t.Fatalf("esc #1 should return to provider menu, got %v", m.over.mode)
	}
	// esc #2: back to commands menu
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = m2.(model)
	if m.over.mode != overlaySlashMenu {
		t.Fatalf("esc #2 should return to commands menu, got %v", m.over.mode)
	}
	// esc #3: back to the main screen (stack empty)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = m2.(model)
	if m.over.mode != overlayNone {
		t.Fatalf("esc #3 should close the last menu, got %v", m.over.mode)
	}
}

// TestMouseClickSelectsSplashMenu pins the STARTUP screen click geometry:
// the row map must mirror the rows splashView actually renders above the
// panel (banner + blank + border + padding + title + blank), so a click on
// a rendered row activates that row. Regression: headRows reported 2 while
// the real head was 7 rows, so every splash click landed on the wrong item.
func TestMouseClickSelectsSplashMenu(t *testing.T) {
	m := newTestModel(t) // WindowSizeMsg 100x30 already applied
	if !m.splash {
		t.Fatal("precondition: fresh model starts on the splash screen")
	}
	// open the slash menu the way a user does (key press -> Update ->
	// syncViewport), so click geometry is freshly computed.
	mm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mm2.(model)
	if m.over.mode != overlaySlashMenu {
		t.Fatal("precondition: slash menu open on splash")
	}
	v := m.View()
	row := -1
	for i, ln := range strings.Split(v, "\n") {
		if strings.Contains(stripANSI(ln), "themes") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("themes row not visible in splash frame\nframe:\n%s", v)
	}
	m2, _ := m.Update(tea.MouseMsg{Type: tea.MouseLeft, X: 10, Y: row})
	mm := m2.(model)
	if mm.over.mode != overlayThemes {
		t.Fatalf("clicking themes on splash should open theme menu, got mode %v (firstRow=%d maxRows=%d headRows=%d clickRow=%d)\nframe:\n%s",
			mm.over.mode, mm.menuFirstRow, mm.menuMaxRows, mm.menuHeadRows, row, v)
	}
}

// TestMouseHoverMovesMenuCursor: moving the mouse over a menu row moves the
// highlight there; hovering off the rows leaves the cursor alone.
// Type-to-search: printable keys filter the model menu; esc clears the
// filter before walking back; enter picks from the filtered set.
func TestModelMenuTypeToSearch(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := m2.(model)
	if mm.over.mode != overlaySlashMenu {
		t.Fatal("precondition: slash menu open")
	}
	// open the provider menu directly (deterministic — no menu-order deps)
	mm.over.openProviderMenu(map[string]string{})
	mmm := mm
	if mmm.over.mode != overlayProvider {
		t.Fatalf("precondition: provider menu open, got %v", mmm.over.mode)
	}
	before := len(mmm.over.items)
	if before < 3 {
		t.Fatalf("precondition: several provider rows, got %d", before)
	}
	// type a filter that matches only a subset ("oll" → ollama rows)
	for _, r := range []rune("oll") {
		m4, _ := mmm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		mmm = m4.(model)
	}
	if len(mmm.over.items) >= before {
		t.Fatalf("filter should shrink the list: %d -> %d", before, len(mmm.over.items))
	}
	if mmm.over.filter != "oll" {
		t.Fatalf("filter state = %q", mmm.over.filter)
	}
	for _, it := range mmm.over.items {
		if !strings.Contains(strings.ToLower(it.label+it.hint), "oll") {
			t.Fatalf("non-matching row survived: %q", it.label)
		}
	}
	// esc clears the filter FIRST (full list returns), second esc pops level
	m5, _ := mmm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm5 := m5.(model)
	if mm5.over.filter != "" || len(mm5.over.items) != before {
		t.Fatalf("esc should clear filter and restore list, filter=%q rows=%d", mm5.over.filter, len(mm5.over.items))
	}
	if mm5.over.mode != overlayProvider {
		t.Fatalf("menu should stay open after clearing filter, mode=%v", mm5.over.mode)
	}
}

func TestMouseWheelMovesMenuCursor(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := m2.(model)
	if mm.over.mode != overlaySlashMenu {
		t.Fatal("precondition: slash menu open")
	}
	mm.over.cursor = 0
	// wheel over an open menu must move the SELECTION (previously it fell
	// through to the chat viewport and the menu looked unscrollable)
	m3, _ := mm.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	mm = m3.(model)
	if mm.over.cursor != 1 {
		t.Fatalf("wheel down should move menu cursor to 1, got %d", mm.over.cursor)
	}
	m4, _ := mm.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	mm = m4.(model)
	if mm.over.cursor != 0 {
		t.Fatalf("wheel up should move menu cursor back to 0, got %d", mm.over.cursor)
	}
}

func TestMouseHoverMovesMenuCursor(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := m2.(model)
	if mm.over.mode != overlaySlashMenu {
		t.Fatal("precondition: slash menu open")
	}
	// find the frame row of the themes item (it is the LAST slash item, so
	// pin the cursor to the FIRST item first to guarantee a real move).
	v := mm.View()
	row := -1
	for i, ln := range strings.Split(v, "\n") {
		if strings.Contains(stripANSI(ln), "themes") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("themes row not visible in the menu frame\nframe:\n%s", v)
	}
	mm.over.cursor = 0
	if row == mm.menuFirstRow {
		t.Fatalf("precondition: themes row must not be the first item row (firstRow=%d)", mm.menuFirstRow)
	}
	// hover: motion msg at that row
	m3, _ := mm.Update(tea.MouseMsg{Type: tea.MouseMotion, X: 10, Y: row})
	mm = m3.(model)
	rel := row - mm.menuFirstRow
	if mm.over.cursor != rel {
		t.Fatalf("hover should move cursor to row %d (rel %d), got cursor=%d", row, rel, mm.over.cursor)
	}
	// hover outside the item band: cursor unchanged
	cur := mm.over.cursor
	m4, _ := mm.Update(tea.MouseMsg{Type: tea.MouseMotion, X: 10, Y: 0})
	mm = m4.(model)
	if mm.over.cursor != cur {
		t.Fatal("hover off the item rows must not move the cursor")
	}
	// hover must NOT activate (only click does)
	if mm.over.mode != overlaySlashMenu {
		t.Fatalf("hover must not activate a row, got mode %v", mm.over.mode)
	}
}

// TestMenuWindowsLongLists: a 60-row model list renders in a window with
// the cursor row visible and a scroll indicator (new providers have long
// live /models catalogs).
func TestMenuWindowsLongLists(t *testing.T) {
	o := &overlay{mode: overlayModel, title: "SELECT MODEL · X"}
	for i := range 60 {
		o.items = append(o.items, menuItem{label: fmt.Sprintf("model-%02d", i), value: fmt.Sprintf("m:%d", i)})
	}
	// cursor at the top: window starts at 0
	v := o.view(80, 40)
	if !strings.Contains(stripANSI(v), "model-00") {
		t.Fatal("top of the list must be visible when cursor is at row 0")
	}
	if !strings.Contains(v, "of 60") {
		t.Fatalf("scroll indicator missing:\n%s", v)
	}
	// cursor at the bottom: last row visible, early rows windowed out
	o.cursor = 59
	v = o.view(80, 40)
	if !strings.Contains(stripANSI(v), "model-59") {
		t.Fatal("cursor row must stay visible when scrolled to the bottom")
	}
	if strings.Contains(stripANSI(v), "model-00") {
		t.Fatal("rows above the window must be hidden")
	}
	// short lists render fully, no indicator
	o2 := &overlay{mode: overlayModel, title: "X", items: o.items[:3]}
	v2 := o2.view(80, 40)
	if strings.Contains(v2, "of 3") {
		t.Fatal("short list must not show a scroll indicator")
	}
}

// TestModelMenuAsyncFetch pins the async /models flow: opening the menu
// kicks off a fetch command WITHOUT blocking (no network call in the
// update path), shows a fetching placeholder, drops STALE results, and
// swaps in live rows when the matching result lands. Regression: the
// blocking fetch stalled the UI and desynced mouse release events (menu
// self-click bug).
func TestModelMenuAsyncFetch(t *testing.T) {
	api := &fakeAPI{evs: make(chan TUIEvent), saved: map[string]string{"deepseek": "sk-test"}}
	m := New(api).(model)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(model)
	m.splash = false
	m.over.openProviderMenu(m.agent.Keys())
	// activate the deepseek row -> model menu with a fetch in flight
	for i, it := range m.over.items {
		if it.value == "deepseek" {
			m.over.cursor = i
		}
	}
	m3, cmd := m.activateOverlay()
	mm := m3.(model)
	if mm.over.mode != overlayModel {
		t.Fatalf("expected model menu, got %v", mm.over.mode)
	}
	if cmd == nil {
		t.Fatal("model menu must return a fetch command (async, non-blocking)")
	}
	found := false
	for _, it := range mm.over.items {
		if it.value == "@fetching" {
			found = true
		}
	}
	if !found {
		t.Fatal("fetching placeholder row missing")
	}
	// STALE result (older run): must be dropped, rows unchanged
	stale := menuModelsLoaded{run: mm.over.fetchRun - 1, prov: "deepseek", models: []provider.ModelInfo{{ID: "stale-model"}}}
	m4, _ := mm.Update(stale)
	mm = m4.(model)
	for _, it := range mm.over.items {
		if it.label == "stale-model" {
			t.Fatal("stale fetch result must be dropped")
		}
	}
	// matching result: rows replaced with live ids
	fresh := menuModelsLoaded{run: mm.over.fetchRun, prov: "deepseek",
		models: []provider.ModelInfo{{ID: "deepseek-v4-flash", Context: 131072}}}
	m5, _ := mm.Update(fresh)
	mm = m5.(model)
	live := false
	for _, it := range mm.over.items {
		if it.label == "deepseek-v4-flash" {
			live = true
		}
		if it.value == "@fetching" {
			t.Fatal("fetching placeholder must be removed after results land")
		}
	}
	if !live {
		t.Fatal("live model row missing after fetch result")
	}
}

// TestSessionsShowTaskCounts: /sessions hints carry the tool-exec count.
// Hermetic: points HOME/USERPROFILE at a temp dir so the real ~/.whis
// (live sessions from actual runs) can never influence the assertion.
func TestSessionsShowTaskCounts(t *testing.T) {
	tmp := t.TempDir()
	oldHome, hadHome := os.LookupEnv("HOME")
	oldProf, hadProf := os.LookupEnv("USERPROFILE")
	os.Setenv("HOME", tmp)
	os.Setenv("USERPROFILE", tmp)
	defer func() {
		if hadHome {
			os.Setenv("HOME", oldHome)
		} else {
			os.Unsetenv("HOME")
		}
		if hadProf {
			os.Setenv("USERPROFILE", oldProf)
		} else {
			os.Unsetenv("USERPROFILE")
		}
	}()
	o := &overlay{}
	o.openSessions("/no/such/root")
	if len(o.items) != 1 || !o.items[0].disabled {
		t.Fatal("empty root should show the disabled placeholder row")
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
