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

	"whis/internal/project"
	"whis/internal/provider"
	"whis/internal/session"
	"whis/internal/tool"
)

// Events emitted by the agent loop (consumed by the TUI or headless mode).
type Event struct {
	Type       string // "reasoning" | "text" | "tool_start" | "tool_end" | "approval" | "turn_done" | "error"
	Text       string
	ToolName   string
	ToolArgs   string
	ToolOutput string
	ToolOK     bool
	Usage      *provider.Usage
	DurationMS int64
	Cost       float64
	// Approve is set on "approval" events; call with the decision.
	Approve func(bool)
}

// Agent owns providers, tools and session state for one conversation.
type Agent struct {
	Root        string
	AutoApprove bool

	Slug    string // e.g. "deepseek-flash"
	Wire    string
	Prov    provider.Provider
	Prices  provider.Prices
	Sess    *session.Session
	Tools   *tool.Env
	System  string
	MaxTurn int

	// askApproval is re-armed each turn by the loop; it blocks on the TUI.
	askApproval func(string) bool
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
		Root: root, AutoApprove: auto,
		Sess:    session.NewForRoot("", root),
		Tools:   tool.NewEnv(root),
		MaxTurn: 12,
	}
	a.Tools.AutoApprove = auto
	a.installApprovals()
	a.Tools.OnSnapshot = a.snapshot
	a.rebuildSystem()
	return a
}

// Bound reports whether a model is attached.
func (a *Agent) Bound() bool { return a.Prov != nil }

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
	// WHIS.md project guide rides the static system block (cache-friendly).
	if b, err := os.ReadFile(filepath.Join(a.Root, "WHIS.md")); err == nil && len(b) > 0 {
		a.System += "\n\n--- WHIS.md (project guide) ---\n" + string(b)
	}
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
	out := make(chan Event, 64)
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

	for turn := 0; turn < a.MaxTurn; turn++ {
		msgs := a.buildMessages()
		stream, err := a.Prov.Stream(ctx, a.Wire, msgs, apiTools())
		if err != nil {
			emit(out, Event{Type: "error", Text: err.Error()})
			return
		}

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

		// no tool calls => final answer
		if len(calls) == 0 {
			emit(out, Event{Type: "turn_done", Text: text.String(), DurationMS: time.Since(start).Milliseconds()})
			return
		}

		// execute tools; approval modal rides the stream for this turn
		a.askApproval = func(action string) bool {
			ch := make(chan bool, 1)
			out <- Event{Type: "approval", Text: action, Approve: func(ok bool) { ch <- ok }}
			return <-ch
		}
		for _, c := range calls {
			emit(out, Event{Type: "tool_start", ToolName: c.Name, ToolArgs: string(c.Args)})
			res := a.Tools.Execute(c.Name, c.Args)
			emit(out, Event{Type: "tool_end", ToolName: c.Name, ToolOutput: res.Output, ToolOK: res.OK})
			a.Sess.Append(session.Msg{Role: "tool", Content: res.Output, ToolCallID: c.ID})
		}
		if n := a.maybeCompact(); n > 0 {
			emit(out, Event{Type: "notice", Text: fmt.Sprintf("compaction: squashed %d tool logs into diagnostic vectors", n)})
		}
	}
	emit(out, Event{Type: "error", Text: "turn limit reached without final answer"})
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
	return msgs
}

// apiTools converts the manifest to provider tools.
func apiTools() []provider.Tool {
	var out []provider.Tool
	for _, d := range tool.Manifest() {
		out = append(out, provider.Tool{Name: d.Name, Description: d.Description, Schema: d.Schema})
	}
	return out
}

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
