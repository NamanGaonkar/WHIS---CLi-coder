package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"whis/internal/agent"
	"whis/internal/config"
	"whis/internal/mcp"
	"whis/internal/memory"
	"whis/internal/project"
	"whis/internal/provider"
	"whis/internal/session"
)

// normalizePick validates a slug before binding; ollama names pass through.
func normalizePick(a *agent.Agent, slug string, keys map[string]string) (string, error) {
	if !provider.Exists(slug) {
		return "", fmt.Errorf("unknown model %q — pick from the /model menu", slug)
	}
	prov, _, _, err := provider.Get(slug)
	if err != nil {
		return "", err
	}
	if provider.NeedsKey(prov) && keys[prov] == "" {
		return "", fmt.Errorf("no API key for %s — /model → pick provider → add key", prov)
	}
	_ = a
	return slug, nil
}

// --- spinner & clock helpers ---

const statusInterval = 120 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	spinnerMu  sync.Mutex
	spinnerIdx int
)

func nextSpinner() string {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	f := spinnerFrames[spinnerIdx%len(spinnerFrames)]
	spinnerIdx++
	return f
}

// fmtDur is defined in tui.go; runtime keeps only the spinner.

// --- concrete adapter: *agent.Agent -> AgentAPI ---

// Adapter binds a *agent.Agent plus keys to the AgentAPI surface.
type Adapter struct {
	A      *agent.Agent
	keys   map[string]string
	Cfg    *config.Config
	cancel context.CancelFunc

	mu sync.Mutex
}

// Keys exposes the current resolved key set (for menu readiness hints).
func (ad *Adapter) Keys() map[string]string { return ad.keys }

// NewAdapter builds the TUI adapter.
func NewAdapter(a *agent.Agent, keys map[string]string) *Adapter {
	return &Adapter{A: a, keys: keys}
}

// Cancel aborts any in-flight agent loop.
func (ad *Adapter) Cancel() {
	if ad.cancel != nil {
		ad.cancel()
	}
}

// Interrupt stops the running agent loop on user request (esc).
func (ad *Adapter) Interrupt() {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	ad.Cancel()
}

// Run starts a turn; it emits TUIEvents from the agent's event channel.
func (ad *Adapter) Run(prompt string) (<-chan TUIEvent, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ad.cancel = cancel
	ch, err := ad.A.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	out := make(chan TUIEvent, 64)
	go func() {
		defer close(out)
		for ev := range ch {
			te := TUIEvent{
				Type: ev.Type, Text: ev.Text,
				ToolName: ev.ToolName, ToolArgs: ev.ToolArgs,
				ToolOutput: ev.ToolOutput, ToolOK: ev.ToolOK,
				Turn: ev.Turn, Cost: ev.Cost, DurationMS: ev.DurationMS,
			}
			if ev.Usage != nil {
				te.In, te.Cached, te.Out = ev.Usage.In, ev.Usage.Cached, ev.Usage.Out
			}
			out <- te
		}
	}()
	return out, nil
}

// HandleSlash executes slash commands, returning a UI message.
// /task runs synchronously and may take a while; the TUI shows the last
// spinner frame until it returns.
func (ad *Adapter) HandleSlash(cmd string) (string, error) {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", nil
	}
	switch fields[0] {
	case "/model":
		if len(fields) < 2 {
			return "usage: /model <slug> — or just type / and pick from the menu", nil
		}
		return ad.PickModel(fields[1])
	case "/task":
		desc := strings.TrimSpace(strings.TrimPrefix(cmd, "/task"))
		if desc == "" {
			return "usage: /task \"run the test suite and fix linter errors\"", nil
		}
		if !ad.A.Bound() {
			return "", fmt.Errorf("no model selected — /model first")
		}
		final, err := agent.Task(ad.A, context.Background(), desc)
		if err != nil {
			return "", err
		}
		return "task result (isolated bubble):\n" + final, nil
	case "/resume":
		if len(fields) < 2 {
			return "usage: /resume <session-id>", nil
		}
		s, err := session.Load(fields[1])
		if err != nil {
			return "", err
		}
		ad.A.Sess = s
		return "", nil
	case "/undo":
		return agent.Undo(ad.A), nil
	case "/init":
		md, err := project.GenerateWHISMD(ad.A.Root)
		if err != nil {
			return "", err
		}
		return "wrote WHIS.md (" + fmt.Sprint(len(md)) + " bytes)", nil
	case "/memory":
		st := memory.Load()
		if len(fields) >= 2 && fields[1] == "forget" && len(fields) >= 3 {
			n := st.Forget(0, strings.Join(fields[2:], " "))
			if n == 0 {
				return "no memory matched", nil
			}
			ad.A.RebuildSystemFromMemory()
			return fmt.Sprintf("forgot %d memor%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]), nil
		}
		if st.Count() == 0 {
			return "memory is empty - tell whis things like \"remember that I prefer tabs\"", nil
		}
		return "persistent memories (survive all sessions):\n" + st.Digest(60000), nil
	case "/sessions":
		return strings.Join(session.List(), "\n"), nil
	case "/new":
		if !ad.A.Bound() {
			return "", fmt.Errorf("no model selected — /model first")
		}
		return ad.A.ResetSession(), nil
	case "/compress":
		if !ad.A.Bound() {
			return "", fmt.Errorf("no model selected — /model first")
		}
		if n := ad.A.CompactNow(); n > 0 {
			return fmt.Sprintf("compressed: squashed %d oversized tool log(s) — context freed", n), nil
		}
		return "nothing to compress — context is already lean", nil
	case "/usage":
		if !ad.A.Bound() {
			return "", fmt.Errorf("no model selected — /model first")
		}
		in, cached, out, cost := ad.A.Sess.Totals()
		return fmt.Sprintf("tokens in=%d (cached %d) out=%d · cost so far $%.4f · context ≈%s / %s", in, cached, out, cost,
			commify(contextUsed(ad.A.Sess)), commify(contextLimit(ad.A.Wire))), nil
	case "/help":
		return `commands:
  /model <slug>   hot-swap model — or just press / and pick from the menu
  /task <desc>    run an isolated subagent task
  /undo           roll back to the last pre-edit snapshot
  /new            fresh conversation (old one saved)
  /retry          re-run the last prompt from scratch
  /compress       squash old tool logs to free context now
  /usage          token + cost + context usage for this session
  /copy [what]    copy last reply to clipboard; /copy prompt or /copy output
                  for other targets (ctrl+y = quick reply copy)
  /init           (re)generate WHIS.md project guide
  /sessions       list saved sessions
  /mcp            MCP servers: connection status + tool list
  /memory         show remembered facts (survive all sessions)
  /memory forget <words>   delete matching memories
  /themes         switch the color palette (also: ? or ctrl+t)
  /help           this help
keys:
  /       command menu (arrow keys or mouse click to pick)
  ?       theme picker
  Ctrl+T  theme picker        Ctrl+P  plan pane toggle
  Ctrl+O  expand code blocks  Ctrl+Y  copy last reply to clipboard
  Ctrl+C/D quit
  y/n     approve diffs       esc     back one menu (main screen at root)
memory:
  just say "remember that ..." — whis stores it permanently and knows it
  in every future chat (memory_save / memory_recall / memory_forget)
provider keys:
  / -> provider -> "edit / re-enter a provider key" replaces a saved key`, nil
	}
	return "", fmt.Errorf("unknown command %s (try /help)", fields[0])
}

// ResumedTranscript returns the restored conversation as TUI transcript
// lines so a resumed session visibly shows its full history.
func (ad *Adapter) ResumedTranscript() []TUILine {
	var out []TUILine
	if ad.A == nil || ad.A.Sess == nil {
		return out
	}
	for _, m := range ad.A.Sess.Messages {
		switch m.Role {
		case "user":
			out = append(out, TUILine{Kind: "user", Body: m.Content})
		case "assistant":
			// reasoning blocks are NOT replayed: a resumed session would
			// render a wall of boxed plan panes ("history tabs") in chat.
			if m.Content != "" {
				out = append(out, TUILine{Kind: "md", Body: m.Content})
			}
		case "tool":
			body := m.Content
			if len(body) > 400 {
				body = body[:400] + "\n… (truncated)"
			}
			out = append(out, TUILine{Kind: "toolout", Body: body})
		}
	}
	return out
}

// ResumeInfo returns a one-line summary of the active session.
func (ad *Adapter) ResumeInfo() string {
	if ad.A == nil || ad.A.Sess == nil {
		return ""
	}
	s := ad.A.Sess
	model := s.Model
	if model == "" {
		model = "model not set"
	}
	return fmt.Sprintf("resumed %s · %d messages · %s", s.ID, len(s.Messages), model)
} // SetMode switches the agent work mode (plan | ask | auto).
func (ad *Adapter) SetMode(mode string) error {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	return ad.A.SetMode(mode)
}

// Status reports live header/bar data.
func (ad *Adapter) Status() Status {
	in, cached, out, cost := ad.A.Sess.Totals()
	st := Status{
		Model:  ad.A.Slug,
		Mode:   ad.A.Mode,
		Branch: gitBranch(ad.A.Root),
		In:     in, Cached: cached, Out: out,
		Cost:    cost,
		CtxUsed: contextUsed(ad.A.Sess),
	}
	if ad.A.Prov != nil {
		st.Provider = ad.A.Prov.Name()
		st.CtxLimit = contextLimit(ad.A.Wire)
	}
	return st
}                                     // Workspace returns the current project root.
func (ad *Adapter) Workspace() string { return ad.A.Root }

// MCPStatus renders the /mcp panel body: one line per configured server
// (● connected with tool count, ○ disabled, × failed), then its tools.
func (ad *Adapter) MCPStatus() string {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	cfg := mcp.LoadConfig()
	if len(cfg.MCPServers) == 0 {
		return "no MCP servers configured.\nadd one with:  whis mcp add filesystem\nconfig file:  " + mcp.ConfigPath()
	}
	var b strings.Builder
	for name, sc := range cfg.MCPServers {
		switch {
		case sc.Disabled:
			fmt.Fprintf(&b, "○ %s (disabled)\n", name)
			continue
		case ad.A.MCP == nil || !ad.A.MCP.HasServer(name):
			fmt.Fprintf(&b, "× %s (failed to connect)\n", name)
			continue
		}
		n := ad.A.MCP.ToolCount(name)
		fmt.Fprintf(&b, "● %s (connected, %d tools)\n", name, n)
		for _, ns := range ad.A.MCP.ServerTools(name) {
			fmt.Fprintf(&b, "    %s\n", ns)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Ready reports whether a model is bound and prompts can run.
func (ad *Adapter) Ready() bool { return ad.A.Bound() }

// PickModel binds a model via the shared agent picker logic.
func (ad *Adapter) PickModel(slug string) (string, error) {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	slug2, err := normalizePick(ad.A, slug, ad.keys)
	if err != nil {
		return "", err
	}
	if err := ad.A.SwapModel(slug2, ad.keys); err != nil {
		return "", err
	}
	ad.Cfg.Model = slug2
	_ = ad.Cfg.Save()
	return "model → " + slug2 + " (state retained)", nil
}

// SaveKey stores a provider key into config and the live key set.
// Clipboard paste often drags in trailing spaces/newlines (or zero-width
// chars) which make the provider reject a perfectly good key — sanitize
// before storing.
func (ad *Adapter) SaveKey(prov, key string) {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	key = strings.TrimSpace(key)
	key = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F || (r >= 0x200B && r <= 0x200F) || r == 0xFEFF {
			return -1
		}
		return r
	}, key)
	switch prov {
	case "deepseek":
		ad.Cfg.Keys.DeepSeek = key
	case "anthropic":
		ad.Cfg.Keys.Anthropic = key
	case "openai":
		ad.Cfg.Keys.OpenAI = key
	case "openrouter":
		ad.Cfg.Keys.OpenRouter = key
	case "ollama-cloud":
		ad.Cfg.Keys.OllamaCloud = key
	case "gemini":
		ad.Cfg.Keys.Gemini = key
	case "xai":
		ad.Cfg.Keys.XAI = key
	case "mistral":
		ad.Cfg.Keys.Mistral = key
	case "moonshot":
		ad.Cfg.Keys.Moonshot = key
	case "qwen":
		ad.Cfg.Keys.Qwen = key
	case "zai":
		ad.Cfg.Keys.Zai = key
	case "minimax":
		ad.Cfg.Keys.MiniMax = key
	case "groq":
		ad.Cfg.Keys.Groq = key
	}
	_ = ad.Cfg.Save()
	// refresh the live map
	refresh := provider.KeysMap(ad.Cfg.Keys.Anthropic, ad.Cfg.Keys.OpenAI,
		ad.Cfg.Keys.DeepSeek, ad.Cfg.Keys.OpenRouter, ad.Cfg.Keys.OllamaCloud)
	refresh["gemini"], refresh["xai"], refresh["mistral"], refresh["moonshot"] = ad.Cfg.Keys.Gemini, ad.Cfg.Keys.XAI, ad.Cfg.Keys.Mistral, ad.Cfg.Keys.Moonshot
	refresh["qwen"], refresh["zai"], refresh["minimax"], refresh["groq"] = ad.Cfg.Keys.Qwen, ad.Cfg.Keys.Zai, ad.Cfg.Keys.MiniMax, ad.Cfg.Keys.Groq
	for k, v := range refresh {
		ad.keys[k] = v
	}
}

// RetryPrompt re-runs the last user prompt (Hermes-inspired /retry): the
// transcript rewinds to just before that message and the loop starts over.
func (ad *Adapter) RetryPrompt() (string, bool) {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	return ad.A.RetryPrompt()
}

// LastAssistantText returns the newest NON-empty assistant message body
// from the session store (raw markdown, no ANSI) — used by /copy and
// ctrl+y so the clipboard gets the SOURCE of the reply, not its rendered
// transcript form. Empty assistant records are skipped: every tool-using
// turn appends one (the model spoke via tool calls, not text), so the
// naive "last message" used to return "" right after a web_search turn
// and copy grabbed the wrong thing.
func (ad *Adapter) LastAssistantText() string {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if ad.A == nil || ad.A.Sess == nil {
		return ""
	}
	msgs := ad.A.Sess.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// LastUserText returns the newest user prompt from the session store
// (raw source) — the /copy prompt target.
func (ad *Adapter) LastUserText() string {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if ad.A == nil || ad.A.Sess == nil {
		return ""
	}
	msgs := ad.A.Sess.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// contextUsed estimates tokens currently in the conversation.
func contextUsed(s *session.Session) int {
	total := 0
	for _, m := range s.Messages {
		total += len(m.Content)
	}
	return total / 4
}

// contextLimit maps a wire model to its approximate context window.
func contextLimit(wire string) int {
	switch {
	case strings.Contains(wire, "deepseek"):
		return 128 * 1024
	case strings.Contains(wire, "claude"):
		return 200 * 1024
	case strings.Contains(wire, "gpt"):
		return 128 * 1024
	default:
		return 32 * 1024 // local models, conservative
	}
}

func gitBranch(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return "—"
	}
	h := strings.TrimSpace(string(b))
	if strings.HasPrefix(h, "ref: refs/heads/") {
		return strings.TrimPrefix(h, "ref: refs/heads/")
	}
	return "detached"
}
