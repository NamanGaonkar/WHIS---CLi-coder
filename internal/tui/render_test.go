package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// rcEntries counts live render-cache entries.
func rcEntries() int { return len(rc.entries) }

// testModel builds a minimal renderable model (mirrors New() without the
// agent binding).
func testModel() model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.SetWidth(60)
	ta.SetHeight(1)
	vp := viewport.New(80, 20)
	vp.SetContent("")
	return model{input: ta, vp: vp, planOpen: true, codeOpen: map[int]bool{}}
}

// TestRenderCacheHits verifies the memoized transcript renderer actually
// serves repeated frames from cache: rendering the same model twice must
// produce identical output and ZERO new cache entries the second time. This
// is the regression test for paste/esc lag (every keystroke used to re-run
// glamour over the whole transcript).
func TestRenderCacheHits(t *testing.T) {
	rcReset()
	defer rcReset()

	m := testModel()
	m.runStart = time.Now()
	m.runLast = 42 * time.Second
	long := strings.Repeat("paragraph with `inline code` and **bold** text. ", 40)
	m.lines = []line{
		{kind: "user", body: "make a folder with html css js"},
		{kind: "md", n: 1, body: "## Plan\n\n" + long + "\n\n```go\nfunc main() {}\n```"},
		{kind: "tool", body: "run_command mkdir site"},
		{kind: "toolout", body: "ok"},
		{kind: "done", body: "created 3 files", stamp: "42s"},
	}

	// Frame 1: cold — every line renders and caches.
	out1 := m.renderTranscriptCached(100)
	n := rcEntries()
	if n == 0 {
		t.Fatal("expected cache entries after first render, got 0")
	}

	// Frame 2 (the "paste another keystroke" frame): warm — identical
	// output, zero new entries.
	out2 := m.renderTranscriptCached(100)
	if out1 != out2 {
		t.Fatal("cached render differs from cold render")
	}
	if got := rcEntries(); got != n {
		t.Fatalf("warm frame added %d cache entries (wanted 0)", got-n)
	}

	// streamBuf tail (the streaming placeholder) must not poison the cache.
	m.status.Spinning = true
	m.streamBuf = "partial reply so far"
	_ = m.renderTranscriptCached(100)
	if got := rcEntries(); got != n {
		t.Fatalf("streaming tail changed cache size: %d -> %d", n, got)
	}
}

// TestRenderCacheInvalidation verifies the two mutation paths that MUST
// re-render: toggling a collapsed code block, and a theme switch (rcReset).
func TestRenderCacheInvalidation(t *testing.T) {
	rcReset()
	defer rcReset()

	m := testModel()
	m.lines = []line{{kind: "md", n: 1, body: "text\n\n```js\nlet b;\n```"}}

	before := m.renderTranscriptCached(90)
	n := rcEntries()

	// open the first code segment: signature changes -> re-render
	// (high bits encode the md line: n=1 -> key 2000)
	m.codeOpen[2000] = true
	after := m.renderTranscriptCached(90)
	if after == before {
		t.Fatal("code-open toggle did not change the render")
	}
	if got := rcEntries(); got <= n {
		t.Fatal("expected a re-render (new cache entry) after code toggle")
	}

	// theme change bumps the generation: whole cache drops
	rcReset()
	if got := rcEntries(); got != 0 {
		t.Fatalf("rcReset left %d entries", got)
	}
}

// TestEsc responsiveness guard: the esc keypress path (approval -> interrupt)
// must complete synchronously with no channel reads — regression for esc
// lagging behind the render backlog.
func TestEscInterruptsSynchronously(t *testing.T) {
	m := testModel()
	m.status = Status{Spinning: true}
	m.agent = &syncInterruptAPI{}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !m.agent.(*syncInterruptAPI).hit {
		t.Fatal("esc did not reach Interrupt while spinning")
	}
}

// syncInterruptAPI records Interrupt calls; everything else is a stub.
type syncInterruptAPI struct{ hit bool }

func (f *syncInterruptAPI) Run(string) (<-chan TUIEvent, error) { return nil, nil }
func (f *syncInterruptAPI) HandleSlash(string) (string, error)  { return "", nil }
func (f *syncInterruptAPI) Status() Status                      { return Status{} }
func (f *syncInterruptAPI) Keys() map[string]string             { return nil }
func (f *syncInterruptAPI) Workspace() string                   { return "." }
func (f *syncInterruptAPI) Ready() bool                         { return true }
func (f *syncInterruptAPI) PickModel(string) (string, error)    { return "", nil }
func (f *syncInterruptAPI) SaveKey(string, string)              {}
func (f *syncInterruptAPI) RetryPrompt() (string, bool)         { return "", false }
func (f *syncInterruptAPI) LastAssistantText() string           { return "" }
func (f *syncInterruptAPI) LastUserText() string                { return "" }
func (f *syncInterruptAPI) MCPStatus() string                   { return "" }
func (f *syncInterruptAPI) ResumedTranscript() []TUILine        { return nil }
func (f *syncInterruptAPI) ResumeInfo() string                  { return "" }
func (f *syncInterruptAPI) SetMode(string) error                { return nil }
func (f *syncInterruptAPI) Interrupt()                          { f.hit = true }
