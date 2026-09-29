// Package mcp implements WHIS's MCP (Model Context Protocol) host/client
// support: it spawns local MCP servers over stdio, lists their tools, and
// exposes them to the agent under namespaced names (server__tool).
//
// Design rules (deliberate, keep them):
//   - ZERO overhead when ~/.whis/mcp.json is missing or empty: no spawn,
//     no goroutines, no latency. MCP is opt-in by config file.
//   - A failing MCP server must NEVER break boot: 5s init timeout per
//     server, failures become a one-line warning and WHIS continues.
//   - Output is capped before it can reach the model: external DB tools
//     must not blow up the token budget.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig is one entry of ~/.whis/mcp.json (standard Claude/Cursor
// schema so users can copy their existing config verbatim). The
// whis-specific "disabled" flag is additive and ignored by other hosts.
type ServerConfig struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// Config is the whole mcp.json file.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ConfigPath returns ~/.whis/mcp.json (respects USERPROFILE on Windows).
func ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".whis", "mcp.json")
}

// LoadConfig reads the MCP config. Missing file or parse failure returns an
// empty config (MCP stays off; a broken file should not brick the CLI).
func LoadConfig() Config {
	var c Config
	path := ConfigPath()
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

// SaveConfig writes the config back (used by `whis mcp add`), creating
// ~/.whis when needed.
func SaveConfig(c Config) error {
	path := ConfigPath()
	if path == "" {
		return fmt.Errorf("cannot resolve home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

const (
	// initTimeout bounds the synchronous handshake of fast servers (native
	// binaries connect in well under a second).
	initTimeout = 5 * time.Second
	// bgInitTimeout is the budget for BACKGROUND connects, which absorb
	// slow starters like `npx` (5-10s of node startup + first-run package
	// download). WHIS boots instantly either way; the tools land when the
	// server is up.
	bgInitTimeout = 15 * time.Second
	callTimeout   = 120 * time.Second
	nsSep         = "__"
	maxToolLines  = 200
	maxToolBytes  = 20_000
)

// Manager owns all connected MCP servers for one WHIS process.
type Manager struct {
	sessions map[string]*mcp.ClientSession // server name -> live session
	tools    map[string]*mcp.Tool          // "server__tool" -> tool def
	order    []string                      // namespaced names, stable order
	servers  []string                      // connected server names, stable
	root     string                        // workspace root: filesystem__ relative paths resolve against it

	mu sync.Mutex // guards sessions/tools/order/servers/root: background connects race the agent loop
}

// NewManager builds an idle manager. SetRoot may be called after
// construction once the workspace is known (before first tool dispatch).
func NewManager() *Manager {
	return &Manager{sessions: map[string]*mcp.ClientSession{}, tools: map[string]*mcp.Tool{}}
}

// SetRoot records the workspace root for filesystem path normalization.
func (m *Manager) SetRoot(root string) {
	m.mu.Lock()
	m.root = root
	m.mu.Unlock()
}

// Connect spawns and initializes every configured server. Individual
// failures are collected as warnings; WHIS keeps booting regardless.
// cfg-rooted env vars inherit the parent environment (PATH etc.).
//
// slow (> initTimeout): connects synchronously in the background so WHIS
// boots instantly and the tools appear when the server is up; fast servers
// (native binaries) can be forced synchronous with force=true (used by
// `whis mcp`, where there is nothing else to do anyway).
func (m *Manager) Connect(ctx context.Context, cfg Config) []string {
	var (
		mu       sync.Mutex
		warnings []string
		wg       sync.WaitGroup
	)
	for name, sc := range cfg.MCPServers {
		if sc.Disabled {
			continue // token saver: zero spawns, zero prompt cost
		}
		if strings.TrimSpace(sc.Command) == "" {
			mu.Lock()
			warnings = append(warnings, fmt.Sprintf("mcp: server %q has no command — skipped", name))
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(name string, sc ServerConfig) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, bgInitTimeout)
			defer cancel()
			err := m.connectOne(cctx, name, sc)
			if err == nil {
				return
			}
			mu.Lock()
			warnings = append(warnings, fmt.Sprintf("mcp: %q unavailable (%v) — continuing without it", name, err))
			mu.Unlock()
		}(name, sc)
	}
	wg.Wait()
	return warnings
}

// HasServer reports whether a server by this name is connected.
func (m *Manager) HasServer(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[name]
	return ok
}

// ToolCount returns the number of tools a connected server exposes.
func (m *Manager) ToolCount(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for ns := range m.tools {
		if strings.HasPrefix(ns, name+nsSep) {
			n++
		}
	}
	return n
}

// ServerTools returns the namespaced tool names of one connected server.
func (m *Manager) ServerTools(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, ns := range m.order {
		if strings.HasPrefix(ns, name+nsSep) {
			out = append(out, ns)
		}
	}
	return out
}

// ConnectSync is Connect with a hard 5s budget, for callers that must know
// the outcome before continuing (`whis mcp` status). Slow servers fail
// here with a timeout — use background Connect for real sessions.
func (m *Manager) ConnectSync(ctx context.Context, cfg Config) []string {
	var (
		mu       sync.Mutex
		warnings []string
		wg       sync.WaitGroup
	)
	for name, sc := range cfg.MCPServers {
		if strings.TrimSpace(sc.Command) == "" {
			mu.Lock()
			warnings = append(warnings, fmt.Sprintf("mcp: server %q has no command — skipped", name))
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(name string, sc ServerConfig) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, initTimeout)
			defer cancel()
			err := m.connectOne(cctx, name, sc)
			if err == nil {
				return
			}
			mu.Lock()
			warnings = append(warnings, fmt.Sprintf("mcp: %q unavailable (%v) — continuing without it", name, err))
			mu.Unlock()
		}(name, sc)
	}
	wg.Wait()
	return warnings
}

// connectOne connects a single server and caches its tool list.
func (m *Manager) connectOne(ctx context.Context, name string, sc ServerConfig) error {
	cctx, cancel := context.WithTimeout(ctx, bgInitTimeout)
	defer cancel()

	cmd := exec.Command(sc.Command, sc.Args...) // #nosec G204 — command comes from the user's own config file
	cmd.Env = os.Environ()
	for k, v := range sc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	hideWindow(cmd)

	client := mcp.NewClient(&mcp.Implementation{Name: "whis", Version: "0.2.20"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}
	sess, err := client.Connect(cctx, transport, nil)
	if err != nil {
		return err
	}
	// initialize handshake result lands during Connect; ListTools proves
	// the server is actually serving (and gives us the tool list).
	res, err := sess.ListTools(cctx, nil)
	if err != nil {
		_ = sess.Close()
		return err
	}
	m.mu.Lock()
	m.sessions[name] = sess
	m.servers = append(m.servers, name)
	for _, t := range res.Tools {
		ns := name + nsSep + t.Name
		if _, dup := m.tools[ns]; dup {
			continue // first server wins on collision
		}
		m.tools[ns] = t
		m.order = append(m.order, ns)
	}
	m.mu.Unlock()
	return nil
}

// Connected reports whether any MCP server is live.
func (m *Manager) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions) > 0
}

// Stats returns "name (n tools)" summaries for boot notes.
func (m *Manager) Stats() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.servers))
	for _, s := range m.servers {
		n := 0
		for ns := range m.tools {
			if strings.HasPrefix(ns, s+nsSep) {
				n++
			}
		}
		out = append(out, fmt.Sprintf("%s (%d tools)", s, n))
	}
	return out
}

// Namespaced returns all MCP tool definitions (already namespaced,
// schema-cleaned) for the tool manifest.
func (m *Manager) Namespaced() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order
}

// IsMCP reports whether a tool name belongs to a connected MCP server.
func (m *Manager) IsMCP(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tools[name]
	return ok
}

// ToolDef converts one MCP tool into WHIS's unified schema shape:
// {"type":"object","properties":{...},"required":[...]} with meta fields
// ($schema, additionalProperties, ...) stripped so Gemini and
// DeepSeek/OpenAI strict function-calling accept it.
func ToolDef(t *mcp.Tool) map[string]any {
	props := map[string]any{}
	var req []string
	if t.InputSchema != nil {
		var sm map[string]any
		switch s := t.InputSchema.(type) {
		case map[string]any:
			sm = s
		case json.RawMessage:
			_ = json.Unmarshal(s, &sm)
		case []byte:
			_ = json.Unmarshal(s, &sm)
		}
		if p, ok := sm["properties"].(map[string]any); ok {
			props = p
		}
		if r, ok := sm["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					req = append(req, s)
				}
			}
		}
	}
	if req == nil {
		req = []string{}
	}
	return map[string]any{
		"type":        "object",
		"description": t.Description,
		"properties":  props,
		"required":    req,
	}
}

// ManifestJSON returns the raw MCP tool JSON for a namespaced name — used
// by the agent layer to build provider tool definitions.
func (m *Manager) ManifestJSON(ns string) ([]byte, error) {
	m.mu.Lock()
	t, ok := m.tools[ns]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown mcp tool %s", ns)
	}
	return json.Marshal(ToolDef(t))
}

// CallTool routes a call to the owning server, caps the output, and
// returns (text, ok).
//
// Filesystem path normalization: the standard
// @modelcontextprotocol/server-filesystem rejects relative paths and paths
// outside its configured roots. Models routinely emit bare names ("landing"",
// "index.html"), so any filesystem__ call with a relative string "path"
// argument is resolved to an absolute path against the workspace root before
// dispatch. Non-string / absolute / non-filesystem args pass through.
func (m *Manager) CallTool(ctx context.Context, ns string, args json.RawMessage) (string, bool) {
	server, _, found := strings.Cut(ns, nsSep)
	m.mu.Lock()
	sess := m.sessions[server]
	root := m.root
	m.mu.Unlock()
	if !found || sess == nil {
		return "unknown mcp tool: " + ns, false
	}
	var params any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &params)
	}
	if server == "filesystem" {
		params = normalizeFSPaths(params, root)
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := sess.CallTool(cctx, &mcp.CallToolParams{Name: strings.TrimPrefix(ns, server+nsSep), Arguments: params})
	if err != nil {
		return "mcp call failed: " + err.Error(), false
	}
	if res.IsError {
		return "mcp tool reported an error (see tool output)", false
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return capOutput(b.String()), true
}

// normalizeFSPaths rewrites any string field named "path" (or "paths"
// entries) to an absolute path rooted at the workspace when it is relative.
// Returns the original value untouched when root is unknown or the shape is
// not a recognized argument map.
func normalizeFSPaths(params any, root string) any {
	if root == "" || params == nil {
		return params
	}
	pm, ok := params.(map[string]any)
	if !ok {
		return params
	}
	norm := func(v any) any {
		s, ok := v.(string)
		if !ok || s == "" || filepath.IsAbs(s) {
			return v
		}
		if abs, err := filepath.Abs(filepath.Join(root, s)); err == nil {
			return abs
		}
		return v
	}
	if p, ok := pm["path"]; ok {
		pm["path"] = norm(p)
	}
	if ps, ok := pm["paths"].([]any); ok {
		out := make([]any, len(ps))
		for i, p := range ps {
			out[i] = norm(p)
		}
		pm["paths"] = out
	}
	return pm
}

// capOutput applies WHIS's standard truncation to external tool output.
func capOutput(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > maxToolLines {
		lines = append(lines[:maxToolLines], fmt.Sprintf("... (%d more lines truncated)", len(lines)-maxToolLines))
		s = strings.Join(lines, "\n")
	}
	if len(s) > maxToolBytes {
		s = s[:maxToolBytes] + "\n... (output truncated at " + fmt.Sprint(maxToolBytes) + " bytes)"
	}
	return s
}

// Close shuts every session down; the SDK's stdio transport already runs
// the full stdin-close -> wait -> terminate -> kill ladder per process.
func (m *Manager) Close() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*mcp.ClientSession{}
	m.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.Close()
	}
}

// hideWindow stops console-ette child processes from flashing a window on
// Windows when spawned from a GUI-adjacent context.
func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		return
	}
	hideWindowsWindow(cmd)
}
