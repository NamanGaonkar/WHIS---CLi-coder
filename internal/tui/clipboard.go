package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbletea"
)

// lastTranscriptText picks the text a user most plausibly wants copied:
// the newest md (assistant reply) or user line. Markdown is stored as
// rendered ANSI in the transcript, so the raw source is recovered from the
// session store instead — falling back to the stored body for kinds that
// are not round-tripped (info / error / tool output). Plan panes, tool
// traces and banners are skipped (they are not "the answer").
func (m *model) lastTranscriptText() (string, bool) {
	// pass 1: newest reply or user prompt wins — even over newer info
	// lines (busy notices, previous "copied" confirmations)
	for i := len(m.lines) - 1; i >= 0; i-- {
		l := m.lines[i]
		if l.kind != "md" && l.kind != "user" {
			continue
		}
		if l.kind == "md" {
			if txt := m.agent.LastAssistantText(); strings.TrimSpace(txt) != "" {
				return txt, true
			}
		}
		if strings.TrimSpace(stripANSI(l.body)) != "" {
			return stripANSI(l.body), true
		}
	}
	// pass 2: fall back to system output (command results, errors)
	for i := len(m.lines) - 1; i >= 0; i-- {
		l := m.lines[i]
		if l.kind == "info" || l.kind == "error" || l.kind == "toolout" {
			if strings.TrimSpace(stripANSI(l.body)) != "" {
				return stripANSI(l.body), true
			}
		}
	}
	return "", false
}

// osc52Limit keeps the OSC 52 fallback payload far below the ~100k byte
// limit most terminals enforce and below bubbletea's per-line truncation,
// which would otherwise corrupt the escape sequence.
const osc52Limit = 7000

// copyResult copies text to the system clipboard and returns a
// user-facing confirmation plus an optional tea.Cmd.
//
// Windows / macOS: atotto uses the native clipboard directly (no external
// binary needed). Linux desktop needs xclip / xsel / wl-copy; over SSH a
// short OSC 52 escape is emitted instead so the local terminal copies it.
func copyResult(text string) (string, tea.Cmd) {
	if err := clipboard.WriteAll(text); err == nil {
		return fmt.Sprintf("copied %d bytes to clipboard (ctrl+y or /copy)", len(text)), nil
	}
	if os.Getenv("SSH_TTY") != "" && len(text) <= osc52Limit {
		enc := base64.StdEncoding.EncodeToString([]byte(text))
		return "copied via terminal (OSC 52)", tea.Printf("\x1b]52;c;%s\x07", enc)
	}
	return "clipboard unavailable — on Linux install xclip (X11) or wl-clipboard (Wayland)", nil
}
