package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"whis/internal/config"
)

// RunInitWizard is the `whis init` entry point.
func RunInitWizard() error {
	w := newWizard()
	p := tea.NewProgram(w, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fw, ok := final.(wizard); ok {
		return fw.result
	}
	return nil
}

type wizard struct {
	step    int
	inputs  []textinput.Model
	ollamas []string
	local   []string
	cfg     *config.Config
	result  error
	done    bool
}

var wizardSteps = []struct {
	title string
	hint  string
	env   string
}{
	{"DeepSeek", "primary coder model — cheapest strong option", "WHIS_DEEPSEEK_KEY"},
	{"Anthropic", "Claude models", "WHIS_ANTHROPIC_KEY"},
	{"OpenAI", "GPT models", "WHIS_OPENAI_KEY"},
	{"OpenRouter", "any model, one key", "WHIS_OPENROUTER_KEY"},
	{"Ollama Cloud", "ollama.com API key (optional)", "WHIS_OLLAMA_KEY"},
}

func newWizard() wizard {
	cfg := config.Load()
	w := wizard{cfg: cfg}
	for _, s := range wizardSteps {
		ti := textinput.New()
		ti.Placeholder = fmt.Sprintf("%s (env: %s)", s.title, s.env)
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
		ti.CharLimit = 200
		w.inputs = append(w.inputs, ti)
	}
	// detect local ollama
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:11434/api/tags")
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		b, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(b, &tags) == nil {
			for _, m := range tags.Models {
				w.local = append(w.local, m.Name)
			}
		}
	}
	w.inputs[0].Focus()
	return w
}

func (w wizard) Init() tea.Cmd { return textinput.Blink }

func (w wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			w.result = fmt.Errorf("cancelled")
			return w, tea.Quit
		case "enter":
			if w.step < len(wizardSteps) {
				v := strings.TrimSpace(w.inputs[w.step].Value())
				switch w.step {
				case 0:
					w.cfg.Keys.DeepSeek = pick(w.cfg.Keys.DeepSeek, v)
				case 1:
					w.cfg.Keys.Anthropic = pick(w.cfg.Keys.Anthropic, v)
				case 2:
					w.cfg.Keys.OpenAI = pick(w.cfg.Keys.OpenAI, v)
				case 3:
					w.cfg.Keys.OpenRouter = pick(w.cfg.Keys.OpenRouter, v)
				case 4:
					w.cfg.Keys.OllamaCloud = pick(w.cfg.Keys.OllamaCloud, v)
				}
				w.step++
				if w.step < len(w.inputs) {
					return w, w.inputs[w.step].Focus()
				}
			}
			// finish
			w.cfg.ApplyEnv()
			if err := w.cfg.Save(); err != nil {
				w.result = err
			}
			w.done = true
			return w, tea.Quit
		case "shift+tab":
			if w.step > 0 {
				w.step--
				return w, w.inputs[w.step].Focus()
			}
		case "tab":
			if w.step < len(wizardSteps)-1 {
				w.step++
				return w, w.inputs[w.step].Focus()
			}
		}
	}
	var cmd tea.Cmd
	if w.step < len(w.inputs) {
		w.inputs[w.step], cmd = w.inputs[w.step].Update(msg)
	}
	return w, cmd
}

func pick(existing, next string) string {
	if next != "" {
		return next
	}
	return existing
}

func (w wizard) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("WHIS INIT — zero-friction setup"))
	b.WriteString("\n\n")

	if len(w.local) > 0 {
		b.WriteString(okStyle.Render("[ok] Ollama detected:") + " " + dimStyle.Render(strings.Join(w.local, ", ")) + "\n")
		b.WriteString(dimStyle.Render("  use anytime with /model ollama:" + w.local[0] + "\n\n"))
	} else {
		b.WriteString(dimStyle.Render("○ No local Ollama on :11434 — start it later; /model ollama:<name> works when running\n\n"))
	}

	if w.done {
		b.WriteString(okStyle.Render("[ok] config saved to "+config.Path()) + "\n")
		b.WriteString(dimStyle.Render("run `whis` in your project to start") + "\n")
		return b.String()
	}

	if w.step < len(wizardSteps) {
		s := wizardSteps[w.step]
		b.WriteString(fmt.Sprintf("Step %d/%d — %s\n", w.step+1, len(wizardSteps)+1, titleStyle.Render(s.title)))
		b.WriteString(dimStyle.Render(s.hint) + "\n")
		b.WriteString(w.inputs[w.step].View() + "\n")
		b.WriteString(dimStyle.Render("Enter to continue · leave blank to skip · Tab/Shift+Tab navigate · Ctrl+C cancel") + "\n")
	}
	return b.String()
}

// titleStyle for wizard headers.
var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(ember)
