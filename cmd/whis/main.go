// whis is a hyper-lean, token-surgical AI coding CLI with an overkill TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"whis/internal/agent"
	"whis/internal/config"
	"whis/internal/project"
	"whis/internal/provider"
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
		verFlag    = flag.Bool("version", false, "print version")
	)
	flag.Parse()

	if *verFlag {
		fmt.Println("whis", version)
		return
	}

	root, _ := os.Getwd()
	cfg := config.Load()
	keys := provider.KeysMap(cfg.Keys.Anthropic, cfg.Keys.OpenAI, cfg.Keys.DeepSeek, cfg.Keys.OpenRouter, cfg.Keys.OllamaCloud)

	// subcommands
	switch flag.Arg(0) {
	case "init":
		if err := tui.RunInitWizard(); err != nil {
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

	// headless one-shot: model must resolve up front (no UI to pick from)
	if *promptFlag != "" {
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
		headless(a, *promptFlag)
		return
	}

	// interactive TUI: boots instantly, model picked in-app (opencode-style).
	// Esc interrupts the running agent loop.
	a := agent.NewUnbound(root, *autoFlag || cfg.AutoApprove)
	if *resumeFlag != "" {
		s, err := session.Load(*resumeFlag)
		if err != nil {
			fatal(err)
		}
		a.Sess = s
	}
	adapter := tui.NewAdapter(a, keys)
	adapter.Cfg = cfg
	// clean up the browser engine (if the agent launched it) when the
	// interactive session ends.
	defer tool.CloseBrowser()
	// WithMouseCellMotion enables terminal mouse reporting: without it the
	// TUI never receives wheel events (scrollbar stays dead).
	p := tea.NewProgram(tui.New(adapter), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
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
