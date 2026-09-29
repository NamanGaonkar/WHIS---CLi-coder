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

	"whis/internal/provider"
)

// Version is set from main at boot (release builds inject the tag).
var Version = "0.1.4"

// TakeoverMsg is sent to a running whis when another window takes over its
// workspace folder (single-instance rule). The holder flushes its session
// and exits politely; the takeover window then starts fresh.
type TakeoverMsg struct{}

// menuModelsLoaded replaces the open model menu's rows with the live
// /models fetch result. Carries the request run number so a STALE fetch
// (user reopened the menu for another provider meanwhile) is dropped.
type menuModelsLoaded struct {
	run    int
	prov   string
	models []provider.ModelInfo
	err    error
}

// applyModelFetch swaps the fetching placeholder for the live rows (or an
// error note when the fetch failed; the static catalog rows stay).
func (m *model) applyModelFetch(msg menuModelsLoaded) {
	var rows []menuItem
	for _, it := range m.over.items {
		if it.value != "@fetching" {
			rows = append(rows, it)
		}
	}
	if msg.err != nil || len(msg.models) == 0 {
		reason := "no models visible for this key"
		if msg.err != nil {
			reason = msg.err.Error()
		}
		if len(rows) == 0 {
			rows = append(rows, menuItem{label: "no models found", hint: reason, disabled: true})
		} else {
			rows = append(rows, menuItem{label: "live fetch failed — showing catalog", hint: reason, disabled: true})
		}
		m.over.items = rows
		if m.over.cursor >= len(rows) {
			m.over.cursor = len(rows) - 1
		}
		return
	}
	m.over.fillModelItems(msg.prov, m.agent.Status().Model, m.agent.Status(), msg.models)
	provider.SyncCatalogWithPricing(msg.prov, msg.models)
}

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

	pasteExpand bool // ctrl+e: expand the clipped input pane for huge pastes
	// one queued live-usage event: syncStatus() runs after every event and
	// would otherwise overwrite the fresh tok numbers the usage event just
	// set (root cause of "tok 0" persisting after each turn).
	usageWait *TUIEvent
	// usageMerged latches once session.Totals() catches up with the last
	// live-usage event, so tok never flickers back to a stale value.
	usageMerged bool

	// click geometry for overlay menus: frame row of the first item and the
	// number of item rows actually visible. Computed in syncViewport (which
	// runs after every Update), never in View (whose mutations are dumped).
	menuFirstRow int
	menuMaxRows  int
	// overlay layout decision, made once per sync: rows shed from the bottom
	// (0 = full layout, 1 = no pad/status, 2 = menu is the whole screen) and
	// the panel body-row budget actually available.
	menuShed     int
	menuPAvail   int
	menuHeadRows int // splash: rows of logo head above the menu panel (0/2)

	// overlay back-stack: opening a menu from inside another menu pushes the
	// previous one, so esc walks BACK menu-by-menu (model list -> provider ->
	// commands -> main) instead of dumping to the main screen.
	stack []overlay

	// ghost double-click debounce: bubbletea v1.2.4 also reports the button
	// RELEASE as a MouseLeft a few ms after the press, so one physical click
	// fires twice and the second hit lands on the NEW menu (e.g. click
	// "themes" -> the theme menu picks an item by itself). Drop ANY click
	// within 350ms of the previous one: humans never re-click that fast,
	// ghost releases always are that fast. (Fixed upstream in bubbletea
	// v1.3.0; revisit after any dependency bump.)
	lastClick time.Time
}

// pushOverlay saves the current overlay for esc-to-return. No-op when no
// menu is open (menus opened from the main screen are their own root).
func (m *model) pushOverlay() {
	if m.over.mode == overlayNone {
		return
	}
	if len(m.stack) >= 8 {
		m.stack = m.stack[1:]
	}
	m.stack = append(m.stack, m.over)
}

// line is one transcript entry.
type line struct {
	kind  string // "user" | "md" | "plan" | "tool" | "toolout" | "error" | "info" | "done"
	body  string
	n     int    // index for codeOpen maps (md lines)
	stamp string // frozen elapsed label for done lines (render-cache safe)
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
	RetryPrompt() (string, bool)
	LastAssistantText() string
	LastUserText() string
	MCPStatus() string
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
	ta.CharLimit = 500000
	ta.MaxHeight = 512
	ta.ShowLineNumbers = false
	ta.SetWidth(60)
	ta.SetHeight(1)
	ta.Focus()
	vp := viewport.New(80, 20)
	vp.SetContent("")
	return model{agent: a, input: ta, vp: vp, planOpen: true, splash: true}
}

func (m model) Init() tea.Cmd { return textarea.Blink }

// Update syncs the viewport AFTER every message is applied. CRITICAL: the
// viewport MUST be fed content here and NOT in View() — View's mutations
// happen on a value copy that Bubble Tea discards, so any SetContent done
// there is lost and scrolling acts on an empty viewport (the bug that made
// wheel/arrows dead in real runs). The input pane height is synced here too
// (one place covers every mutation path: typing, paste, submits, clears).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m2, cmd := m.update(msg)
	mm := m2.(model)
	mm.syncInputHeight()
	mm.syncViewport()
	return mm, cmd
}

// syncViewport re-renders the transcript into the viewport with correct
// dimensions and preserves the follow-lock (stick to bottom unless the
// user scrolled up).
func (m *model) syncViewport() {
	if m.width <= 0 || m.quitting {
		return
	}
	vw := clampInt(m.width-1, 10, m.width) // last col reserved for scrollbar
	wasBottom := m.vp.AtBottom()
	m.vp.Width = vw
	if m.over.mode != overlayNone && !m.splash && m.over.mode == overlayHelp {
		// help overlay: old half/half layout
		h := (m.height - 9) / 2
		if h < 3 {
			h = 0
		}
		m.vp.Height = clampInt(h, 0, 40)
		m.menuShed = 0
		if h >= 3 {
			m.menuPAvail = m.height - h - 9
		} else {
			m.menuPAvail = m.height - 8
		}
		m.menuPAvail = clampInt(m.menuPAvail, 1, 40)
	} else if m.over.mode != overlayNone && !m.splash {
		// item menus get the rows they NEED; the chat shrinks to fit
		// (opencode-style: the menu is the screen, the transcript yields).
		// No menu item is ever truncated on a normal window — last-row items
		// like "themes" stay visible and clickable.
		// Frame rows (vp shown): 1 header + 1 sep + vpH + 1 sep + (body+4)
		// panel + 1 sep + (inRows+2) input + 1 blank + 1 status.
		inRows := strings.Count(m.input.View(), "\n") + 1
		bodyRows := m.over.bodyRows(inRows)
		// cap the menu to what fits WITH the input visible — a menu taller
		// than the screen pushed the input box off-screen entirely
		if maxBody := m.height - inRows - 10; bodyRows > maxBody {
			if maxBody < 4 {
				maxBody = 4
			}
			bodyRows = maxBody // view() windows the items; menu scrolls
		}
		m.menuShed = 0
		m.menuPAvail = bodyRows
		if h := m.height - bodyRows - inRows - 12; h >= 3 {
			m.vp.Height = clampInt(h, 3, 40)
		} else if m.height >= bodyRows+inRows+10 {
			m.vp.Height = 0 // chat hidden; full menu, nothing shed
		} else if m.height >= bodyRows+inRows+8 {
			m.vp.Height = 0
			m.menuShed = 1 // drop pad + status rows (input stays)
		} else if m.height >= bodyRows+4 {
			m.vp.Height = 0
			m.menuShed = 2 // drop the input box too: menu is the screen
		} else {
			m.vp.Height = 0
			m.menuShed = 2
			// header(1) + panel border/padding(4) stay; menu takes the rest
			m.menuPAvail = clampInt(m.height-5, 1, 40)
		}
	} else if m.over.mode != overlayNone {
		// splash + menu, REAL frame accounting: head(headRows) + panel
		// frame(border+padding=4) + body(menuPAvail: title+blank+items+nav)
		// + blank 1 + input(inRows+2) + blank 1 + status 1.
		inRows := strings.Count(m.input.View(), "\n") + 1
		headRows := 0
		if m.height >= 20 && m.width >= 30 {
			// MUST mirror splashView's head exactly: full banner + blank at
			// >= 26 rows, compact wordmark + blank below that.
			headRows = 2
			if m.height >= 26 {
				headRows = len(banner) + 1
			}
		}
		p := m.height - headRows - 4 - inRows - 4
		if p < 1 && headRows > 0 {
			headRows = 0
			p = m.height - 4 - inRows - 4
		}
		m.menuHeadRows = headRows
		m.menuPAvail = clampInt(p, 1, 40)
	} else {
		inRows := strings.Count(m.input.View(), "\n") + 1
		// non-viewport rows: header 1 + sep 1 + input (inRows+2) + pad 1 + status 1
		m.vp.Height = clampInt(m.height-6-inRows, 3, m.height)
	}
	// memoized render: unchanged transcript lines come from the cache instead
	// of re-running glamour every frame (paste/esc/stream backpressure fix)
	m.vp.SetContent(m.renderTranscriptCached(vw))
	if wasBottom {
		m.vp.GotoBottom()
	}

	// click geometry for overlay menus. Frame ladder, session view:
	//   header 1, sep 1, vp vpH, sep 1, border 1, padding 1, title 1,
	//   blank 1, items... (vp hidden: no vp/sep rows). Splash: head rows
	//   replace header/sep/vp. Visible rows mirror the panel budget minus
	//   the fixed body rows (title + blank + blank + nav = 4).
	m.menuFirstRow, m.menuMaxRows = 0, 0
	if m.over.mode != overlayNone && m.over.mode != overlayHelp {
		if m.splash {
			// head block (banner + blank) then panel frame: border, padding,
			// title, blank -> first item. menuHeadRows now mirrors the rows
			// splashView ACTUALLY renders above the panel.
			m.menuFirstRow = m.menuHeadRows + 4
		} else if m.vp.Height >= 3 {
			m.menuFirstRow = m.vp.Height + 5 // empirically pinned by click tests
		} else {
			m.menuFirstRow = 5
		}
		m.menuMaxRows = m.menuPAvail - 4
		if m.menuMaxRows < 0 {
			m.menuMaxRows = 0
		}
		if m.menuMaxRows > len(m.over.items) {
			m.menuMaxRows = len(m.over.items)
		}
	}
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		} // bracketed paste: the terminal wrapped the clipboard in ESC[200~ /
		// ESC[201~ and bubbletea delivers it as ONE KeyRunes message with
		// Paste=true (its String() deliberately reports "ctrl+v", which makes
		// bubbles' textarea trigger an OS-clipboard read instead of using the
		// actual paste payload). Insert the payload at the cursor directly —
		// verbatim, one shot, and never route through the slash/menu hijacks
		// (a paste starting with "/" on an empty box used to open the command
		// menu and swallow the clipboard as menu input).
		if msg.Paste {
			m.input.InsertString(string(msg.Runes))
			return m, nil
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
				// arrows ALWAYS scroll the chat. Windows ConPTY translates
				// the mouse wheel to arrow keys, so this is also the wheel
				// path. Caret stays editable via left/right/home/end-free.
				if msg.String() == "up" {
					m.vp.LineUp(2)
				} else {
					m.vp.LineDown(2)
				}
				return m, nil
			}
		}

		// overlays capture keys first
		if m.over.mode != overlayNone {
			return m.updateOverlay(msg)
		}

		// menu cycling when the box is empty (opencode parity): / = commands.
		// "?" is deliberately NOT a hijack key: it shares the physical key
		// with "/" (Shift+/) and users must be able to TYPE a question mark
		// as the first character of a prompt. Themes remain on ctrl+t and
		// /themes.
		if strings.TrimSpace(m.input.Value()) == "" {
			if msg.String() == "/" {
				m.input.SetValue("")
				m.over.openSlashMenu()
				return m, nil
			}
		}

		if m.splash {
			// "/" opens the command menu but keeps the logo on screen
			if msg.String() == "/" {
				m.input.SetValue("")
				m.over.openSlashMenu()
				return m, nil
			}
			// "?" types normally on splash too (same Shift+/ physical key as
			// "/"); themes stay on ctrl+t and /themes
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
			if msg.String() == "ctrl+y" {
				return m.copyLast("")
			}
			if msg.String() == "ctrl+e" {
				m.pasteExpand = !m.pasteExpand
				return m, nil
			}
			if msg.String() == "ctrl+t" {
				m.over.openThemes(curTheme)
				return m, nil
			}
			if msg.String() == "enter" {
				return m, m.submitPrompt(false)
			}
		}

	case TakeoverMsg:
		// lost the folder to a new window: save everything, hand over, quit
		m.flushStream()
		m.endRun()
		m.status.Spinning = false
		m.lines = append(m.lines, line{kind: "info", body: "another whis window took over this folder — this session was saved and closed. Resume it with /sessions in the new window."})
		return m, tea.Quit

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

	case menuModelsLoaded:
		// drop stale fetches: menu closed, reopened for another provider,
		// or a newer fetch already in flight
		if msg.run != m.over.fetchRun || m.over.mode != overlayModel || msg.prov != m.over.provider {
			return m, nil
		}
		m.applyModelFetch(msg)
		return m, nil

	case statusTick:
		if m.runActive {
			m.status.Spinner = msg.spinner
			m.status.Elapsed = fmtDur(time.Since(m.runStart))
			m.runLast = time.Since(m.runStart)
		}
		return m, tickStatus()

	case tea.MouseMsg:
		// hover over an overlay menu row moves the highlight there
		// (opencode parity). Requires WithMouseAllMotion; motion arrives as
		// Type MouseMotion with the cursor cell coordinates.
		if msg.Type == tea.MouseMotion && m.over.mode != overlayNone && m.over.mode != overlayHelp && m.menuMaxRows > 0 {
			rel := msg.Y - m.menuFirstRow
			// windowed menus render items[lo:hi]; a rendered row maps to item
			// lo+rel (clicking "the 3rd visible row" is NOT items[3] when the
			// list is scrolled)
			lo, _ := m.over.itemWindow(m.menuMaxRows)
			idx := lo + rel
			if rel >= 0 && rel < m.menuMaxRows && idx < len(m.over.items) && !m.over.items[idx].disabled && idx != m.over.cursor {
				m.over.cursor = idx
			}
			return m, nil
		}
		// left click on an overlay menu row selects it (opencode parity)
		if msg.Type == tea.MouseLeft && m.over.mode != overlayNone && m.over.mode != overlayHelp && m.menuMaxRows > 0 {
			// ghost double-click: swallow any click within 350ms of the
			// previous one (bubbletea reports the release as a second click).
			if time.Since(m.lastClick) < 350*time.Millisecond {
				return m, nil
			}
			m.lastClick = time.Now()
			rel := msg.Y - m.menuFirstRow
			// same windowing map as hover: rendered row -> lo+rel
			lo, _ := m.over.itemWindow(m.menuMaxRows)
			idx := lo + rel
			if rel >= 0 && rel < m.menuMaxRows && idx < len(m.over.items) {
				if !m.over.items[idx].disabled {
					m.over.cursor = idx
					return m.activateOverlay()
				}
			}
			return m, nil
		}
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
		// wheel over an open menu (model / provider / sessions / …) moves
		// the selection — previously it fell through to the chat viewport
		// and the menu seemed scroll-less.
		if m.over.mode != overlayNone && m.menuMaxRows > 0 {
			switch {
			case msg.Type == tea.MouseWheelUp || msg.Button == tea.MouseButtonWheelUp:
				m.over.move(-1)
			case msg.Type == tea.MouseWheelDown || msg.Button == tea.MouseButtonWheelDown:
				m.over.move(1)
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
		if v == "/themes" {
			m.over.openThemes(curTheme)
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
	m.lines = append(m.lines, line{kind: "user", body: sanitizeText(v)})
	m.beginRun()
	return m.startPrompt(sanitizeText(v))
}

// asCmd adapts a (model, cmd) pair to contexts that only need the cmd.
func asCmd(_ tea.Model, cmd tea.Cmd) tea.Cmd { return cmd }

// runSlash executes a slash command typed into the box (menu commands open
// overlays; the rest go to the adapter).
func (m *model) runSlash(v string) tea.Cmd {
	if v == "/quit" || v == "/exit" {
		m.quitting = true
		return tea.Quit
	}
	// /retry starts a RUN (not just a message): rewind to the last user
	// prompt and launch the loop again.
	if v == "/retry" {
		p, ok := m.agent.RetryPrompt()
		if !ok {
			m.lines = append(m.lines, line{kind: "error", body: "nothing to retry — no previous prompt in this session"})
			return nil
		}
		m.lines = append(m.lines, line{kind: "user", body: sanitizeText(p) + "   (retry)"})
		m.beginRun()
		return m.startPrompt(sanitizeText(p))
	}
	switch v {
	case "/model", "/provider":
		m.over.openProviderMenu(m.agent.Keys())
		return nil
	case "/sessions":
		m.over.openSessions(m.agent.Workspace())
		return nil
	case "/mcp":
		m.over.openHelp(m.agent.MCPStatus())
		m.over.title = "MCP SERVERS"
		return nil
	case "/mode":
		m.over.openModeMenu(m.agent.Status().Mode)
		return nil
	case "/themes":
		m.over.openThemes(curTheme)
		return nil
	case "/copy":
		return asCmd(m.copyLast(strings.TrimSpace(strings.TrimPrefix(v, "/copy"))))
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
		// a type-to-search filter is cleared FIRST, then the menu pops back
		if m.over.filter != "" {
			m.over.clearFilter()
			return m, nil
		}
		// walk BACK one menu level; only the root esc closes to the main screen
		if n := len(m.stack); n > 0 {
			m.over = m.stack[n-1]
			m.stack = m.stack[:n-1]
			if m.over.mode == overlayKeyInput {
				m.input.Placeholder = "paste API key for " + m.over.provider + " (enter to save, esc to cancel)"
			} else {
				m.input.Placeholder = "describe a task, WHIS handles the rest...  ( / for commands )"
			}
			return m, m.input.Focus()
		}
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
	// type-to-search on model/provider menus: printable runes refine the
	// filter, backspace unwinds it. Runs AFTER key-nav so up/down/enter/esc
	// keep their meanings. "j/k" are nav here, so only non-control runes
	// that aren't handled above land in the filter.
	if m.over.filterable && msg.Type == tea.KeyRunes && len(msg.String()) > 0 {
		for _, r := range msg.String() {
			m.over.typeFilter(r)
		}
		return m, nil
	}
	if msg.String() == "backspace" && m.over.filterable {
		m.over.backspaceFilter()
		return m, nil
	}
	return m, nil
}

// keyShape describes the expected prefix of each provider's API keys so a
// bad paste is caught at entry time (not as a cryptic 401 later).
var keyShape = map[string]string{
	"openrouter": "sk-or-", "openai": "sk-", "deepseek": "sk-",
	"anthropic": "sk-ant-", "groq": "gsk_", "xai": "xai-",
	"gemini": "AQ.", "moonshot": "sk-", "mistral": "", "qwen": "sk-",
	"zai": "", "minimax": "", "ollama-cloud": "",
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
		// instant sanity check: warn when the key does not match the vendor's
		// known shape (the #1 cause of mysterious 401s on other machines)
		if want := keyShape[prov]; want != "" && !strings.HasPrefix(v, want) {
			m.lines = append(m.lines, line{kind: "error", body: fmt.Sprintf(
				"hmm — %s keys normally start with %q, yours doesn't. if you get a 401, that's why: re-check the copy from the provider dashboard",
				prov, want)})
		}
	}
	m.pushOverlay()
	cmd := m.over.openModelMenu(prov, m.agent.Keys(), m.agent.Status().Model, m.agent.Status())
	return m, tea.Batch(m.input.Focus(), cmd)
}

// activateOverlay runs the highlighted menu entry.
func (m model) activateOverlay() (tea.Model, tea.Cmd) {
	it := m.over.current()
	switch m.over.mode {
	case overlaySlashMenu:
		cmd := it.value
		m.input.SetValue("")
		switch cmd {
		case "@help":
			m.pushOverlay()
			if resp, err := m.agent.HandleSlash("/help"); err == nil && resp != "" {
				m.over.openHelp(resp)
			}
			return m, nil
		case "/model", "/provider":
			m.pushOverlay()
			m.over.openProviderMenu(m.agent.Keys())
			return m, nil
		case "/sessions":
			m.pushOverlay()
			m.over.openSessions(m.agent.Workspace())
			return m, nil
		case "/mcp":
			m.pushOverlay()
			m.over.openHelp(m.agent.MCPStatus())
			m.over.title = "MCP SERVERS"
			return m, nil
		case "/mode":
			m.pushOverlay()
			m.over.openModeMenu(m.agent.Status().Mode)
			return m, nil
		case "/themes":
			m.pushOverlay()
			m.over.openThemes(curTheme)
			return m, nil
		case "/copy":
			m.over = overlay{}
			return m.copyLast("")
		case "/retry":
			m.over = overlay{}
			if m.status.Spinning {
				m.lines = append(m.lines, line{kind: "error", body: "agent is still working — esc interrupts first"})
				return m, nil
			}
			p, ok := m.agent.RetryPrompt()
			if !ok {
				m.lines = append(m.lines, line{kind: "error", body: "nothing to retry — no previous prompt in this session"})
				return m, nil
			}
			m.lines = append(m.lines, line{kind: "user", body: sanitizeText(p) + "   (retry)"})
			m.beginRun()
			return m, m.startPrompt(sanitizeText(p))
		}
		m.over = overlay{}
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
		// "edit / re-enter a provider key" row: switch to the key editor
		if it.value == "@editkey" {
			m.pushOverlay()
			m.over.openProviderEditMenu(m.agent.Keys())
			return m, nil
		}
		// key editor rows: "@set:<prov>" jumps straight into the key form
		if after, found := strings.CutPrefix(it.value, "@set:"); found {
			m.pushOverlay()
			m.over.openKeyInput(after)
			m.input.Placeholder = "paste NEW API key for " + after + " (enter to save, esc to cancel)"
			return m, m.input.Focus()
		}
		prov := it.value
		if pNeedsKey(prov) && m.agent.Keys()[prov] == "" {
			m.pushOverlay()
			m.over.openKeyInput(prov)
			m.input.Placeholder = "paste API key for " + prov + " (enter to save, esc to cancel)"
			return m, m.input.Focus()
		}
		m.pushOverlay()
		cmd := m.over.openModelMenu(prov, m.agent.Keys(), m.agent.Status().Model, m.agent.Status())
		return m, cmd

	case overlayModel:
		switch it.value {
		case "@fetching":
			return m, nil // live fetch still in flight
		case "@key":
			prov := m.over.provider
			m.pushOverlay()
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

	case overlayThemes:
		idx := curTheme
		for i, th := range themes {
			if th.name == it.value {
				idx = i
			}
		}
		m.over = overlay{}
		applyTheme(idx)
		rcReset() // styles changed: every cached render is stale
		m.lines = append(m.lines, line{kind: "info", body: "theme -> " + themes[idx].name})
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
			m.lines = append(m.lines, line{kind: tl.Kind, body: sanitizeText(tl.Body)})
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
		m.planBuf += sanitizeText(ev.Text)
	case "text":
		m.streamBuf += sanitizeText(ev.Text)
	case "plan":
		m.planBuf = ev.Text
		m.flushStream()
	case "notice":
		m.flushStream()
		m.lines = append(m.lines, line{kind: "info", body: sanitizeText(ev.Text)})
		if strings.HasPrefix(ev.Text, "thinking · turn ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(ev.Text, "thinking · turn ")); err == nil {
				m.turnNum = n
			}
		}
	case "usage":
		// live telemetry mid-run. The agent persists the turn into the session
		// BEFORE emitting this event, but the session JSON on disk only
		// reloads lazily — Status() may still report stale totals here. Hold
		// this event until the totals catch up (tok never resets to 0).
		if ev.In > 0 || ev.Out > 0 {
			m.usageWait = &ev
			m.usageMerged = false
		}
		if ev.Turn > 0 {
			m.turnNum = ev.Turn
		}
	case "tool_start":
		m.flushStream()
		m.lines = append(m.lines, line{kind: "tool", body: sanitizeText(ev.ToolName + " " + ev.ToolArgs)})
	case "tool_end":
		m.lines = append(m.lines, line{kind: "toolout", body: sanitizeText(ev.ToolOutput)})
	case "approval":
		m.flushStream()
		m.approval = ev.Text
		m.approveFn = ev.Approve
	case "turn_done":
		m.flushStream()
		if ev.In > 0 || ev.Out > 0 {
			m.usageWait = &ev
			m.usageMerged = false
		}
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
		m.lines = append(m.lines, line{kind: "error", body: sanitizeText(ev.Text)})
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
			if m.runLast == 0 {
				m.runLast = time.Since(m.runStart)
			}
			m.lines = append(m.lines, line{kind: "done", body: summary, stamp: fmtDur(m.runLast)})
		}
		m.streamBuf = ""
	}
	if m.planBuf != "" {
		if cp := cleanPlan(m.planBuf); cp != "" {
			m.lines = append(m.lines, line{kind: "plan", body: cp})
		}
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
} // syncStatus pulls fresh status from the agent. Token counters come from
// the session totals, but a just-arrived usage event may be ahead of them —
// keep the live numbers visible until the totals catch up (fixes "tok 0").
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
	if m.usageWait != nil && !m.usageMerged {
		if st.In >= m.usageWait.In && st.Out >= m.usageWait.Out {
			m.usageMerged = true // totals caught up; release the live event
		} else {
			m.status.In = m.usageWait.In
			m.status.Cached = m.usageWait.Cached
			m.status.Out = m.usageWait.Out
			if m.usageWait.Cost > 0 {
				m.status.Cost = m.usageWait.Cost
			}
		}
	}
	if m.usageMerged && m.usageWait != nil && m.usageWait.Cost > 0 && m.status.Cost < m.usageWait.Cost {
		m.status.Cost = m.usageWait.Cost
	}
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

// menuBlock renders the active overlay as a full-width panel. The body-row
// budget was computed in syncViewport (menuPAvail); the cap here only guards
// against a stale call path. Key-entry embeds the input view in the body.
func (m model) menuBlock() string {
	maxRows := m.menuPAvail
	if maxRows < 1 {
		maxRows = 1
	}
	body := m.over.view(m.width, maxRows)
	if m.over.mode == overlayKeyInput {
		body += "\n\n" + m.input.View()
	}
	// border 2 cols + padding 4 cols = 6; inner width keeps total == m.width
	inner := clampInt(m.width-6, 20, m.width)
	rows := strings.Split(body, "\n")
	if m.over.mode != overlayHelp && maxRows >= 1 && len(rows) > maxRows {
		// keep the title and as many items as fit; the note replaces the
		// last kept row so the result is EXACTLY maxRows rows.
		kept := append([]string{}, rows[:maxRows-1]...)
		kept = append(kept, fmt.Sprintf("... (%d more rows - enlarge terminal)", len(rows)-maxRows+1))
		body = strings.Join(kept, "\n")
	}
	// hard-clip every row to the CONTENT width (inner minus the 2+2 padding)
	// AFTER capping: lipgloss Width() wraps long rows, and wrapped rows blow
	// the exact row budget on narrow terminals (40-col overflow regression).
	var clippedRows []string
	for _, ln := range strings.Split(body, "\n") {
		clippedRows = append(clippedRows, clipANSI(ln, inner-4))
	}
	body = strings.Join(clippedRows, "\n")
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

	modelLine := splashHintStyle.Render("press / to pick a provider & model")
	if m.agent.Ready() {
		modelLine = splashReadyStyle.Render("[ready] " + m.agent.Status().Model)
	}
	hints := splashHintStyle.Render("/ commands · type a task and press enter · ctrl+c quit")

	if m.over.mode != overlayNone {
		// Splash + menu: syncViewport computed menuPAvail and menuHeadRows
		// for this exact assembly (head + panel + sep + input + blank + status).
		var head []string
		if m.menuHeadRows > 0 {
			// FULL banner while a menu is open (user preference); the row
			// math in syncViewport accounts for these exact rows.
			head = []string{m.splashLogo(m.height < 26), ""}
		}
		content := lipgloss.JoinVertical(lipgloss.Left, append(head, m.menuBlock())...)
		return content + "\n" + inpView(m) + "\n\n" + m.statusBar()
	}

	inRows := strings.Count(m.input.View(), "\n") + 1
	avail := clampInt(m.height-6-inRows, 3, m.height) // -1: padding row under input
	var content string
	switch {
	case avail >= 11 && m.width >= 58:
		content = lipgloss.JoinVertical(lipgloss.Center, bannerBlock, "", tag, "", modelLine, hints)
	case avail >= 11:
		// narrow: drop the 52-col tagline and the hint row
		content = lipgloss.JoinVertical(lipgloss.Center, bannerBlock, "", modelLine)
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
//
// Large content is CLIPPED IN VIEW ONLY: the textarea keeps every byte (the
// model always receives the full prompt — bubbles' MaxHeight would silently
// DROP overflow lines, so it is raised, never used as the clipping tool).
//
// Two bounds are enforced, matching the box's real-world failure modes:
//   - vertical: more lines than inputMaxRows → preview pane (marker + tail)
//   - horizontal: lines longer than the box → hard-clipped to the box edge
//     showing the TAIL (where the cursor sits); wrapping forever downward
//     is exactly what the pane must prevent.
//
// ctrl+e switches to the real textarea (height-capped at 12 rows, its own
// viewport follows the cursor) for full editing of oversized content.
func inpView(m model) string {
	inner := clampInt(m.width-8, 10, m.width-8) // border 2 + padding 2 + scrollbar margin
	limit := m.inputRowLimit()
	val := m.input.Value()
	lines := strings.Split(val, "\n")
	wide := false
	for _, ln := range lines {
		if visWidth(ln) > inner {
			wide = true
			break
		}
	}
	if m.pasteExpand || (len(lines) <= limit && !wide) {
		// normal: the genuine textarea (bounded by its synced height)
		return inputStyle.Width(clampInt(m.width-4, 16, m.width)).Render(m.input.View())
	}
	// clipped preview: marker + last rows, tail-shown, hard horizontal clip.
	// Renders EXACTLY as many rows as the textarea it replaces (its synced
	// height) so the frame accounting in syncViewport stays correct.
	rows := make([]string, 0, limit)
	if m.input.Height() <= 1 {
		// single-row box: no marker fits — show the tail of the line itself
		r := []rune(lines[len(lines)-1])
		if len(r) > inner {
			r = append([]rune("…"), r[len(r)-inner:]...)
		}
		return inputStyle.Width(clampInt(m.width-4, 16, m.width)).Render(string(r))
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf("… %d lines — showing tail · ctrl+e edit · enter sends ALL", len(lines))))
	remaining := m.input.Height() - 1
	start := len(lines) - remaining
	if start < 0 {
		start = 0
	}
	for _, ln := range lines[start:] {
		r := []rune(ln)
		if len(r) > inner {
			r = append([]rune("…"), r[len(r)-inner+1:]...)
		}
		rows = append(rows, string(r))
	}
	return inputStyle.Width(clampInt(m.width-4, 16, m.width)).Render(strings.Join(rows, "\n"))
}

// inputMaxRows caps the visible input pane. Content beyond it stays in the
// buffer (submittable in full) — only the view is clipped.
const inputMaxRows = 10

// inputRowLimit is the effective pane cap for the CURRENT screen: a huge
// input must never push the frame past a small terminal (smoke-tested at
// 40x12), so tiny screens get a proportionally tinier pane.
func (m model) inputRowLimit() int {
	base := inputMaxRows
	if m.pasteExpand {
		base = 12
	}
	screenCap := m.height - 8 // transcript + frame chrome minimums
	if screenCap < 1 {
		screenCap = 1
	}
	if base > screenCap {
		base = screenCap
	}
	return base
}

// syncInputHeight keeps the textarea's visible pane at min(buffer lines,
// row limit) rows. Called after every message, so typing, pasting and
// submits all resize the box correctly; the textarea viewport follows the
// cursor, so the end of the content stays visible when clipped.
func (m *model) syncInputHeight() {
	limit := m.inputRowLimit()
	h := m.input.LineCount()
	if h > limit {
		h = limit
	}
	if h < 1 {
		h = 1
	}
	if m.input.Height() != h {
		m.input.SetHeight(h)
	}
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

	// viewport content/dimensions are synced in Update -> syncViewport;
	// the view here only READS the synced viewport (pure render).

	// overlay-open layout: the menu panel REPLACES the transcript and sits
	// DIRECTLY ABOVE the input box — it never rises over or above the text
	// box (user rule: menus stay down). syncViewport already decided the
	// layout (menuShed) and the panel body budget (menuPAvail): the chat
	// shrinks first so menus get their full height on normal windows.
	if m.over.mode != overlayNone {
		if m.vp.Height >= 3 {
			b.WriteString(vpWithScrollbar(m.vp) + "\n")
		}
		switch m.menuShed {
		case 0:
			b.WriteString(m.menuBlock() + "\n" + inpView(m) + "\n\n" + m.statusBar())
		case 1:
			b.WriteString(m.menuBlock() + "\n" + inpView(m))
		default:
			b.WriteString(m.menuBlock())
		}
		return b.String()
	}

	// transcript viewport (content already synced, follow-lock applied).
	b.WriteString(vpWithScrollbar(m.vp) + "\n")

	// approval modal
	if m.approval != "" {
		mod := warnStyle.Render(" APPROVAL REQUIRED ") + " " + m.approval +
			dimStyle.Render("   [y] apply · [n] skip · [esc] cancel")
		b.WriteString(mod + "\n")
	}
	b.WriteString(inpView(m) + "\n")

	// one clean gap row between the input box and the status band
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

// statusBar is the simplified bottom band: mode + run timer while working.
// Rendered as its own fully black padded strip (separate from the input box)
// with uniform full width. Token counters were removed by request.
func (m model) statusBar() string {
	timer := ""
	if m.status.Spinning {
		timer = " · " + m.status.Elapsed + " · esc stops"
	}
	mode := m.agent.Status().Mode
	bar := fmt.Sprintf(" mode %s%s", mode, timer)
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
			parts = append(parts, planStyle.Render(truncateLines(l.body, vw-4, 12)))
		case "tool":
			if isMCPToolLine(l.body) {
				// external MCP call: amber diamond marker so external side
				// effects are visually distinct from native tools
				parts = append(parts, warnStyle.Render("◆ "+l.body))
			} else {
				parts = append(parts, toolStyle.Render("> "+l.body))
			}
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

// isMCPToolLine reports whether a transcript tool line names an MCP tool:
// tool lines start with the tool name; namespaced names (srv__tool) only
// ever come from MCP routing.
func isMCPToolLine(body string) bool {
	name, _, ok := strings.Cut(body, " ")
	if !ok {
		name = body
	}
	return strings.Contains(name, "__")
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

// sanitizeText strips C0 control characters (except \n) from text entering
// the transcript. BEL (0x07) makes Windows terminals beep mid-reply; other
// control bytes (carriage returns from fetched pages, escapes) corrupt the
// frame. Tabs expand so column math stays honest.
func sanitizeText(s string) string {
	dirty := false
	for _, r := range s {
		if r < 0x20 && r != '\n' {
			dirty = true
			break
		}
	}
	if !dirty {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r >= 0x20:
			b.WriteRune(r)
		}
	}
	return b.String()
}

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

// cleanPlan normalizes reasoning text for the transcript: models pad
// reasoning with leading/trailing blank lines and sometimes emit runs of
// blank lines mid-stream — combined with the blank line renderTranscript
// puts between blocks, that showed as big airy gaps after every tool step.
// Trims the edges, squashes internal blank runs to one, drops trailing
// whitespace per line.
func cleanPlan(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	// squash blank runs
	out := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, ln)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func truncateLines(s string, width, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("... (+%d lines of reasoning)", len(lines)-maxLines))
	}
	for i, ln := range lines {
		if width > 0 && len([]rune(ln)) > width {
			r := []rune(ln)
			lines[i] = string(r[:width]) + "..."
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
