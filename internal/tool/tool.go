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
	index      *Index
	indexBuilt bool
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
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

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
	case "run_command":
		return e.RunCommand(gs("command"))
	case "search_codebase":
		return e.SearchCodebase(gs("pattern"), gs("glob"))
	case "list_tree":
		return e.ListTree(gs("path"))
	}
	return Result{Output: "unknown tool: " + name}
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
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Dir = e.Root
	// security: hard timeout so runaway processes cannot hang the agent
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	cmd.Cancel = func() error { // Go 1.20+: kills the whole process tree
		return cmd.Process.Kill()
	}
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
