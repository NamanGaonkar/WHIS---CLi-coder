// Package agent implements the WHIS multi-turn agent loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"whis/internal/mcp"
	"whis/internal/project"
	"whis/internal/provider"
	"whis/internal/session"
	"whis/internal/tool"
)

// Events emitted by the agent loop (consumed by the TUI or headless mode).
type Event struct {
	Type       string // "reasoning" | "text" | "tool_start" | "tool_end" | "approval" | "usage" | "turn_done" | "error"
	Text       string
	ToolName   string
	ToolArgs   string
	ToolOutput string
	ToolOK     bool
	Usage      *provider.Usage
	Turn       int // 1-based turn number within this run
	DurationMS int64
	Cost       float64
	// Approve is set on "approval" events; call with the decision.
	Approve func(bool)
}

// Agent owns providers, tools and session state for one conversation.
type Agent struct {
	Root        string
	AutoApprove bool
	Mode        string // "plan" | "ask" | "auto"

	Slug    string // e.g. "deepseek-flash"
	Wire    string
	Prov    provider.Provider
	Prices  provider.Prices
	Sess    *session.Session
	Tools   *tool.Env
	System  string
	MaxTurn int
	// Mem is the persistent cross-session memory store (nil when disabled).
	Mem memDigestSource

	// askApproval is re-armed each turn by the loop; it blocks on the TUI.
	askApproval func(string) bool

	// lastToolResult caches the most recent result per tool-call signature
	// (name + args) so an identical repeat never re-executes and the model
	// gets a STOP instruction instead of looping forever.
	lastToolResult map[string]tool.Result

	// titleSet marks that the session already has a human title (first
	// prompt of the run); later prompts never overwrite it.
	titleSet bool

	// pendingNudge is a TRANSIENT user-side instruction appended to the next
	// buildMessages call (not persisted in the session). Used to jolt small
	// local models that answer with an empty turn (no text, no tool call) —
	// the classic "stuck at done, never replies" failure of 1-4B models.
	pendingNudge string

	// MCP hosts external tool servers (nil-safe: zero config = plain whis).
	// The agent routes namespaced tool calls (srv__tool) to it and appends
	// its tools to the manifest.
	MCP *mcp.Manager
}

// deriveTitle turns the first user prompt into a short session title
// (LOCAL string surgery only — no API call, no token burn). Long prompts
// collapse to their first meaningful line, trimmed to ~48 chars.
func deriveTitle(prompt string) string {
	t := strings.TrimSpace(prompt)
	// first non-empty line wins
	for _, ln := range strings.Split(t, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			t = s
			break
		}
	}
	t = strings.Join(strings.Fields(t), " ") // collapse inner whitespace
	if t == "" {
		return ""
	}
	const max = 48
	r := []rune(t)
	if len(r) > max {
		t = string(r[:max-1]) + "…"
	}
	return t
}

// memDigestSource is what rebuildSystem needs from the memory store
// (satisfied by *memory.Store via the adapter in memory.go).
type memDigestSource interface {
	Digest(maxChars int) string
	Count() int
}

// Work modes.
const (
	ModePlan = "plan" // read-only: map the codebase, produce a plan
	ModeAsk  = "ask"  // act freely; approve only sensitive/destructive actions
	ModeAuto = "auto" // full autonomy: nothing prompts (still blocks dangerous)
)

// Modes returns the selectable work modes with UI descriptions.
func Modes() [](struct{ Name, Desc string }) {
	return [](struct{ Name, Desc string }){
		{ModePlan, "read-only · analyze & plan, no writes, no commands"},
		{ModeAsk, "agent acts freely · popup only for destructive stuff"},
		{ModeAuto, "no popups at all · even destructive commands run"},
	}
}

// ValidMode reports whether m is a known mode.
func ValidMode(m string) bool {
	return m == ModePlan || m == ModeAsk || m == ModeAuto
} // New wires up an Agent for a workspace with an immediately bound model.
func New(root, slug string, keys map[string]string, auto bool) (*Agent, error) {
	a := NewUnbound(root, auto)
	if err := a.SwapModel(slug, keys); err != nil {
		return nil, err
	}
	return a, nil
}

// NewUnbound boots an Agent without a model. The TUI starts instantly and a
// model is bound lazily via SwapModel (boot never errors on model issues).
func NewUnbound(root string, auto bool) *Agent {
	a := &Agent{
		Root: root, AutoApprove: auto, Mode: ModeAsk,
		Sess:           session.NewForRoot("", root),
		Tools:          tool.NewEnv(root),
		MaxTurn:        120,
		lastToolResult: map[string]tool.Result{},
	}
	a.Tools.RiskBased = !auto
	a.Tools.AutoApprove = auto
	a.installApprovals()
	a.Tools.OnSnapshot = a.snapshot
	a.attachMemory()
	return a
}

// AttachMCP connects the manager (spawn servers, list tools) and appends a
// short note to the system prompt. With no config, nothing is added to the
// prompt and nothing spawns (zero overhead). Returns boot warnings — the
// agent itself never fails because of MCP problems.
func (a *Agent) AttachMCP(m *mcp.Manager) []string {
	if m == nil {
		return nil
	}
	cfg := mcp.LoadConfig()
	if len(cfg.MCPServers) == 0 {
		return nil // zero overhead: no config, no spawn, no prompt bloat
	}
	warns := m.Connect(context.Background(), cfg)
	a.MCP = m
	if m.Connected() {
		a.System += "\n\n--- MCP TOOLS (external servers; namespaced srv__tool) ---\n" +
			strings.Join(m.Stats(), ", ") +
			"\nCall them like native tools. Their output is capped; they may touch external systems."
	} else {
		a.System += "\nNo MCP servers connected (config had entries but none could start)."
	}
	return warns
}

// Bound reports whether a model is attached.
func (a *Agent) Bound() bool { return a.Prov != nil }

// ResetSession starts a fresh conversation in place (Hermes-inspired /new):
// the current session is saved, a blank one takes over. Model, keys, memory
// and the undo trail are untouched.
func (a *Agent) ResetSession() string {
	a.Sess.Save()
	a.Sess = session.NewForRoot(a.Slug, a.Root)
	a.titleSet = false
	a.pendingNudge = ""
	return "fresh session started — the old one is saved (whis -l lists it)"
}

// RetryPrompt rewinds the session to just before the last user message
// (dropping any assistant reply after it) and returns that prompt for a
// fresh attempt (Hermes-inspired /retry). Each call retries one step back.
func (a *Agent) RetryPrompt() (string, bool) {
	msgs := a.Sess.Messages
	i := len(msgs) - 1
	if i >= 0 && msgs[i].Role == "assistant" {
		i--
	}
	if i < 0 || msgs[i].Role != "user" || strings.TrimSpace(msgs[i].Content) == "" {
		return "", false
	}
	p := msgs[i].Content
	a.Sess.Messages = msgs[:i]
	return p, true
}

// CompactNow runs the context compactor immediately (Hermes-inspired
// /compress) and reports how many oversized tool logs were squashed.
func (a *Agent) CompactNow() int { return a.maybeCompact() }

// installApprovals wires the tool env's AskApproval to emit approval events
// on the active event stream. It must be re-armed by the loop for each turn.
func (a *Agent) installApprovals() {
	if a.AutoApprove {
		a.Tools.AskApproval = nil
		return
	}
	a.Tools.AskApproval = func(action string) bool {
		if a.askApproval == nil {
			return false // no active stream: deny rather than deadlock
		}
		return a.askApproval(action)
	}
}

// SwapModel hot-swaps provider mid-session, retaining history + index.
func (a *Agent) SwapModel(slug string, keys map[string]string) error {
	prov, wire, prices, err := provider.Resolve(slug, keys)
	if err != nil {
		return err
	}
	a.Slug, a.Wire, a.Prov, a.Prices = slug, wire, prov, prices
	a.rebuildSystem()
	return nil
}

func (a *Agent) rebuildSystem() {
	names := make([]string, 0, 6)
	for _, d := range tool.Manifest() {
		names = append(names, d.Name)
	}
	a.System = project.SystemPrompt(a.Root, names)
	a.System += "\n\n" + a.modeDirective()
	a.System += "\n\n" + completionDirective()
	// active task list: the agent's own working plan, refreshed whenever it
	// calls task_tracker write. Kept separate from the cached static block.
	if tl := (tool.TaskTracker{Root: a.Root}).Load(); strings.TrimSpace(tl) != "" {
		a.System += "\n\n--- ACTIVE TASK LIST (your plan; update via task_tracker write) ---\n" + tl
	}
	// persistent memory: remembered facts ride the cached system prompt so
	// every session starts already knowing them.
	if a.Mem != nil {
		if d := a.Mem.Digest(4000); d != "" {
			a.System += "\n\n--- PERSISTENT MEMORY (facts the user asked you to remember; " +
				"use memory_forget to delete one) ---\n" + d
		}
	}
	// WHIS.md project guide rides the static system block (cache-friendly).
	if b, err := os.ReadFile(filepath.Join(a.Root, "WHIS.md")); err == nil && len(b) > 0 {
		a.System += "\n\n--- WHIS.md (project guide) ---\n" + string(b)
	}
}

// completionDirective teaches the model how to finish: write files to disk
// (never dump code in chat), verify, then emit the DONE footer.
func completionDirective() string {
	return `COMPLETION PROTOCOL (strict):
1. NEVER print code in the chat. Write every file with apply_patch. The chat
   is for short status only. If you already know the content, create the file
   immediately; do not paste it into the reply first.
2. After the last action, run the project's verify command via run_command
   when one exists (build/test). Fix failures and re-verify.
3. End EVERY final answer with exactly this footer on its own line:
   DONE: <one-line summary of what changed>
   Example: DONE: added calculator app (index.html, style.css, script.js), verified in browser
4. Keep final replies under 10 lines: what you did, where, how to run it.`
}
func (a *Agent) modeDirective() string {
	switch a.Mode {
	case ModePlan:
		return "MODE: PLAN. Read-only research phase. Only read/search/list tools are " +
			"available. Explore the codebase, then present a concrete step-by-step " +
			"implementation plan (files to touch, edits, commands to verify). Do not " +
			"attempt writes; they are disabled and will be denied."
	case ModeAuto:
		return "MODE: AUTO. The user pre-approved ALL file writes and shell commands. " +
			"Act fully autonomously: create/modify files with apply_patch, run build " +
			"and test commands via run_command, and iterate until it works. No " +
			"approval prompts will appear."
	default:
		return "MODE: ASK. You may write files and run commands freely WITHOUT " +
			"asking permission for normal work: creating files, editing code, " +
			"running builds/tests. Only destructive actions (rm -rf, git push, " +
			"system changes) trigger a user prompt. Do not ask, just do the work."
	}
}

// SetMode switches the work mode and re-derives approvals + prompt.
func (a *Agent) SetMode(mode string) error {
	if !ValidMode(mode) {
		return fmt.Errorf("unknown mode %q (plan | ask | auto)", mode)
	}
	a.Mode = mode
	a.AutoApprove = mode == ModeAuto
	a.Tools.AutoApprove = a.AutoApprove
	a.Tools.RiskBased = mode == ModeAsk // ask = act freely, prompt only risky
	a.rebuildSystem()
	return nil
}

// allowedTools filters the manifest for the active mode (plan = read-only).
// MCP tools are appended namespaced; they follow the same plan-mode rule
// (read-only unknown => denied in plan mode, like run_command).
func (a *Agent) allowedTools() []provider.Tool {
	readOnly := map[string]bool{
		"locate_symbol": true, "read_range": true,
		"search_codebase": true, "list_tree": true, "web_fetch": true, "web_search": true,
		"browser": true,
	}
	var out []provider.Tool
	for _, d := range tool.Manifest() {
		if a.Mode == ModePlan && !readOnly[d.Name] {
			continue
		}
		out = append(out, provider.Tool{Name: d.Name, Description: d.Description, Schema: d.Schema})
	}
	if a.MCP != nil && a.MCP.Connected() {
		for _, ns := range a.MCP.Namespaced() {
			raw, err := a.MCP.ManifestJSON(ns)
			if err != nil {
				continue
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				continue
			}
			desc, _ := schema["description"].(string)
			if a.Mode == ModePlan {
				continue // MCP side effects are unknown: never allowed in plan
			}
			out = append(out, provider.Tool{Name: ns, Description: desc + " (MCP tool)", Schema: schema})
		}
	}
	return out
}

// snapshot writes a shadow snapshot before mutations (/undo source).
func (a *Agent) snapshot() error { return snapshotWorkspace(a.Root) }

// Run executes one user prompt through the full agent loop.
func (a *Agent) Run(ctx context.Context, prompt string) (<-chan Event, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("empty prompt")
	}
	if !a.Bound() {
		return nil, fmt.Errorf("no model selected — pick one with /model")
	}
	a.Sess.Append(session.Msg{Role: "user", Content: prompt})
	// auto-title: name the session after its first real prompt so the
	// /sessions list shows WHAT each chat was about (local only, free).
	if !a.titleSet {
		if t := deriveTitle(prompt); t != "" && (a.Sess.Title == "" || a.Sess.Title == a.Sess.ID) {
			a.Sess.SetTitle(t)
		}
		a.titleSet = true
	}
	out := make(chan Event, 256) // token-level streams from slow local models must not block the loop
	go a.loop(ctx, out, prompt)
	return out, nil
}

func (a *Agent) loop(ctx context.Context, out chan<- Event, prompt string) {
	defer close(out)
	defer func() {
		if r := recover(); r != nil {
			emit(out, Event{Type: "error", Text: fmt.Sprintf("internal error: %v", r)})
		}
	}()
	start := time.Now()
	nudges := 0

	for turn := 0; turn < a.MaxTurn; turn++ {
		msgs := a.buildMessages()
		stream, err := a.Prov.Stream(ctx, a.Wire, msgs, a.allowedTools())
		if err != nil {
			// cancelled mid-run is not an error worth showing
			if ctx.Err() != nil {
				emit(out, Event{Type: "turn_done", Text: "", Turn: turn + 1, DurationMS: time.Since(start).Milliseconds()})
				return
			}
			emit(out, Event{Type: "error", Text: err.Error()})
			return
		}
		// announce the phase so the UI can show "turn n/max"
		emit(out, Event{Type: "notice", Text: fmt.Sprintf("thinking · turn %d/%d", turn+1, a.MaxTurn), Turn: turn + 1})

		var text strings.Builder
		var reason strings.Builder
		var calls []provider.ToolCall

		for {
			d, err := stream.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				emit(out, Event{Type: "error", Text: err.Error()})
				_ = stream.Close()
				return
			}
			if d.Reasoning != "" {
				reason.WriteString(d.Reasoning)
				emit(out, Event{Type: "reasoning", Text: d.Reasoning})
			}
			if d.Text != "" {
				text.WriteString(d.Text)
				emit(out, Event{Type: "text", Text: d.Text})
			}
			if d.Call != nil {
				calls = append(calls, *d.Call)
			}
			if d.Usage != nil {
				a.recordUsage(*d.Usage, time.Since(start))
				// live telemetry for the status bar (Cost included so the TUI
				// can show real spend, not just token counts)
				u := *d.Usage
				emit(out, Event{Type: "usage", Usage: &u, Turn: turn + 1, DurationMS: time.Since(start).Milliseconds(), Cost: a.Prices.Cost(u)})
			}
			if ctx.Err() != nil {
				// user interrupted mid-stream
				_ = stream.Close()
				emit(out, Event{Type: "notice", Text: "interrupted"})
				emit(out, Event{Type: "turn_done", Text: "", Turn: turn + 1, DurationMS: time.Since(start).Milliseconds()})
				return
			}
		}
		_ = stream.Close()

		// persist assistant message
		am := session.Msg{Role: "assistant", Content: text.String(), Reasoning: reason.String()}
		for _, c := range calls {
			am.ToolCalls = append(am.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Name, Args: string(c.Args)})
		}
		a.Sess.Append(am)

		if reason.String() != "" {
			emit(out, Event{Type: "plan", Text: reason.String()})
		}

		// no tool calls => final answer — UNLESS the reply is EMPTY (small
		// local models do this when the tool list overwhelms them). Nudge
		// up to twice, then fail loudly instead of a silent "done" stall.
		if len(calls) == 0 {
			if strings.TrimSpace(text.String()) == "" && strings.TrimSpace(reason.String()) == "" {
				if nudges < 2 {
					nudges++
					_ = stream.Close()
					a.pendingNudge = "[whis] your previous reply was EMPTY (no text, no tool call). " +
						"Respond now: either call one of the provided tools to act, or write your answer " +
						"to the user directly. Never return a blank message."
					emit(out, Event{Type: "notice", Text: fmt.Sprintf("empty reply from model — nudging (%d/2)", nudges)})
					continue
				}
				emit(out, Event{Type: "error", Text: "model returned three empty replies in a row. Small local models " +
					"(1B-4B) often choke on large tool lists — try a bigger model (/model) or fewer tools."})
				emit(out, Event{Type: "turn_done", Text: "", Turn: turn + 1, DurationMS: time.Since(start).Milliseconds()})
				return
			}
			emit(out, Event{Type: "turn_done", Text: text.String(), Turn: turn + 1, DurationMS: time.Since(start).Milliseconds()})
			return
		}

		// execute tools; approval modal rides the stream for this turn
		a.askApproval = func(action string) bool {
			if a.AutoApprove {
				return true
			}
			ch := make(chan bool, 1)
			out <- Event{Type: "approval", Text: action, Approve: func(ok bool) { ch <- ok }}
			return <-ch
		}
		for _, c := range calls {
			emit(out, Event{Type: "tool_start", ToolName: c.Name, ToolArgs: string(c.Args)})
			// plan mode hard-deny for mutating tools (model may still attempt)
			if a.Mode == ModePlan && (c.Name == "apply_patch" || c.Name == "multi_edit" || c.Name == "run_command") {
				emit(out, Event{Type: "tool_end", ToolName: c.Name,
					ToolOutput: "denied: plan mode is read-only — switch to ask/auto mode to act", ToolOK: false})
				a.Sess.Append(session.Msg{Role: "tool", Content: "denied: plan mode is read-only", ToolCallID: c.ID})
				continue
			}
			var res tool.Result
			if a.MCP != nil && a.MCP.IsMCP(c.Name) {
				// external MCP tool: through the SAME approval gate as native
				// tools (side effects are unknown => treated as sensitive),
				// then routed to the owning server with output caps applied.
				if !a.askApproval("run MCP tool: " + c.Name) {
					res = tool.Result{Output: "user declined MCP tool call."}
				} else {
					txt, ok := a.MCP.CallTool(ctx, c.Name, c.Args)
					res = tool.Result{OK: ok, Output: txt}
				}
			} else {
				res = a.Tools.Execute(c.Name, c.Args)
			}
			// anti-loop: identical repeating tool calls get a deterministic
			// cache of the previous result (free) plus a STOP instruction.
			sig := c.Name + "|" + string(c.Args)
			if prev, seen := a.lastToolResult[sig]; seen {
				res = tool.Result{OK: prev.OK, Output: prev.Output +
					"\n[stop] this exact call already ran with this result. Do NOT repeat it: " +
					"use what you have, try a different tool/arguments, or write the final answer now."}
			} else {
				a.lastToolResult[sig] = res
			}
			emit(out, Event{Type: "tool_end", ToolName: c.Name, ToolOutput: res.Output, ToolOK: res.OK})
			a.Sess.Append(session.Msg{Role: "tool", Content: res.Output, ToolCallID: c.ID})
		}
		if n := a.maybeCompact(); n > 0 {
			emit(out, Event{Type: "notice", Text: fmt.Sprintf("compaction: squashed %d tool logs into diagnostic vectors", n)})
		}
	}
	emit(out, Event{Type: "error", Text: fmt.Sprintf("stopped: agent used all %d tool turns without a final answer — try breaking the task into smaller steps (/task helps too)", a.MaxTurn)})
}

// buildMessages converts the session into provider messages.
func (a *Agent) buildMessages() []provider.Message {
	msgs := []provider.Message{{Role: "system", Content: a.System, Cacheable: true}}
	for _, m := range a.Sess.Messages {
		pm := provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Reasoning: m.Reasoning}
		for _, tc := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: tc.ID, Name: tc.Name, Args: json.RawMessage(tc.Args)})
		}
		msgs = append(msgs, pm)
	}
	if a.pendingNudge != "" {
		msgs = append(msgs, provider.Message{Role: "user", Content: a.pendingNudge})
		a.pendingNudge = "" // transient: one-shot
	}
	return msgs
}

// apiTools is unused; the loop uses a.allowedTools() for mode filtering.

// maybeCompact squashes stale tool logs into 2-line diagnostic vectors once
// the estimated context crosses 60% of the active window.
func (a *Agent) maybeCompact() int {
	// rough token estimate: chars/4 over all messages
	total := 0
	for _, m := range a.Sess.Messages {
		total += len(m.Content)
	}
	est := total / 4
	limit := 64 * 1024 // conservative default window (fits 128k context comfortably)
	switch {
	case strings.Contains(a.Wire, "deepseek"):
		limit = 100 * 1024
	case strings.Contains(a.Wire, "claude"):
		limit = 150 * 1024
	}
	if est < limit*60/100 {
		return 0
	}
	compacted := 0
	for i := range a.Sess.Messages {
		if a.Sess.Messages[i].Role == "tool" && len(a.Sess.Messages[i].Content) > 300 {
			a.Sess.Messages[i].Content = summarize(a.Sess.Messages[i].Content)
			compacted++
		}
	}
	if compacted > 0 {
		a.Sess.Save()
	}
	return compacted
}

func summarize(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	head := lines[0]
	if len(head) > 160 {
		head = head[:160]
	}
	tail := ""
	if len(lines) > 1 {
		t := lines[len(lines)-1]
		if len(t) > 160 {
			t = t[:160]
		}
		tail = " … " + t
	}
	return fmt.Sprintf("[compacted tool output; %d lines] %s%s", len(lines), head, tail)
}

func emit(out chan<- Event, e Event) {
	out <- e
}

// recordUsage adds telemetry to the session.
func (a *Agent) recordUsage(u provider.Usage, d time.Duration) {
	cost := a.Prices.Cost(u)
	a.Sess.AddTurn(session.Turn{Model: a.Slug, In: u.In, Cached: u.Cached, Out: u.Out, Cost: cost, DurationMS: d.Milliseconds(), At: time.Now()})
}
