package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Version is set from main.
var Version = "0.1.0"

// ANSI accent palette (cybernetic).
var (
	cyber   = lipgloss.Color("#7C3AED") // violet
	cyan    = lipgloss.Color("#22D3EE")
	green   = lipgloss.Color("#34D399")
	red     = lipgloss.Color("#F87171")
	yellow  = lipgloss.Color("#FBBF24")
	dimGray = lipgloss.Color("#4B5563")
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB")).Background(cyber).Padding(0, 1)
	badgeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#0B0F19")).Background(cyan).Padding(0, 1).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(dimGray)
	okStyle     = lipgloss.NewStyle().Foreground(green)
	errStyle    = lipgloss.NewStyle().Foreground(red).Bold(true)
	warnStyle   = lipgloss.NewStyle().Foreground(yellow)
	planStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Border(lipgloss.RoundedBorder()).BorderForeground(dimGray).Padding(0, 1)
	toolStyle   = lipgloss.NewStyle().Foreground(cyan)
	diffAdd     = lipgloss.NewStyle().Foreground(green)
	diffDel     = lipgloss.NewStyle().Foreground(red)
	inputStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#A78BFA")).Padding(0, 1)
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Background(lipgloss.Color("#111827")).Padding(0, 1)
)

// banner is the big WHIS wordmark (ANSI-shadow style) shown on startup,
// one vivid gradient color per row.
var banner = []string{
	"██╗    ██╗ ██╗  ██╗ ██╗ ███████╗",
	"██║    ██║ ██║  ██║ ██║ ██╔════╝",
	"██║ █╗ ██║ ███████║ ██║ ███████╗",
	"██║███╗██║ ██╔══██║ ██║ ╚════██║",
	"╚███╔███╔╝ ██║  ██║ ██║ ███████║",
	" ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝",
}

var bannerColors = []string{"#22D3EE", "#38BDF8", "#818CF8", "#A78BFA", "#E879F9", "#F472B6"}

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
	input           textinput.Model
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
	hasDone         bool         // last turn ended with a DONE banner
	turnNum         int          // current agent turn (1-based)
}

// line is one transcript entry.
type line struct {
	kind string // "user" | "md" | "plan" | "tool" | "toolout" | "error" | "info"
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
} // Status feeds the header/status bars.
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
	ti := textinput.New()
	ti.Placeholder = "describe a task, WHIS handles the rest…  ( / for commands )"
	ti.Focus()
	ti.CharLimit = 8192
	ti.Width = 60
	vp := viewport.New(80, 20)
	vp.SetContent("")
	return model{agent: a, input: ti, vp: vp, planOpen: true, splash: true}
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width = msg.Width
		m.vp.Height = clampInt(msg.Height-7, 3, msg.Height)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			m.quitting = true
			return m, tea.Quit
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
				v := strings.TrimSpace(m.input.Value())
				if v == "" {
					return m, nil
				}
				m.splash = false
				m.input.SetValue("")
				m.lines = append(m.lines, line{kind: "user", body: v})
				if !m.agent.Ready() {
					m.lines = append(m.lines, line{kind: "error", body: "pick a model first — choose one below"})
					m.over.openProviderMenu(m.agent.Keys())
					return m, nil
				}
				m.status.Spinning = true
				return m, m.startPrompt(v)
			}
		} else {
			switch msg.String() {
			case "esc":
				if m.approval != "" {
					m.answerApproval(false)
					return m, nil
				}
				if m.status.Spinning {
					// interrupt the running agent loop (gather/act/verify cycle)
					m.agent.Interrupt()
					return m, nil
				}
			case "y", "Y":
				if m.approval != "" {
					m.answerApproval(true)
					return m, nil
				}
			case "n", "N":
				if m.approval != "" {
					m.answerApproval(false)
					return m, nil
				}
			case "ctrl+p":
				m.planOpen = !m.planOpen
				return m, nil
			case "c":
				m.toggleCode(true)
				return m, nil
			case "t":
				m.toggleCode(false)
				return m, nil
			case "enter":
				v := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				if v == "" {
					return m, nil
				}
				if strings.HasPrefix(v, "/") {
					if v == "/quit" || v == "/exit" {
						m.quitting = true
						return m, tea.Quit
					}
					// menu commands open overlays; the rest go to the adapter
					switch v {
					case "/model":
						m.over.openProviderMenu(m.agent.Keys())
						return m, nil
					case "/provider":
						m.over.openProviderMenu(m.agent.Keys())
						return m, nil
					case "/sessions":
						m.over.openSessions(m.agent.Workspace())
						return m, nil
					}
					resp, err := m.agent.HandleSlash(v)
					if err != nil {
						m.lines = append(m.lines, line{kind: "error", body: err.Error()})
					} else if resp != "" {
						m.lines = append(m.lines, line{kind: "info", body: resp})
					}
					return m, nil
				}
				if !m.agent.Ready() {
					m.lines = append(m.lines, line{kind: "error", body: "pick a model first — type / and choose model"})
					m.over.openProviderMenu(m.agent.Keys())
					return m, nil
				}
				m.lines = append(m.lines, line{kind: "user", body: v})
				m.status.Spinning = true
				return m, m.startPrompt(v)
			}
		}

	case streamChunk:
		return m.handleChunk(msg)

	case streamDone:
		m.status.Spinning = false
		m.flushStream()
		return m, nil

	case statusTick:
		m.syncStatus()
		m.status.Elapsed = msg.elapsed
		m.status.Spinner = msg.spinner
		return m, tickStatus()
	}

	// typing "/" at an empty input opens the command menu immediately
	if km, ok := msg.(tea.KeyMsg); ok && m.over.mode == overlayNone && !m.splash {
		if km.String() == "/" && strings.TrimSpace(m.input.Value()) == "" {
			m.input.SetValue("")
			m.over.openSlashMenu()
			return m, nil
		}
		// submitting a custom model id
		if km.String() == "enter" && m.customBuf {
			v := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			m.customBuf = false
			m.input.Placeholder = "describe a task, WHIS handles the rest…  ( / for commands )"
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
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
		m.input.Placeholder = "describe a task, WHIS handles the rest…  ( / for commands )"
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
	m.input.EchoMode = textinput.EchoNormal
	prov := m.over.provider
	m.input.Placeholder = "describe a task, WHIS handles the rest…  ( / for commands )"
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
			m.input.EchoCharacter = '•'
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
		m.input.Placeholder = "describe a task, WHIS handles the rest…  ( / for commands )"
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
			m.lines = append(m.lines, line{kind: "info", body: "mode → " + it.value})
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

// startPrompt launches the agent loop as a Bubble Tea command.
func (m model) startPrompt(prompt string) tea.Cmd {
	evt, err := m.agent.Run(prompt)
	if err != nil {
		return func() tea.Msg {
			return streamChunk{first: true, ev: TUIEvent{Type: "error", Text: err.Error()}, rest: make(chan TUIEvent)}
		}
	}
	return func() tea.Msg {
		first, ok := <-evt
		if !ok {
			return streamDone{}
		}
		return streamChunk{first: true, ev: first, rest: evt}
	}
}

// streamChunk carries one agent event; rest is the remaining channel.
type streamChunk struct {
	first bool
	ev    TUIEvent
	rest  <-chan TUIEvent
}

type streamDone struct{}

type statusTick struct {
	elapsed string
	spinner string
}

func tickStatus() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg {
		return statusTick{elapsed: elapsedString(startClock), spinner: nextSpinner()}
	})
}

// handleChunk folds an agent event into the transcript.
func (m model) handleChunk(sc streamChunk) (tea.Model, tea.Cmd) {
	ev := sc.ev
	switch ev.Type {
	case "reasoning":
		m.planBuf += ev.Text
	case "text":
		m.streamBuf += ev.Text
	case "notice":
		m.flushStream()
		m.lines = append(m.lines, line{kind: "info", body: ev.Text})
		if strings.HasPrefix(ev.Text, "thinking · turn ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(ev.Text, "thinking · turn ")); err == nil {
				m.turnNum = n
			}
		}
	case "usage":
		// live telemetry mid-run: update bars without waiting for turn end
		if ev.In > 0 || ev.Out > 0 {
			m.status.In, m.status.Cached, m.status.Out = ev.In, ev.Cached, ev.Out
			m.status.Cost += ev.Cost
			m.status.Elapsed = fmt.Sprintf("%ds", int(time.Duration(ev.DurationMS)*time.Millisecond/time.Second)+1)
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
		m.status.Spinning = false
		m.status.In, m.status.Cached, m.status.Out = ev.In, ev.Cached, ev.Out
		m.status.Cost += ev.Cost
		if ev.DurationMS > 0 {
			m.status.Elapsed = (time.Duration(ev.DurationMS) * time.Millisecond).Round(time.Second).String()
		}
		if strings.Contains(ev.Text, "DONE:") {
			m.hasDone = true
		} else if ev.Text == "" {
			m.hasDone = false
		}
	case "error":
		m.flushStream()
		m.status.Spinning = false
		m.lines = append(m.lines, line{kind: "error", body: ev.Text})
	}
	m.syncStatus()
	if sc.first {
		return m, tea.Batch(waitMore(sc.rest), tickStatus())
	}
	return m, waitMore(sc.rest)
}

// flushStream commits buffered prose / plan as transcript lines. A final
// line matching "DONE: ..." is lifted out and rendered as a completion banner.
func (m *model) flushStream() {
	if m.streamBuf != "" {
		body, summary := splitDone(m.streamBuf)
		if body != "" {
			m.mdSeq++
			m.lines = append(m.lines, line{kind: "md", body: body, n: m.mdSeq})
		}
		if summary != "" {
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

// waitMore schedules the next event read.
func waitMore(rest <-chan TUIEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rest
		if !ok {
			return streamDone{}
		}
		return streamChunk{ev: ev, rest: rest}
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
		return "booting…"
	}
	if m.splash {
		return m.splashView()
	}
	return m.sessionView()
}

// menuBlock renders the active overlay as a full-width panel (plus the key
// input inside it when entering a key). Width accounts for the border and
// padding so the right edge always closes cleanly at the terminal edge.
func (m model) menuBlock() string {
	body := m.over.view(m.width)
	if m.over.mode == overlayKeyInput {
		body += "\n\n" + m.input.View()
	}
	// border 2 cols + padding 4 cols = 6; inner width keeps total == m.width
	inner := clampInt(m.width-6, 20, m.width)
	return menuPanelStyle.Width(inner).Render(body)
}

// splashView is the centered Claude-Code-style startup screen. The logo
// remains while menus are open — the panel replaces only the lower half.
func (m model) splashView() string {
	var rows []string
	for i, row := range banner {
		rows = append(rows, lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(bannerColors[i%len(bannerColors)])).Render(row))
	}
	bannerBlock := lipgloss.JoinVertical(lipgloss.Center, rows...)

	tag := splashTagStyle.Render("T O K E N - S U R G I C A L   C O D I N G   A G E N T")
	ver := splashHintStyle.Render("v" + Version + " · byo-key · local or remote")

	modelLine := splashHintStyle.Render("press / to pick a provider & model")
	if m.agent.Ready() {
		modelLine = splashReadyStyle.Render("[ready] " + m.agent.Status().Model)
	}
	hints := splashHintStyle.Render("/ commands · type a task and press enter · ctrl+c quit")

	if m.over.mode != overlayNone {
		content := lipgloss.JoinVertical(lipgloss.Left, bannerBlock, "", tag, "", m.menuBlock())
		bodyH := clampInt(m.height-2, 3, m.height)
		body := lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Top, content)
		return body + "\n" + m.statusBar()
	}

	content := lipgloss.JoinVertical(lipgloss.Center, bannerBlock, "", tag, ver, "", modelLine, hints)
	bodyH := clampInt(m.height-3, 3, m.height)
	body := lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, content)
	// border 2 cols = 2; inner width keeps total == m.width
	inp := inputStyle.Width(clampInt(m.width-2, 20, m.width)).Render(m.input.View())
	return body + "\n" + inp + "\n" + m.statusBar()
}

// sessionView is the main layout after the first prompt.
func (m model) sessionView() string {
	var b strings.Builder

	// header ribbon
	left := fmt.Sprintf(" WHIS · %s", m.status.Model)
	spin := ""
	if m.status.Spinning {
		spin = " " + m.status.Spinner
	}
	mode := m.agent.Status().Mode
	right := fmt.Sprintf("%s · %s%s %s ", strings.ToUpper(mode), m.status.Branch, spin, m.status.Elapsed)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(headerStyle.Render(left) + strings.Repeat(" ", gap) + headerStyle.Render(right) + "\n")

	// transcript viewport: auto-scroll to the newest output on every render
	// so long agent runs never look "stuck" on old content.
	m.vp.Width = m.width
	m.vp.Height = clampInt(m.height-7, 3, m.height)
	m.vp.SetContent(m.renderTranscript())
	m.vp.GotoBottom()
	b.WriteString(m.vp.View() + "\n")

	// approval modal
	if m.approval != "" {
		mod := warnStyle.Render(" APPROVAL REQUIRED ") + " " + m.approval +
			dimStyle.Render("   [y] apply · [n] skip · [esc] cancel")
		b.WriteString(mod + "\n")
	}

	// full-width overlay panel; header stays visible above it
	if m.over.mode != overlayNone {
		b.WriteString(m.menuBlock() + "\n")
		return b.String() + m.statusBar()
	} // full-width input box (border included in the width math)
	b.WriteString(inputStyle.Width(clampInt(m.width-2, 20, m.width)).Render(m.input.View()) + "\n")

	// telemetry status bar
	b.WriteString(m.statusBar())
	return b.String()
}

// statusBar is the persistent telemetry bar: tokens, cache, cost, context
// gauge, and the live turn counter + timer while the agent runs.
func (m model) statusBar() string {
	st := m.agent.Status()
	in, cached, out, cost := st.In, st.Cached, st.Out, st.Cost
	hitPct := 0
	if in > 0 {
		hitPct = cached * 100 / in
	}
	bar := fmt.Sprintf(" tokens %s/%s · cache %d%% · $%.4f · ctx %s",
		commify(in), commify(out), hitPct, cost, ctxGauge(st.CtxUsed, st.CtxLimit))
	if m.status.Spinning && m.turnNum > 0 {
		bar += workingStyle.Render(fmt.Sprintf(" · turn %d · %s · esc to stop", m.turnNum, m.status.Elapsed))
	}
	return barStyle.Width(m.width - 1).MaxWidth(m.width - 1).Render(bar)
}

// renderTranscript renders all transcript lines with styling. Assistant md
// lines render with code blocks collapsed (c expands the targeted block).
func (m model) renderTranscript() string {
	var parts []string
	for _, l := range m.lines {
		switch l.kind {
		case "user":
			parts = append(parts, badgeStyle.Render("you")+okStyle.Render(" "+l.body))
		case "md":
			parts = append(parts, renderCollapsible(l.body, m.width, m.codeOpenFor(l.n)))
		case "plan":
			parts = append(parts, planStyle.Render(truncateLines(l.body, m.width-4, 8)))
		case "tool":
			parts = append(parts, toolStyle.Render("> "+l.body))
		case "toolout":
			parts = append(parts, diffStyle(l.body))
		case "done":
			elapsed := m.status.Elapsed
			if elapsed == "" {
				elapsed = "0s"
			}
			parts = append(parts, doneStyle.Render(" DONE ")+doneTextStyle.Render(" "+l.body+" ")+dimStyle.Render(" "+elapsed))
		case "info":
			parts = append(parts, dimStyle.Render("· "+l.body))
		}
	}
	if m.streamBuf != "" {
		if m.status.Spinning {
			// while working: show a live line count, not the dumping text
			lines := strings.Count(strings.TrimSpace(m.streamBuf), "\n") + 1
			parts = append(parts, workingStyle.Render(fmt.Sprintf("… composing reply (%d lines so far)", lines)))
		} else {
			parts = append(parts, renderCollapsible(m.streamBuf, m.width, m.codeOpenFor(-1)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
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

// toggleCode flips expansion of the code segment nearest the last output.
func (m *model) toggleCode(open bool) {
	if m.codeOpen == nil {
		m.codeOpen = map[int]bool{}
	}
	// find the last md line
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
	for _, s := range segs {
		if s.code {
			codeCount++
		}
	}
	if codeCount == 0 {
		return
	}
	for i := 0; i < codeCount; i++ {
		m.codeOpen[(target+1)*1000+i] = open
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
		lines = append(lines[:maxLines], fmt.Sprintf("… (+%d lines, Ctrl+P toggles plan pane)", len(lines)-maxLines))
	}
	for i, ln := range lines {
		if width > 0 && len(ln) > width {
			lines[i] = ln[:width] + "…"
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

func ctxGauge(used, limit int) string {
	if limit <= 0 {
		return "—"
	}
	pct := used * 100 / limit
	bars := pct / 5
	if bars > 20 {
		bars = 20
	}
	return "[" + strings.Repeat("█", bars) + strings.Repeat("░", 20-bars) + fmt.Sprintf(" %d%%", pct) + "]"
}
