package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Version is set from main.
var Version = "0.1.0"

// renderMD renders markdown with a dark glamour theme (falls back to plain).
func renderMD(md string, width int) string {
	if width < 20 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(glamour.WithWordWrap(width-2), glamour.WithStandardStyle("dark"))
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}

// model is the Bubble Tea root model.
type model struct {
	agent           AgentAPI
	vp              viewport.Model
	input           textarea.Model
	width           int
	height          int
	lines           []line // transcript lines (pre-rendered ANSI)
	streamBuf       string // streaming prose buffer (plain string: model is copied by value)
	planBuf         string // plan/reasoning buffer
	planOpen        bool
	splash          bool       // startup banner mode until first prompt
	approval        string     // pending approval action text
	approveFn       func(bool) // pending approval resolver
	status          Status
	quitting        bool
	over            overlay
	customBuf       bool         // collecting a custom openrouter model id
	pendingProvider string       // provider for the custom id / key being entered
	codeOpen        map[int]bool // expanded code blocks per md line index
	mdSeq           int          // monotonic id for md lines
	turnNum         int          // current agent turn (1-based)
	hasDone         bool         // last turn ended with a DONE banner
	busyNoticed     bool         // busy notice shown once per run

	// run timer state: only advances while the agent is working
	runStart  time.Time
	runActive bool
	runLast   time.Duration

	// runSeq guards against stale event chains: each submitted prompt gets a
	// new sequence number; events from older chains are dropped instead of
	// clearing the new run's state (fixes delayed/doubled replies).
	runSeq int
}

// line is one transcript entry.
type line struct {
	kind string // "user" | "md" | "plan" | "tool" | "toolout" | "error" | "info" | "done"
	body string
	n    int // index for codeOpen maps (md lines)
}

// TUILine is a transcript line produced outside the streaming loop.
type TUILine struct {
	Kind string
	Body string
}

// AgentAPI decouples the TUI from the agent package.
type AgentAPI interface {
	Run(prompt string) (<-chan TUIEvent, error)
	HandleSlash(cmd string) (string, error)
	Status() Status
	Keys() map[string]string
	Workspace() string
	Ready() bool
	PickModel(slug string) (string, error)
	SaveKey(provider, key string)
	ResumedTranscript() []TUILine
	ResumeInfo() string
	SetMode(mode string) error
	Interrupt()
}

// TUIEvent bridges agent events into the TUI.
type TUIEvent struct {
	Type            string
	Text            string
	ToolName        string
	ToolArgs        string
	ToolOutput      string
	ToolOK          bool
	Approve         func(bool)
	In, Cached, Out int
	Turn            int
	Cost            float64
	DurationMS      int64
}

// Status feeds the header/status bars.
type Status struct {
	Model           string
	Provider        string
	Mode            string // plan | ask | auto
	Branch          string
	In, Cached, Out int
	Cost            float64
	Spinning        bool
	Spinner         string
	Elapsed         string
	CtxUsed         int
	CtxLimit        int
}

// New creates the initial Bubble Tea model for the main WHIS TUI.
func New(a AgentAPI) tea.Model {
	ta := textarea.New()
	ta.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
	ta.Prompt = ""
	ta.CharLimit = 16384
	ta.ShowLineNumbers = false
	ta.SetWidth(60)
	ta.SetHeight(1)
	ta.Focus()
	vp := viewport.New(80, 20)
	vp.SetContent("")
	return model{agent: a, input: ta, vp: vp, planOpen: true, splash: true}
}

func (m model) Init() tea.Cmd { return textarea.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// textarea content width: full width minus border (2) + padding (2)
		m.input.SetWidth(clampInt(msg.Width-4, 10, msg.Width))
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			m.quitting = true
			return m, tea.Quit
		}
		// keyboard scrolling of the transcript (chat history)
		if m.over.mode == overlayNone {
			switch msg.String() {
			case "pgup", "shift+up", "ctrl+up":
				m.vp.LineUp(m.vp.Height / 2)
				return m, nil
			case "pgdown", "shift+down", "ctrl+down":
				m.vp.LineDown(m.vp.Height / 2)
				return m, nil
			case "alt+up":
				m.vp.LineUp(2)
				return m, nil
			case "alt+down":
				m.vp.LineDown(2)
				return m, nil
			case "end":
				m.vp.GotoBottom()
				return m, nil
			case "up", "down":
				// arrows scroll the chat when the input is EMPTY. Windows
				// ConPTY translates mouse wheel to arrow keys for legacy
				// input, so this path is also what makes the wheel work.
				if strings.TrimSpace(m.input.Value()) == "" {
					if msg.String() == "up" {
						m.vp.LineUp(2)
					} else {
						m.vp.LineDown(2)
					}
					return m, nil
				}
			}
		}

		// overlays capture keys first
		if m.over.mode != overlayNone {
			return m.updateOverlay(msg)
		}

		if m.splash {
			// "/" opens the command menu but keeps the logo on screen
			if msg.String() == "/" {
				m.input.SetValue("")
				m.over.openSlashMenu()
				return m, nil
			}
			// any typing lands in the input; enter dismisses and submits
			if msg.String() == "enter" {
				return m, m.submitPrompt(true)
			}
		} else {
			// esc: answer approval first, then interrupt a running agent
			if msg.String() == "esc" {
				if m.approval != "" {
					m.answerApproval(false)
					return m, nil
				}
				if m.status.Spinning {
					m.agent.Interrupt()
					return m, nil
				}
			}
			// y/n answer a pending approval modal (never steal typing: the
			// input stays focused, so plain letters go into the textarea)
			if (msg.String() == "y" || msg.String() == "n") && m.approval != "" {
				m.answerApproval(msg.String() == "y")
				return m, nil
			}
			if msg.String() == "ctrl+p" {
				m.planOpen = !m.planOpen
				return m, nil
			}
			if msg.String() == "ctrl+o" {
				m.toggleCodeBlocks()
				return m, nil
			}
			if msg.String() == "enter" {
				return m, m.submitPrompt(false)
			}
		}

	case streamChunk:
		return m.handleChunk(msg)

	case streamDone:
		// only a chain for the CURRENT run may end the spinning state;
		// stale chains from interrupted/superseded runs are ignored.
		if msg.seq == m.runSeq {
			m.status.Spinning = false
			m.endRun()
			// force one full repaint so the finished reply is ALWAYS visible
			// even when the renderer skips the final frame.
			m.flushStream()
			return m, tea.ClearScreen
		}
		m.flushStream()
		return m, nil

	case statusTick:
		if m.runActive {
			m.status.Spinner = msg.spinner
			m.status.Elapsed = fmtDur(time.Since(m.runStart))
			m.runLast = time.Since(m.runStart)
		}
		return m, tickStatus()

	case tea.MouseMsg:
		// wheel arrives as Type or Button depending on backend; check both.
		if m.over.mode == overlayHelp {
			switch {
			case msg.Type == tea.MouseWheelUp || msg.Button == tea.MouseButtonWheelUp:
				m.over.scrollHelp(-3)
			case msg.Type == tea.MouseWheelDown || msg.Button == tea.MouseButtonWheelDown:
				m.over.scrollHelp(3)
			}
			return m, nil
		}
		switch {
		case msg.Type == tea.MouseWheelUp || msg.Button == tea.MouseButtonWheelUp:
			m.vp.LineUp(3)
		case msg.Type == tea.MouseWheelDown || msg.Button == tea.MouseButtonWheelDown:
			m.vp.LineDown(3)
		}
		return m, nil
	}

	// typing "/" at an empty input opens the command menu immediately
	if km, ok := msg.(tea.KeyMsg); ok && m.over.mode == overlayNone && !m.splash {
		if km.String() == "/" && strings.TrimSpace(m.input.Value()) == "" {
			m.input.SetValue("")
			m.over.openSlashMenu()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submitPrompt sends the current input as a prompt. While a run is active the
// submit is ignored (typed text stays in the box) so a second agent loop can
// never start and starve the first one's event chain (delayed/doubled reply
// fix). During the splash phase empty submits do nothing.
func (m *model) submitPrompt(splash bool) tea.Cmd {
	v := strings.TrimSpace(m.input.Value())
	if v == "" {
		return nil
	}
	// entering a custom model id (after "custom model id..." in the menu)
	if m.customBuf {
		m.input.SetValue("")
		m.customBuf = false
		m.input.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
		if v != "" && m.pendingProvider != "" {
			slug := m.pendingProvider + ":" + v
			if m.pendingProvider == "openrouter" {
				slug = "or:" + v
			}
			msg, err := m.agent.PickModel(slug)
			if err != nil {
				m.lines = append(m.lines, line{kind: "error", body: err.Error()})
			} else if msg != "" {
				m.lines = append(m.lines, line{kind: "info", body: msg})
			}
			m.pendingProvider = ""
		}
		return nil
	}
	if m.status.Spinning {
		// never queue a second loop; keep the text for the next submit
		if m.runActive && !m.busyNoticed {
			m.busyNoticed = true
			m.lines = append(m.lines, line{kind: "info", body: "agent is still working — esc interrupts, or wait for DONE"})
		}
		return nil
	}
	if splash {
		m.splash = false
	}
	if strings.HasPrefix(v, "/") {
		m.input.SetValue("")
		if v == "/help" {
			if resp, err := m.agent.HandleSlash("/help"); err == nil && resp != "" {
				m.over.openHelp(resp)
			}
			return nil
		}
		return m.runSlash(v)
	}
	if !m.agent.Ready() {
		m.lines = append(m.lines, line{kind: "error", body: "pick a model first — type / and choose model"})
		m.over.openProviderMenu(m.agent.Keys())
		return nil
	}
	m.input.SetValue("")
	m.lines = append(m.lines, line{kind: "user", body: v})
	m.beginRun()
	return m.startPrompt(v)
}

// runSlash executes a slash command typed into the box (menu commands open
// overlays; the rest go to the adapter).
func (m *model) runSlash(v string) tea.Cmd {
	if v == "/quit" || v == "/exit" {
		m.quitting = true
		return tea.Quit
	}
	switch v {
	case "/model", "/provider":
		m.over.openProviderMenu(m.agent.Keys())
		return nil
	case "/sessions":
		m.over.openSessions(m.agent.Workspace())
		return nil
	case "/mode":
		m.over.openModeMenu(m.agent.Status().Mode)
		return nil
	}
	resp, err := m.agent.HandleSlash(v)
	if err != nil {
		m.lines = append(m.lines, line{kind: "error", body: err.Error()})
	} else if resp != "" {
		m.lines = append(m.lines, line{kind: "info", body: resp})
	}
	return nil
}

// beginRun starts the per-run timer and bumps the run sequence.
func (m *model) beginRun() {
	m.runSeq++
	m.runStart = time.Now()
	m.runActive = true
	m.runLast = 0
	m.turnNum = 0
	m.hasDone = false
	m.busyNoticed = false
	m.status.Spinning = true
	m.status.Elapsed = "0s"
}

// endRun freezes the timer at its last value and clears the spinning state.
func (m *model) endRun() {
	if m.runActive {
		m.runLast = time.Since(m.runStart)
	}
	m.runActive = false
	m.status.Spinning = false
	m.status.Elapsed = fmtDur(m.runLast)
}

// answerApproval resolves the pending approval modal synchronously so the
// agent loop unblocks deterministically.
func (m *model) answerApproval(ok bool) {
	m.approval = ""
	if m.approveFn != nil {
		fn := m.approveFn
		m.approveFn = nil
		fn(ok)
	}
}

// updateOverlay handles keys while a menu overlay is open.
func (m model) updateOverlay(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.over = overlay{}
		m.input.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
		return m, m.input.Focus()
	case "up", "k":
		if m.over.mode != overlayKeyInput {
			m.over.move(-1)
		}
		return m, nil
	case "down", "j":
		if m.over.mode != overlayKeyInput {
			m.over.move(1)
		}
		return m, nil
	case "enter":
		if m.over.mode == overlayKeyInput {
			return m.saveKeyAndContinue()
		}
		return m.activateOverlay()
	}
	// help panel: wheel-like keys scroll, esc closes (handled above)
	if m.over.mode == overlayHelp {
		switch msg.String() {
		case "up", "k":
			m.over.scrollHelp(-2)
		case "down", "j":
			m.over.scrollHelp(2)
		case "pgup":
			m.over.scrollHelp(-(m.vp.Height / 2))
		case "pgdown":
			m.over.scrollHelp(m.vp.Height / 2)
		}
		return m, nil
	}
	if m.over.mode == overlayKeyInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// saveKeyAndContinue persists the entered API key, then shows the model list.
func (m model) saveKeyAndContinue() (tea.Model, tea.Cmd) {
	v := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	prov := m.over.provider
	m.input.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
	if v != "" {
		m.agent.SaveKey(prov, v)
		m.lines = append(m.lines, line{kind: "info", body: "key saved for " + prov})
	}
	m.over.openModelMenu(prov, m.agent.Keys(), m.agent.Status().Model, m.agent.Status())
	return m, m.input.Focus()
}

// activateOverlay runs the highlighted menu entry.
func (m model) activateOverlay() (tea.Model, tea.Cmd) {
	it := m.over.current()
	switch m.over.mode {
	case overlaySlashMenu:
		cmd := it.value
		m.over = overlay{}
		m.input.SetValue("")
		switch cmd {
		case "@help":
			if resp, err := m.agent.HandleSlash("/help"); err == nil && resp != "" {
				m.over.openHelp(resp)
			}
			return m, nil
		case "/model", "/provider":
			m.over.openProviderMenu(m.agent.Keys())
			return m, nil
		case "/sessions":
			m.over.openSessions(m.agent.Workspace())
			return m, nil
		case "/mode":
			m.over.openModeMenu(m.agent.Status().Mode)
			return m, nil
		}
		resp, err := m.agent.HandleSlash(cmd)
		if err != nil {
			m.lines = append(m.lines, line{kind: "error", body: err.Error()})
		} else if resp != "" {
			m.lines = append(m.lines, line{kind: "info", body: resp})
		}
		return m, nil

	case overlayProvider:
		if it.value == "" {
			return m, nil
		}
		prov := it.value
		if pNeedsKey(prov) && m.agent.Keys()[prov] == "" {
			m.over.openKeyInput(prov)
			m.input.Placeholder = "paste API key for " + prov + " (enter to save, esc to cancel)"
			return m, m.input.Focus()
		}
		m.over.openModelMenu(prov, m.agent.Keys(), m.agent.Status().Model, m.agent.Status())
		return m, nil

	case overlayModel:
		switch it.value {
		case "@key":
			prov := m.over.provider
			m.over.openKeyInput(prov)
			m.input.Placeholder = "paste API key for " + prov + " (enter to save, esc to cancel)"
			return m, m.input.Focus()
		case "@custom", "":
			if it.disabled {
				return m, nil
			}
			prov := m.over.provider
			m.over = overlay{}
			m.input.Placeholder = "type model id (vendor/name) and press enter"
			m.customBuf = true
			m.pendingProvider = prov
			return m, m.input.Focus()
		}
		slug := it.value
		m.over = overlay{}
		m.input.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
		msg, err := m.agent.PickModel(slug)
		if err != nil {
			m.lines = append(m.lines, line{kind: "error", body: err.Error()})
			if strings.Contains(err.Error(), "key") {
				m.over.openProviderMenu(m.agent.Keys())
			}
		} else if msg != "" {
			m.lines = append(m.lines, line{kind: "info", body: msg})
		}
		return m, nil

	case overlayWorkMode:
		m.over = overlay{}
		if err := m.agent.SetMode(it.value); err != nil {
			m.lines = append(m.lines, line{kind: "error", body: err.Error()})
		} else {
			m.lines = append(m.lines, line{kind: "info", body: "mode -> " + it.value})
		}
		return m, nil

	case overlaySessions:
		if it.value == "" || it.disabled {
			m.over = overlay{}
			return m, nil
		}
		id := it.value
		m.over = overlay{}
		if _, err := m.agent.HandleSlash("/resume " + id); err != nil {
			m.lines = append(m.lines, line{kind: "error", body: err.Error()})
			return m, nil
		}
		// replay the restored conversation into the transcript
		m.lines = nil
		m.streamBuf = ""
		m.planBuf = ""
		m.splash = false
		for _, tl := range m.agent.ResumedTranscript() {
			m.lines = append(m.lines, line{kind: tl.Kind, body: tl.Body})
		}
		if resp := m.agent.ResumeInfo(); resp != "" {
			m.lines = append(m.lines, line{kind: "info", body: resp})
		}
		return m, nil
	}
	return m, nil
}

// startPrompt launches the agent loop as a Bubble Tea command tagged with the
// run sequence so stale chains are recognized and dropped.
func (m model) startPrompt(prompt string) tea.Cmd {
	seq := m.runSeq
	evt, err := m.agent.Run(prompt)
	if err != nil {
		return func() tea.Msg {
			return streamChunk{first: true, seq: seq, ev: TUIEvent{Type: "error", Text: err.Error()}, rest: make(chan TUIEvent)}
		}
	}
	return func() tea.Msg {
		first, ok := <-evt
		if !ok {
			return streamDone{seq: seq}
		}
		return streamChunk{first: true, seq: seq, ev: first, rest: evt}
	}
}

// streamChunk carries one agent event; rest is the remaining channel and seq
// identifies the run this chain belongs to.
type streamChunk struct {
	first bool
	seq   int
	ev    TUIEvent
	rest  <-chan TUIEvent
}

// streamDone ends a run chain; stale ones (seq != current) are ignored.
type streamDone struct {
	seq int
}

type statusTick struct {
	spinner string
}

func tickStatus() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg {
		return statusTick{spinner: nextSpinner()}
	})
}

// handleChunk folds an agent event into the transcript.
func (m model) handleChunk(sc streamChunk) (tea.Model, tea.Cmd) {
	stale := sc.seq != m.runSeq
	if stale {
		// drain stale chains for a superseded run: drain silently
		return m, waitMore(sc.rest, sc.seq)
	}

	ev := sc.ev
	switch ev.Type {
	case "reasoning":
		m.planBuf += ev.Text
	case "text":
		m.streamBuf += ev.Text
	case "plan":
		m.planBuf = ev.Text
		m.flushStream()
	case "notice":
		m.flushStream()
		m.lines = append(m.lines, line{kind: "info", body: ev.Text})
		if strings.HasPrefix(ev.Text, "thinking · turn ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(ev.Text, "thinking · turn ")); err == nil {
				m.turnNum = n
			}
		}
	case "usage":
		// live telemetry mid-run
		if ev.In > 0 || ev.Out > 0 {
			m.status.In, m.status.Cached, m.status.Out = ev.In, ev.Cached, ev.Out
			m.status.Cost += ev.Cost
		}
		if ev.Turn > 0 {
			m.turnNum = ev.Turn
		}
	case "tool_start":
		m.flushStream()
		m.lines = append(m.lines, line{kind: "tool", body: ev.ToolName + " " + ev.ToolArgs})
	case "tool_end":
		m.lines = append(m.lines, line{kind: "toolout", body: ev.ToolOutput})
	case "approval":
		m.flushStream()
		m.approval = ev.Text
		m.approveFn = ev.Approve
	case "turn_done":
		m.flushStream()
		m.status.In, m.status.Cached, m.status.Out = ev.In, ev.Cached, ev.Out
		m.status.Cost += ev.Cost
		m.endRun()
		m.status.Spinning = false
		if strings.Contains(ev.Text, "DONE:") {
			m.hasDone = true
		} else if ev.Text == "" {
			m.hasDone = false
		}
	case "error":
		m.flushStream()
		m.endRun()
		m.status.Spinning = false
		m.lines = append(m.lines, line{kind: "error", body: ev.Text})
	}
	m.syncStatus()
	if sc.first {
		return m, tea.Batch(waitMore(sc.rest, sc.seq), tickStatus())
	}
	return m, waitMore(sc.rest, sc.seq)
}

// flushStream commits buffered prose / plan as transcript lines. A final
// line matching "DONE: ..." is lifted out and rendered as a completion banner
// stamped with the run duration.
func (m *model) flushStream() {
	if m.streamBuf != "" {
		body, summary := splitDone(m.streamBuf)
		if body != "" {
			m.mdSeq++
			m.lines = append(m.lines, line{kind: "md", body: body, n: m.mdSeq})
		}
		if summary != "" {
			m.hasDone = true
			m.lines = append(m.lines, line{kind: "done", body: summary})
		}
		m.streamBuf = ""
	}
	if m.planBuf != "" {
		m.lines = append(m.lines, line{kind: "plan", body: m.planBuf})
		m.planBuf = ""
	}
}

// splitDone extracts a trailing "DONE: ..." line from a reply.
// Returns (remaining body, summary) — summary empty when no footer found.
func splitDone(s string) (string, string) {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "DONE:") {
			summary := strings.TrimSpace(strings.TrimPrefix(t, "DONE:"))
			rest := strings.TrimSpace(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"))
			return rest, summary
		}
	}
	return s, ""
}

// waitMore schedules the next event read for chain seq.
func waitMore(rest <-chan TUIEvent, seq int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rest
		if !ok {
			return streamDone{seq: seq}
		}
		return streamChunk{ev: ev, seq: seq, rest: rest}
	}
}

// syncStatus pulls fresh status from the agent.
func (m *model) syncStatus() {
	if m.agent == nil {
		return
	}
	st := m.agent.Status()
	m.status.Model = st.Model
	m.status.Provider = st.Provider
	m.status.Mode = st.Mode
	m.status.Branch = st.Branch
	m.status.CtxUsed = st.CtxUsed
	m.status.CtxLimit = st.CtxLimit
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fmtDur renders a run duration compactly.
func fmtDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// View renders the whole layout; never panics.
func (m model) View() string { return m.safeView() }

func (m model) safeView() (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = "\n  whis: terminal too small for full render\n"
		}
	}()
	if m.quitting {
		return "whis out.\n"
	}
	if m.width == 0 || m.agent == nil {
		return "booting..."
	}
	if m.splash {
		return m.splashView()
	}
	return m.sessionView()
}

// menuBlock renders the active overlay as a full-width panel (plus the key
// input inside it when entering a key). maxRows caps the body so small
// terminals get a scrollable-short menu instead of an overflowing frame.
func (m model) menuBlock(maxRows int) string {
	body := m.over.view(m.width, maxRows)
	if m.over.mode == overlayKeyInput {
		body += "\n\n" + m.input.View()
	}
	rows := strings.Split(body, "\n")
	if m.over.mode != overlayHelp && maxRows >= 1 && len(rows) > maxRows { // help windows itself
		// keep the title and as many items as fit; the note replaces the
		// last kept row so the result is EXACTLY maxRows rows.
		kept := append([]string{}, rows[:maxRows-1]...)
		kept = append(kept, fmt.Sprintf("... (%d more rows — enlarge terminal)", len(rows)-maxRows+1))
		body = strings.Join(kept, "\n")
	}
	// border 2 cols + padding 4 cols = 6; inner width keeps total == m.width
	inner := clampInt(m.width-6, 20, m.width)
	return menuPanelStyle.Width(inner).Render(body)
}

// splashView is the centered startup screen. The logo remains while menus
// are open — the panel replaces only the lower half.
func (m model) splashView() string {
	var rows []string
	for i, row := range banner {
		rows = append(rows, lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(bannerColors[i%len(bannerColors)])).Render(row))
	}
	bannerBlock := lipgloss.JoinVertical(lipgloss.Center, rows...)

	tag := splashTagStyle.Render("T O K E N - S U R G I C A L   C O D I N G   A G E N T")
	ver := splashHintStyle.Render("v" + Version + " · made by Naman Gaonkar")

	modelLine := splashHintStyle.Render("press / to pick a provider & model")
	if m.agent.Ready() {
		modelLine = splashReadyStyle.Render("[ready] " + m.agent.Status().Model)
	}
	hints := splashHintStyle.Render("/ commands · type a task and press enter · ctrl+c quit")

	if m.over.mode != overlayNone {
		// Exact assembly (lipgloss.Place pads but never clips, so the budget
		// must be computed row by row): head rows + menu panel (menuRows+4
		// for border+padding) + input (inRows+2) + status = height exactly.
		inRows := strings.Count(m.input.View(), "\n") + 1
		avail := clampInt(m.height-6-inRows, 3, m.height) // -1: padding row under input
		headRows := 0
		var head []string
		if m.height >= 20 && m.width >= 30 {
			head = []string{m.splashLogo(m.height < 26), ""}
			headRows = 2
		}
		menuRows := avail - headRows - 4
		if menuRows < 1 {
			head, headRows = nil, 0 // tiny terminal: drop the logo entirely
			menuRows = clampInt(avail-4, 1, 40)
		}
		content := lipgloss.JoinVertical(lipgloss.Left, append(head, m.menuBlock(menuRows))...)
		return content + "\n" + inpView(m) + "\n\n" + m.statusBar()
	}

	inRows := strings.Count(m.input.View(), "\n") + 1
	avail := clampInt(m.height-6-inRows, 3, m.height) // -1: padding row under input
	var content string
	switch {
	case avail >= 11 && m.width >= 58:
		content = lipgloss.JoinVertical(lipgloss.Center, bannerBlock, "", tag, ver, "", modelLine, hints)
	case avail >= 11:
		// narrow: drop the 52-col tagline and the hint row
		content = lipgloss.JoinVertical(lipgloss.Center, bannerBlock, "", ver, "", modelLine)
	default:
		content = lipgloss.JoinVertical(lipgloss.Center, m.splashLogo(true), modelLine)
	}
	body := lipgloss.Place(m.width, avail, lipgloss.Center, lipgloss.Center, content)
	return body + "\n" + inpView(m) + "\n\n" + m.statusBar()
}

// splashLogo renders the full banner, or a one-line wordmark when compact.
func (m model) splashLogo(compact bool) string {
	if !compact {
		var rows []string
		for i, row := range banner {
			rows = append(rows, lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(bannerColors[i%len(bannerColors)])).Render(row))
		}
		return lipgloss.JoinVertical(lipgloss.Center, rows...)
	}
	return lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(bannerColors[3])).Render("W  H  I  S")
}

// inpView renders the full-width input box. lipgloss Width() is the CONTENT
// width: border (2) + padding (2) add on top, so content = width-4 makes the
// box exactly terminal-width, equal to the menu panel and status bar.
func inpView(m model) string {
	return inputStyle.Width(clampInt(m.width-4, 16, m.width)).Render(m.input.View())
}

// sessionView is the main layout after the first prompt.
func (m model) sessionView() string {
	var b strings.Builder

	// header ribbon: timer ONLY while the agent is working
	left := fmt.Sprintf(" WHIS · %s", m.status.Model)
	spin := ""
	if m.status.Spinning {
		spin = " " + m.status.Spinner
	}
	mode := m.agent.Status().Mode
	right := fmt.Sprintf("%s · %s%s ", strings.ToUpper(mode), m.status.Branch, spin)
	if m.status.Spinning {
		right += m.status.Elapsed + " "
	}
	// render the ribbon as ONE styled row: Width(content) + 2 padding cols
	// = exactly terminal width. Two separate renders double-counted padding.
	fill := m.width - 4 - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 1 {
		fill = 1
	}
	ribbon := left + strings.Repeat(" ", fill) + right
	b.WriteString(headerStyle.Width(clampInt(m.width-2, 10, m.width)).MaxWidth(m.width).Render(ribbon) + "\n")

	// viewport width is shared by every layout branch (scrollbar column)
	vw := clampInt(m.width-1, 10, m.width)
	// wasBottom is read BEFORE the content swap: if the user was at the
	// bottom of the OLD content, follow the new output; if they scrolled
	// up to read history, leave the view alone.
	wasBottom := m.vp.AtBottom()

	// overlay-open layout first: the menu panel REPLACES the transcript
	// (viewport shrinks to what is left, or drops out on tiny terminals).
	// Budget: header 1 + sep 1 + panel (maxRows+4) + sep + status 1.
	if m.over.mode != overlayNone {
		panelMax := clampInt(m.height-8, 1, 40)
		if vpH := m.height - panelMax - 8; vpH >= 3 {
			m.vp.Height = vpH
			m.vp.SetContent(m.renderTranscript(vw))
			if wasBottom {
				m.vp.GotoBottom()
			}
			b.WriteString(vpWithScrollbar(m.vp) + "\n")
		}
		b.WriteString(m.menuBlock(panelMax) + "\n" + m.statusBar())
		return b.String()
	}

	// transcript viewport with follow-lock (see wasBottom above).
	m.vp.Width = vw
	inRows := strings.Count(m.input.View(), "\n") + 1
	// non-viewport rows: header 1 + vp separator 1 + input (inRows+2 border)
	// + padding row 1 + status 1 = inRows + 6.
	m.vp.Height = clampInt(m.height-6-inRows, 3, m.height)
	m.vp.SetContent(m.renderTranscript(vw))
	if wasBottom {
		m.vp.GotoBottom()
	}
	b.WriteString(vpWithScrollbar(m.vp) + "\n")

	// approval modal
	if m.approval != "" {
		mod := warnStyle.Render(" APPROVAL REQUIRED ") + " " + m.approval +
			dimStyle.Render("   [y] apply · [n] skip · [esc] cancel")
		b.WriteString(mod + "\n")
	}
	b.WriteString(inpView(m) + "\n")

	// padding row under the text field, then the telemetry bar
	b.WriteString("\n" + m.statusBar())
	return b.String()
}

// vpWithScrollbar renders the viewport at exactly one extra column so the
// chat box is the same width as every other box. The last column carries the
// ember scrollbar when content overflows, or a blank column when it fits.
func vpWithScrollbar(vp viewport.Model) string {
	view := vp.View()
	total := maxInt(1, vp.TotalLineCount())
	visible := maxInt(1, vp.Height)
	overflows := total > visible
	track := vp.Height
	thumb := maxInt(1, track*visible/total)
	maxOff := maxInt(1, total-visible)
	pos := clampInt(vp.YOffset*(track-thumb)/maxOff, 0, track-thumb)
	lines := strings.Split(view, "\n")
	for i := range lines {
		if w := visWidth(lines[i]); w > vp.Width {
			lines[i] = clipANSI(lines[i], vp.Width)
		} else if w < vp.Width {
			lines[i] += strings.Repeat(" ", vp.Width-w)
		}
		if !overflows {
			lines[i] += " "
			continue
		}
		if i >= pos && i < pos+thumb {
			lines[i] += scrollThumbStyle.Render("▐")
		} else {
			lines[i] += scrollTrackStyle.Render("│")
		}
	}
	return strings.Join(lines, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// statusBar is the simplified bottom bar: tokens used, run timer (while
// working), and current mode — nothing else.
func (m model) statusBar() string {
	tok := m.status.In + m.status.Out
	timer := ""
	if m.status.Spinning {
		timer = " · " + m.status.Elapsed + " · esc stops"
	}
	mode := m.agent.Status().Mode
	bar := fmt.Sprintf(" tok %s · mode %s%s", commify(tok), mode, timer)
	// width-2 content + 2 padding = full terminal width, matching every box
	return barStyle.Width(clampInt(m.width-2, 10, m.width)).MaxWidth(m.width).Render(bar)
}

// renderTranscript renders all transcript lines with styling. Assistant md
// lines render with code blocks collapsed (ctrl+o expands them). Every line
// is hard-clipped to vw: glamour/goldmark ignore width on narrow terminals
// and emit wide styled lines that would wrap and corrupt the layout.
func (m model) renderTranscript(vw int) string {
	var parts []string
	for _, l := range m.lines {
		switch l.kind {
		case "user":
			parts = append(parts, badgeStyle.Render("you")+okStyle.Render(" "+l.body))
		case "md":
			parts = append(parts, renderCollapsible(l.body, vw, m.codeOpenFor(l.n)))
		case "plan":
			parts = append(parts, planStyle.Render(truncateLines(l.body, vw-4, 8)))
		case "tool":
			parts = append(parts, toolStyle.Render("> "+l.body))
		case "toolout":
			parts = append(parts, diffStyle(l.body))
		case "done":
			elapsed := fmtDur(m.runLast)
			parts = append(parts, doneStyle.Render(" DONE ")+doneTextStyle.Render(" "+l.body+" ")+dimStyle.Render(" "+elapsed))
		case "info":
			parts = append(parts, dimStyle.Render("· "+l.body))
		case "error":
			parts = append(parts, errStyle.Render("x "+l.body))
		}
	}
	if m.streamBuf != "" {
		if m.status.Spinning {
			// while working: show a live line count, not the dumping text
			lines := strings.Count(strings.TrimSpace(m.streamBuf), "\n") + 1
			parts = append(parts, workingStyle.Render(fmt.Sprintf("... composing reply (%d lines so far)", lines)))
		} else {
			parts = append(parts, renderCollapsible(m.streamBuf, vw, m.codeOpenFor(-1)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// ANSI-aware hard clip to the viewport width. Block structure (blank
	// line between paragraphs/blocks) is preserved exactly.
	clipped := make([]string, 0, len(parts))
	for _, p := range parts {
		var bl []string
		for _, ln := range strings.Split(p, "\n") {
			bl = append(bl, clipANSI(ln, vw))
		}
		clipped = append(clipped, strings.Join(bl, "\n"))
	}
	return strings.Join(clipped, "\n\n")
}

// stripANSI removes all ANSI escape sequences from a line. CSI form:
// ESC '[' params final — the '[' introducer (0x5B) sits inside the final
// byte range, so it must be skipped explicitly or parsing ends early.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == '[' {
				continue // introducer, keep scanning
			}
			if r >= 0x40 && r <= 0x7e {
				inEsc = false // final byte ends the sequence
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// visWidth counts DISPLAY cells of a styled line: ANSI stripped, then
// measured with lipgloss (handles wide CJK/box-drawing runes that a raw
// rune count undercounts, which used to misplace the scrollbar).
func visWidth(s string) int { return lipgloss.Width(stripANSI(s)) }

// clipANSI truncates a styled line to w visible cells, copying escape
// sequences verbatim and appending a hard reset so styles never bleed.
func clipANSI(s string, w int) string {
	if visWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if r == '[' {
				continue // introducer, sequence continues
			}
			if r >= 0x40 && r <= 0x7e { // CSI final byte ends the sequence
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		rw := lipgloss.Width(string(r)) // true display width of the rune
		if used+rw > w {
			continue // drop overflow runes, keep trailing escapes
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// codeOpenFor returns the expansion map for an md line (-1 = streaming buf).
func (m model) codeOpenFor(n int) map[int]bool {
	out := map[int]bool{}
	for k, v := range m.codeOpen {
		if k/1000 == n+1 { // high bits encode the line, low bits the segment
			out[k%1000] = v
		}
	}
	return out
}

// toggleCodeBlocks expands every code block in the newest reply, or collapses
// them if any is open. Bound to ctrl+o so plain letters stay typeable.
func (m *model) toggleCodeBlocks() {
	if m.codeOpen == nil {
		m.codeOpen = map[int]bool{}
	}
	target := -1
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind == "md" {
			target = m.lines[i].n
			break
		}
	}
	if target < 0 {
		return
	}
	segs := splitFences(m.lines[len(m.lines)-1].body)
	codeCount := 0
	anyOpen := false
	for i, s := range segs {
		if s.code {
			codeCount++
			if m.codeOpen[(target+1)*1000+i] {
				anyOpen = true
			}
		}
	}
	if codeCount == 0 {
		return
	}
	for i := 0; i < codeCount; i++ {
		m.codeOpen[(target+1)*1000+i] = !anyOpen
	}
}

// diffStyle colorizes +/- lines in tool output (diffs, logs).
func diffStyle(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "+"):
			lines[i] = diffAdd.Render(ln)
		case strings.HasPrefix(ln, "-"):
			lines[i] = diffDel.Render(ln)
		default:
			lines[i] = dimStyle.Render(ln)
		}
	}
	return strings.Join(lines, "\n")
}

func truncateLines(s string, width, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("... (+%d lines, Ctrl+P toggles plan pane)", len(lines)-maxLines))
	}
	for i, ln := range lines {
		if width > 0 && len(ln) > width {
			lines[i] = ln[:width] + "..."
		}
	}
	return strings.Join(lines, "\n")
}

func commify(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ",")
}
