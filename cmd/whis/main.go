// whis is a hyper-lean, token-surgical AI coding CLI with an overkill TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"whis/internal/agent"
	"whis/internal/config"
	"whis/internal/lock"
	"whis/internal/mcp"
	"whis/internal/project"
	"whis/internal/provider"
	"whis/internal/selfupdate"
	"whis/internal/session"
	"whis/internal/tool"
	"whis/internal/tui"

	"github.com/playwright-community/playwright-go"
)

var version = "0.1.0"

func main() {
	var (
		modelFlag  = flag.String("m", "", "model slug (e.g. deepseek-v4-flash, ollama:<name>, or:<vendor/model>) — or pick in-app with /")
		promptFlag = flag.String("p", "", "headless one-shot prompt (non-interactive)")
		autoFlag   = flag.Bool("y", false, "auto-approve shell commands and file edits (unattended)")
		resumeFlag = flag.String("r", "", "resume a session id (see whis -l)")
		listFlag   = flag.Bool("l", false, "list saved sessions and exit")
		initFlag   = flag.Bool("init", false, "generate WHIS.md project guide and exit")
		undoFlag   = flag.Bool("undo", false, "roll back to the last whis snapshot and exit")
		updateFlag = flag.Bool("update", false, "update whis to the latest release (accepts -update / --update / update)")
		verFlag    = flag.Bool("version", false, "print version")
	)
	flag.Parse()

	if *verFlag {
		fmt.Println("whis", version)
		return
	}
	// self-update: `whis update`, `whis -update` and `whis --update` all work
	if *updateFlag {
		if err := selfupdate.Run(version); err != nil {
			fatal(err)
		}
		return
	}

	root, _ := os.Getwd()
	cfg := config.Load()
	keys := provider.KeysMap(cfg.Keys.Anthropic, cfg.Keys.OpenAI, cfg.Keys.DeepSeek, cfg.Keys.OpenRouter, cfg.Keys.OllamaCloud)
	// 2026 additions: the eight OpenAI-compatible vendors share the same
	// resolve path; seed their keys (may be empty) into the live map.
	keys["gemini"], keys["xai"], keys["mistral"], keys["moonshot"] = cfg.Keys.Gemini, cfg.Keys.XAI, cfg.Keys.Mistral, cfg.Keys.Moonshot
	keys["qwen"], keys["zai"], keys["minimax"], keys["groq"] = cfg.Keys.Qwen, cfg.Keys.Zai, cfg.Keys.MiniMax, cfg.Keys.Groq

	// subcommands
	switch flag.Arg(0) {
	case "init":
		if err := tui.RunInitWizard(); err != nil {
			fatal(err)
		}
		return
	case "update":
		// self-update the binary from GitHub Releases (public repo, no key)
		if err := selfupdate.Run(version); err != nil {
			fatal(err)
		}
		return
	case "browser-install":
		fmt.Println("downloading headless Chromium (one-time, ~90 MB)...")
		if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
			fatal(err)
		}
		fmt.Println("browser engine ready.")
		return
	case "mcp":
		// `whis mcp add <preset>` — one-command community presets.
		if flag.Arg(1) == "add" {
			addMCPPreset(flag.Arg(2))
			return
		}
		// status: connect configured servers, list their tools, exit.
		m := mcp.NewManager()
		defer m.Close()
		cfgM := mcp.LoadConfig()
		if len(cfgM.MCPServers) == 0 {
			fmt.Println("no MCP servers configured — add them to", mcp.ConfigPath())
			fmt.Println(`schema: {"mcpServers":{"name":{"command":"...","args":[...],"env":{}}}}`)
			return
		}
		fmt.Println("connecting...")
		for _, w := range m.Connect(context.Background(), cfgM) {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		if !m.Connected() {
			fmt.Println("no MCP servers connected.")
			return
		}
		for _, s := range m.Stats() {
			fmt.Println("connected:", s)
		}
		for _, ns := range m.Namespaced() {
			fmt.Println("  tool:", ns)
		}
		return
	}
	if *initFlag {
		md, err := project.GenerateWHISMD(root)
		if err != nil {
			fatal(err)
		}
		fmt.Println("wrote WHIS.md:")
		fmt.Println(md)
		return
	}
	if *listFlag {
		for _, id := range session.List() {
			fmt.Println(id)
		}
		return
	}
	if *undoFlag {
		a := agent.NewUnbound(root, true)
		fmt.Println(agent.Undo(a))
		return
	}

	// headless one-shot: model must resolve up front (no UI to pick from).
	// Still lock the folder (non-interactive: refuse, don't prompt) so a
	// one-shot never double-books a folder the TUI already holds — keeps
	// the provider API from getting hammered by parallel sessions. The
	// lock auto-expires when this process exits (dead-pid sweep).
	if *promptFlag != "" {
		if _, lerr := lock.Try(root); lerr != nil {
			fatal(lerr)
		}
		slug := *modelFlag
		if slug == "" {
			slug = cfg.Model
		}
		if slug == "" {
			slug = defaultModel(cfg, keys)
		}
		a, err := agent.New(root, slug, keys, *autoFlag || cfg.AutoApprove)
		if err != nil {
			fatal(err)
		}
		mcpMgr := mcp.NewManager()
		defer mcpMgr.Close()
		for _, w := range a.AttachMCP(mcpMgr) {
			fmt.Fprintln(os.Stderr, "whis: warning:", w)
		}
		headless(a, *promptFlag)
		return
	}

	// interactive TUI: boots instantly, model picked in-app (opencode-style).
	// Esc interrupts the running agent loop.
	a := agent.NewUnbound(root, *autoFlag || cfg.AutoApprove)
	// MCP: opt-in via ~/.whis/mcp.json. Missing/empty config = zero spawns,
	// zero latency, zero prompt bloat. Failures warn, never block boot.
	mcpMgr := mcp.NewManager()
	for _, w := range a.AttachMCP(mcpMgr) {
		fmt.Fprintln(os.Stderr, "whis: warning:", w)
	}
	defer mcpMgr.Close() // kills child processes on any exit path
	if *resumeFlag != "" {
		s, err := session.Load(*resumeFlag)
		if err != nil {
			fatal(err)
		}
		a.Sess = s
	}
	adapter := tui.NewAdapter(a, keys)
	adapter.Cfg = cfg
	tui.Version = version // status bar shows the real build, not a stale default

	// single-instance rule: one whis session per folder per device. A second
	// window sees who holds the lock and may TAKE OVER — the request file
	// tells the running session to save and close itself automatically.
	l, lerr := lock.Try(root)
	if he, held := lerr.(*lock.HeldError); held {
		started := he.Info.Started
		if t, perr := time.Parse(time.RFC3339, he.Info.Started); perr == nil {
			started = t.Local().Format("Jan 2 15:04")
		}
		who := he.Info.User
		if who == "" {
			who = fmt.Sprintf("pid %d", he.Info.PID)
		}
		fmt.Printf("whis is already open on this device: %s (pid %d)\n  folder:  %s\n  started: %s\n", who, he.Info.PID, he.Info.Folder, started)
		fmt.Print("take over? that saves and closes the other session, then opens whis here [y/N] ")
		resp := ""
		fmt.Scanln(&resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") {
			fmt.Println("okay — staying out. close the other whis window (or answer y) to start one here.")
			return
		}
		l, lerr = lock.RequestTakeover(root, 5*time.Second)
		if lerr != nil {
			fatal(fmt.Errorf("takeover failed — the other window did not release the folder in time"))
		}
		fmt.Println("took over — the previous session was saved and closed.")
	} else if lerr != nil {
		fatal(lerr)
	}
	defer l.Release()

	// clean up the browser engine (if the agent launched it) when the
	// interactive session ends.
	defer tool.CloseBrowser()
	// WithMouseAllMotion enables terminal mouse reporting INCLUDING hover
	// motion (CellMotion only reports while a button is held): without it the
	// TUI never receives wheel events (scrollbar stays dead).
	p := tea.NewProgram(tui.New(adapter), tea.WithAltScreen(), tea.WithMouseAllMotion())
	// Bracketed paste: bubbletea v1.2.4 PARSES the ESC[200~/ESC[201~ wrap
	// into a single paste message but (pre-v1.3) has no option to request
	// the terminal mode. Enable it ourselves; restoreTerminal() disables it
	// on every exit path. Without the mode a paste arrives as a burst of
	// synthetic keystrokes and a paste starting with "/" on an empty box
	// opened the command menu and swallowed the clipboard as menu input.
	fmt.Print("\x1b[?2004h")
	// Watch for a takeover request from a newer window. p.Send is safe as
	// soon as NewProgram returns (ctx is initialized there), so start the
	// watcher before Run — the first request politely closes THIS session.
	l.WatchForTakeover(func() { p.Send(tui.TakeoverMsg{}) })
	if _, err := p.Run(); err != nil {
		restoreTerminal()
		fatal(err)
	}
	restoreTerminal()
}

// restoreTerminal force-disables the input modes whis enables (mouse
// tracking, bracketed paste) and re-shows the cursor AFTER the Bubble Tea
// program exits. bubbletea unwinds these itself on clean exits, but any
// missed path leaked mouse-tracking mode: the shell then interpreted mouse
// reports as typed text ("[<51;64;22M") and PowerShell threw ParserErrors
// on every line after closing whis. These sequences are idempotent and
// harmless when the modes are already off.
func restoreTerminal() {
	fmt.Print("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l") // mouse off
	fmt.Print("\x1b[?2004l")                                  // bracketed paste off
	fmt.Print("\x1b[?25h")                                    // cursor show
}

// mcpPresets are one-command templates: name -> builder that asks for the
// single value it needs and returns the server config.
func mcpPresets() map[string]func() (mcp.ServerConfig, bool) {
	ask := func(label string) string {
		fmt.Print(label + ": ")
		var v string
		fmt.Scanln(&v)
		return strings.TrimSpace(v)
	}
	return map[string]func() (mcp.ServerConfig, bool){
		"filesystem": func() (mcp.ServerConfig, bool) {
			dir := ask("directory to expose (absolute path)")
			if dir == "" {
				return mcp.ServerConfig{}, false
			}
			return mcp.ServerConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", dir}}, true
		},
		"github": func() (mcp.ServerConfig, bool) {
			tok := ask("GitHub personal access token")
			if tok == "" {
				return mcp.ServerConfig{}, false
			}
			return mcp.ServerConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, Env: map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": tok}}, true
		},
		"sqlite": func() (mcp.ServerConfig, bool) {
			db := ask("path to the .db file")
			if db == "" {
				return mcp.ServerConfig{}, false
			}
			return mcp.ServerConfig{Command: "uvx", Args: []string{"mcp-server-sqlite", "--db-path", db}}, true
		},
		"brave": func() (mcp.ServerConfig, bool) {
			key := ask("Brave Search API key")
			if key == "" {
				return mcp.ServerConfig{}, false
			}
			return mcp.ServerConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-brave-search"}, Env: map[string]string{"BRAVE_API_KEY": key}}, true
		},
		"fetch": func() (mcp.ServerConfig, bool) {
			return mcp.ServerConfig{Command: "uvx", Args: []string{"mcp-server-fetch"}}, true
		},
	}
}

// addMCPPrompt merges a preset into ~/.whis/mcp.json.
func addMCPPreset(name string) {
	presets := mcpPresets()
	if name == "" {
		fmt.Println("usage: whis mcp add <preset>")
		fmt.Println("presets:")
		for p := range presets {
			fmt.Println("  " + p)
		}
		return
	}
	build, ok := presets[name]
	if !ok {
		fatal(fmt.Errorf("unknown preset %q (try: whis mcp add)", name))
	}
	sc, ok := build()
	if !ok {
		fmt.Println("cancelled — nothing written")
		return
	}
	cfg := mcp.LoadConfig()
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcp.ServerConfig{}
	}
	cfg.MCPServers[name] = sc
	if err := mcp.SaveConfig(cfg); err != nil {
		fatal(err)
	}
	fmt.Printf("saved %q to %s — restart whis (or run whis mcp) to connect\n", name, mcp.ConfigPath())
}

// defaultModel picks the first usable model for headless runs, or empty.
func defaultModel(cfg *config.Config, keys map[string]string) string {
	// local ollama first (no key needed)
	if models := provider.DetectOllamaModels(provider.OllamaHost()); len(models) > 0 {
		return "ollama:" + models[0]
	}
	for prov, slug := range map[string]string{
		"deepseek": "deepseek-v4-flash", "anthropic": "claude-sonnet-5",
		"openai": "gpt-6-sol", "openrouter": "or:anthropic/claude-sonnet-5",
	} {
		if keys[prov] != "" {
			return slug
		}
	}
	return ""
}

// buildAgent constructs an agent with a resolved model (headless only).
func buildAgent(root, slug string, keys map[string]string, auto bool) (*agent.Agent, error) {
	if slug == "" {
		return nil, fmt.Errorf("no usable model — configure a key (whis init) or start Ollama")
	}
	return agent.New(root, slug, keys, auto)
}

// headless runs a one-shot prompt in plain mode (piping-friendly).
func headless(a *agent.Agent, prompt string) {
	a.Tools.AskApproval = func(action string) bool {
		if a.AutoApprove {
			return true
		}
		fmt.Fprintf(os.Stderr, "whis: approve %s? [y/N] ", action)
		var resp string
		fmt.Scanln(&resp)
		return strings.EqualFold(strings.TrimSpace(resp), "y")
	}
	ch, err := a.Run(context.Background(), prompt)
	if err != nil {
		fatal(err)
	}
	defer tool.CloseBrowser()
	for ev := range ch {
		switch ev.Type {
		case "reasoning":
			fmt.Fprintf(os.Stderr, "\x1b[2m%s\x1b[0m", ev.Text)
		case "text":
			fmt.Print(ev.Text)
		case "tool_start":
			fmt.Fprintf(os.Stderr, "\n\x1b[36m> %s %s\x1b[0m\n", ev.ToolName, ev.ToolArgs)
		case "tool_end":
			if !ev.ToolOK {
				fmt.Fprintf(os.Stderr, "\x1b[31m%s\x1b[0m\n", ev.ToolOutput)
			} else {
				fmt.Fprintf(os.Stderr, "\x1b[2m%s\x1b[0m\n", ev.ToolOutput)
			}
		case "approval":
			fmt.Fprintf(os.Stderr, "\x1b[33mapproval: %s\x1b[0m\n", ev.Text)
			if ev.Approve != nil {
				fmt.Fprint(os.Stderr, "approve? [y/N] ")
				var resp string
				fmt.Scanln(&resp)
				ev.Approve(strings.EqualFold(strings.TrimSpace(resp), "y"))
			}
		case "notice":
			fmt.Fprintf(os.Stderr, "\x1b[2m· %s\x1b[0m\n", ev.Text)
		case "error":
			fmt.Fprintf(os.Stderr, "\x1b[31merror: %s\x1b[0m\n", ev.Text)
		}
	}
	fmt.Println()
	in, cached, out, cost := a.Sess.Totals()
	fmt.Fprintf(os.Stderr, "\x1b[2m[whis] tokens in=%d cached=%d out=%d cost=$%.4f\x1b[0m\n", in, cached, out, cost)
}

var _ = session.List

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "whis:", err)
	os.Exit(1)
}
