package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbletea"
)

// copy targets for /copy <what> (and the arg-less default).
const (
	copyTargetReply  = "reply"
	copyTargetPrompt = "prompt"
	copyTargetOut    = "output"
)

// lastTranscriptText picks the text a user most plausibly wants copied,
// newest first. Markdown is stored as rendered ANSI in the transcript, so
// the raw source is recovered from the session store instead — falling
// back to the stored body for kinds that are not round-tripped. Plan
// panes, tool traces and banners are never "the answer".
//
// Priority fix: an info line (busy notice, a previous "copied" ack) newer
// than the reply must NOT shadow it — replies/prompts win over system
// chatter, and empty md lines (tool-call-only turns) fall through to the
// rendered body, then to system output.
func (m *model) lastTranscriptText() (string, bool) {
	// pass 1: newest reply or user prompt wins — even over newer info lines
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

// lastTranscriptOutput returns the newest tool output line (build logs,
// command results) — the /copy output target.
func (m *model) lastTranscriptOutput() (string, bool) {
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind == "toolout" {
			if s := strings.TrimSpace(stripANSI(m.lines[i].body)); s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// copyTargetText resolves a copy target to clipboard text. Unknown
// targets are rejected (no silent fallthrough to reply).
func (m *model) copyTargetText(target string) (string, bool, string) {
	switch target {
	case copyTargetPrompt:
		if txt := strings.TrimSpace(m.agent.LastUserText()); txt != "" {
			return txt, true, "your last prompt"
		}
		return "", false, "no prompt found yet"
	case copyTargetOut:
		if txt, ok := m.lastTranscriptOutput(); ok {
			return txt, true, "last command output"
		}
		return "", false, "no command output yet"
	case copyTargetReply, "":
		if txt, ok := m.lastTranscriptText(); ok {
			return txt, true, "last reply"
		}
		return "", false, "nothing to copy yet — send a prompt first"
	default:
		return "", false, "unknown target \"" + target + "\" (use reply | prompt | output)"
	}
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
		return fmt.Sprintf("copied %d bytes to clipboard", len(text)), nil
	}
	if os.Getenv("SSH_TTY") != "" && len(text) <= osc52Limit {
		enc := base64.StdEncoding.EncodeToString([]byte(text))
		return "copied via terminal (OSC 52)", tea.Printf("\x1b]52;c;%s\x07", enc)
	}
	return "clipboard unavailable — on Linux install xclip (X11) or wl-clipboard (Wayland)", nil
}

// copyLast handles ctrl+y and /copy [reply|prompt|output]. With no arg it
// copies the last reply (falling back to prompt, then output); ctrl+y is
// always the fast path to the reply.
func (m model) copyLast(arg string) (tea.Model, tea.Cmd) {
	target := strings.ToLower(strings.TrimSpace(arg))
	if target == "" {
		target = copyTargetReply
	}
	txt, ok, what := m.copyTargetText(target)
	if !ok {
		m.lines = append(m.lines, line{kind: "error", body: "copy: " + what})
		return m, nil
	}
	resp, cmd := copyResult(txt)
	m.lines = append(m.lines, line{kind: "info", body: what + " · " + resp})
	return m, cmd
}
