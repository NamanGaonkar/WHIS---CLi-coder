// Package tool implements WHIS's surgical tool arsenal: symbol location,
// range reads, fuzzy patching, sandboxed commands, code search and tree listing.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// MaxReadLines blocks unforced large reads (token surgery).
const MaxReadLines = 120

// MaxCommandOutput caps run_command output lines.
const MaxCommandOutput = 40

// CommandTimeout caps every shell command so the agent can never hang the
// session on a waiting process (servers excluded via trailing '&').
const CommandTimeout = 120 * time.Second

// Result is a tool outcome returned to the model.
type Result struct {
	OK     bool
	Output string
}

// Env is the tool execution environment for one workspace.
type Env struct {
	Root        string
	AutoApprove bool
	// RiskBased: only SENSITIVE actions prompt (destructive commands etc.);
	// routine file edits/creates and safe commands run without asking.
	RiskBased bool
	// AskApproval is called before sensitive actions. Return true to proceed.
	AskApproval func(action string) bool
	// OnSnapshot is called before each mutation (used by /undo).
	OnSnapshot func() error
	// Mem is the persistent cross-session memory store (nil-safe: memory
	// tools degrade gracefully when unset, e.g. in /task subagents).
	Mem MemoryStore
	// RunCtx is the AGENT RUN's context, set by the agent loop before each
	// run. Shell commands derive their timeout context from it, so an esc
	// interrupt (context cancel) kills a hung child (cmd /c date waiting
	// on stdin) INSTANTLY instead of running out the full 120s timeout.
	RunCtx     context.Context
	index      *Index
	indexBuilt bool
}

// MemHit is one memory row crossing the tool boundary.
type MemHit struct {
	ID   int
	Text string
}

// MemoryStore is the memory capability the tool env needs (implemented by
// the agent's adapter over *memory.Store; an interface keeps the tool
// package dependency-free).
type MemoryStore interface {
	Save(text string) MemHit
	Forget(id int, textQuery string) int
	Search(query string) []MemHit
	Count() int
}

// Action risk classes.
const (
	riskSafe      = "safe"      // file edits/creates in workspace, build/run commands
	riskSensitive = "sensitive" // destructive or system-touching commands
)

// dangerRe matches commands that can destroy data, escalate privileges, or
// execute remote code. These always prompt, even in auto mode.
var dangerRe = regexp.MustCompile(`(?i)(\brm\s+(-[a-z]*r|-[a-z]*f)` +
	`|\brdel\b|\bdel\s+/[sq]|\brd\s+/s|\brmdir\b` +
	`|\bformat\s|\bmkfs\b|\bdiskpart\b|\bdd\s+if=` +
	`|\bsudo\b|\bdoas\b|\brunas\b` +
	`|\bgit\s+(push|reset\s+--hard|clean\s+-[fd])` +
	`|\b(curl|wget)\b[^|;]*\|\s*(sh|bash|zsh|powershell)` +
	`|\breg\s+(delete|add)\b|\bregedit\b` +
	`|\bshutdown\b|\breboot\b|\btaskkill\b|\bkill\s+-9` +
	`|\bchmod\s+777\b|\bchown\s+-R\b` +
	`|\bdrop\s+(table|database)\b)`)

// NewEnv builds a tool Env rooted at dir.
func NewEnv(root string) *Env {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Env{Root: abs}
}

// Definition is the model-facing tool schema.
type Definition struct {
	Name        string
	Description string
	Schema      map[string]any
}

func obj(props map[string]any, required ...string) map[string]any {
	// DeepSeek (and other strict OpenAI-compatible APIs) reject a null
	// "required": it must ALWAYS be an array, even when empty.
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

// arr describes an array-of-objects parameter (multi_edit edits).
func arr(desc string, props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       obj(props, required...),
	}
}

// Manifest returns all tool definitions for the system prompt / API.
func Manifest() []Definition {
	return []Definition{
		{"locate_symbol", "Extract a specific function, type, or method body via the AST symbol index instead of reading whole files.", obj(map[string]any{
			"file": str("file path relative to workspace root"),
			"name": str("symbol name (function, type, method)"),
		}, "file", "name")},
		{"read_range", "Read an explicit line range from a file (1-indexed, inclusive). Large reads over 120 lines are blocked unless force=true.", obj(map[string]any{
			"path":  str("file path relative to workspace root"),
			"start": num("start line (1-indexed)"),
			"end":   num("end line (inclusive)"),
			"force": boolp("allow reads larger than 120 lines"),
		}, "path", "start", "end")},
		{"apply_patch", "Apply a surgical edit via a SEARCH/REPLACE block. SEARCH must match the file exactly (or fuzzily within ~2 edits).", obj(map[string]any{
			"path":    str("file path relative to workspace root"),
			"search":  str("exact existing text to find"),
			"replace": str("replacement text"),
		}, "path", "search", "replace")},
		{"multi_edit", "Apply SEVERAL search/replace edits in ONE call, optionally across multiple files. Best for multi-site refactors: one approval, one undo point. Each edit: {path, search, replace}; empty search only when creating a new file. All searches are validated first — if any is not found, NOTHING is written.", obj(map[string]any{
			"edits": arr("the batch of edits", map[string]any{
				"path":    str("file path relative to workspace root"),
				"search":  str("exact existing text to find"),
				"replace": str("replacement text"),
			}, "path", "search", "replace"),
		}, "edits")},
		{"run_command", "Run a shell command in the workspace root. Output capped at 40 lines. Requires approval unless auto-approve.", obj(map[string]any{
			"command": str("shell command to run"),
		}, "command")},
		{"search_codebase", "Regex search across the workspace (ripgrep-backed). Returns file:line: match lines.", obj(map[string]any{
			"pattern": str("regular expression"),
			"glob":    str("optional file glob filter, e.g. *.go"),
		}, "pattern")},
		{"list_tree", "List workspace files/dirs up to depth 4, skipping .git, node_modules, .venv and binaries.", obj(map[string]any{
			"path": str("optional subdirectory to start from"),
		})},
		{"web_fetch", "Fetch a public web page or API endpoint and return readable text (HTML stripped). Use for docs, lookups, and internet research.", obj(map[string]any{
			"url": str("http/https URL to fetch"),
		}, "url")},
		{"memory_save", "Persist a durable fact the user asked to remember (preferences, project context, decisions). It becomes available in ALL future sessions.", obj(map[string]any{
			"text": str("the fact to remember, one self-contained sentence"),
		}, "text")},
		{"task_tracker", "Maintain YOUR working plan for multi-step jobs. action=write replaces the whole checklist (markdown: '- [ ]' todo, '- [x]' done); action=get reads it. Write a plan BEFORE starting a multi-step job and update it as you complete steps — it persists across turns.", obj(map[string]any{
			"action": str("get | write"),
			"body":   str("for write: the full markdown checklist"),
		}, "action")},
		{"memory_recall", "Search previously remembered facts. Use when the user refers to something they told you earlier.", obj(map[string]any{
			"query": str("words to search for (empty = list everything)"),
		})},
		{"memory_forget", "Delete one remembered fact (by its id from memory_recall, or by matching words).", obj(map[string]any{
			"id":   num("memory id to delete"),
			"text": str("or: words matching the memory to delete"),
		})},
		{"browser", "Drive a real headless Chromium: use when web_fetch returns a bot-challenge page, the site needs JavaScript, or you must verify a running local dev server or a live UI error. Actions: navigate (url), click (css selector), get_text (css selector), screenshot (optional path), close.", obj(map[string]any{
			"action": str("navigate | click | get_text | screenshot | close"),
			"arg":    str("url for navigate, css selector for click/get_text, optional save path for screenshot"),
		}, "action")},
	}
}

// Execute dispatches a tool call by name.
func (e *Env) Execute(name string, args json.RawMessage) Result {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{Output: "invalid tool arguments: " + err.Error()}
		}
	}
	gs := func(k string) string { v, _ := a[k].(string); return v }
	gi := func(k string) int { v, _ := a[k].(float64); return int(v) }
	gb := func(k string) bool { v, _ := a[k].(bool); return v }

	switch name {
	case "locate_symbol":
		return e.LocateSymbol(gs("file"), gs("name"))
	case "read_range":
		return e.ReadRange(gs("path"), gi("start"), gi("end"), gb("force"))
	case "apply_patch":
		return e.ApplyPatch(gs("path"), gs("search"), gs("replace"))
	case "multi_edit":
		eds, err := parseMultiEditArgs(args)
		if err != nil {
			return Result{Output: "multi_edit: bad arguments: " + err.Error()}
		}
		return e.MultiEdit(eds)
	case "run_command":
		return e.RunCommand(gs("command"))
	case "search_codebase":
		return e.SearchCodebase(gs("pattern"), gs("glob"))
	case "list_tree":
		return e.ListTree(gs("path"))
	case "web_fetch":
		return e.WebFetch(gs("url"))
	case "web_search":
		return e.WebSearch(gs("query"))
	case "browser":
		return e.Browser(gs("action"), gs("arg"))
	case "task_tracker":
		return e.taskTracker(gs("action"), gs("body"))
	case "memory_save":
		if e.Mem == nil {
			return Result{OK: false, Output: "memory store unavailable"}
		}
		text := strings.TrimSpace(gs("text"))
		if text == "" {
			return Result{OK: false, Output: "memory_save needs text"}
		}
		if len(text) > 500 {
			text = text[:500]
		}
		e.Mem.Save(text)
		return Result{OK: true, Output: "remembered: " + text}
	case "memory_recall":
		if e.Mem == nil {
			return Result{OK: false, Output: "memory store unavailable"}
		}
		q := strings.TrimSpace(gs("query"))
		rows := e.Mem.Search(q)
		if len(rows) == 0 {
			return Result{OK: true, Output: "no memories matched" + maybeQuery(q)}
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%d memories:\n", len(rows)))
		for _, r := range rows {
			fmt.Fprintf(&b, "%d. %s\n", r.ID, r.Text)
		}
		return Result{OK: true, Output: strings.TrimSpace(b.String())}
	case "memory_forget":
		if e.Mem == nil {
			return Result{OK: false, Output: "memory store unavailable"}
		}
		n := e.Mem.Forget(gi("id"), gs("text"))
		if n == 0 {
			return Result{OK: false, Output: "no matching memory to forget"}
		}
		return Result{OK: true, Output: fmt.Sprintf("forgot %d memor%s", n, map[bool]string{true: "y", false: "ies"}[n == 1])}
	}
	return Result{Output: "unknown tool: " + name}
}

func maybeQuery(q string) string {
	if q != "" {
		return " for \"" + q + "\""
	}
	return ""
}

// resolve safely joins a workspace-relative path.
func (e *Env) resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	abs := filepath.Join(e.Root, p)
	if !strings.HasPrefix(abs, e.Root+string(filepath.Separator)) && abs != e.Root {
		return "", fmt.Errorf("path escapes workspace: %s", p)
	}
	return abs, nil
}

// rel converts an absolute path back to workspace-relative with / separators.
func (e *Env) rel(abs string) string {
	r, err := filepath.Rel(e.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

// RunCommand executes a sandboxed shell command with approval + output cap.
// Risk: destructive commands prompt even in auto mode; normal build/run/test
// commands execute immediately.
func (e *Env) RunCommand(command string) Result {
	if strings.TrimSpace(command) == "" {
		return Result{Output: "empty command"}
	}
	risk := riskSafe
	if dangerRe.MatchString(command) {
		risk = riskSensitive
	}
	if !e.approve(risk, "run command: "+command) {
		return Result{Output: "user declined command execution."}
	}
	if e.OnSnapshot != nil {
		_ = e.OnSnapshot()
	}
	// security: hard timeout so runaway processes cannot hang the agent.
	// The command MUST be created with exec.CommandContext (Go requires it
	// when Cancel is set — plain exec.Command + Cancel fails every run with
	// "command with a non-nil Cancel was not created with CommandContext").
	// The timeout derives from the RUN context when available: esc cancels
	// the run context, which kills the child instantly (god-key esc).
	base := e.RunCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, CommandTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = e.Root
	// stdin MUST be closed: interactive prompts (cmd /c date, git commit
	// editor, any "press any key") block forever waiting for keyboard
	// input the agent will never type — the classic "stuck at run_command
	// date" hang. Closing stdin makes those commands fail fast or take
	// their default instead of dead-waiting for 120s.
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{OK: false, Output: capLines(string(out), MaxCommandOutput) +
			"\n[timeout] killed after " + CommandTimeout.String()}
	}
	res := capLines(string(out), MaxCommandOutput)
	if err != nil {
		res = res + "\n[exit] " + err.Error()
	}
	return Result{OK: err == nil, Output: strings.TrimSpace(res)}
}

func capLines(s string, max int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > max {
		lines = append(lines[:max], fmt.Sprintf("... (%d more lines truncated)", len(lines)-max))
	}
	return strings.Join(lines, "\n")
}

// approve gates an action. AutoApprove (-y) allows everything; RiskBased
// mode auto-allows safe actions and prompts only for sensitive ones.
func (e *Env) approve(risk, action string) bool {
	if e.AutoApprove {
		return true
	}
	if e.RiskBased && risk == riskSafe {
		return true
	}
	if e.AskApproval == nil {
		return false
	}
	return e.AskApproval(action)
}

// fileExists reports whether path exists.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
