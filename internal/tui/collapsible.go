package tui

import (
	"fmt"
	"strings"
)

// seg is one chunk of an assistant message: prose or a fenced code block.
type seg struct {
	code bool
	lang string
	text string
}

// splitFences splits markdown into prose and fenced-code segments so code can
// be collapsed independently of the prose around it.
func splitFences(md string) []seg {
	var segs []seg
	lines := strings.Split(md, "\n")
	var prose strings.Builder
	var code []string
	lang := ""
	inCode := false

	flushProse := func() {
		if prose.Len() > 0 {
			segs = append(segs, seg{text: strings.Trim(prose.String(), "\n")})
			prose.Reset()
		}
	}
	flushCode := func() {
		if len(code) > 0 {
			segs = append(segs, seg{code: true, lang: lang, text: strings.Join(code, "\n")})
			code = nil
			lang = ""
		}
	}

	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") {
			if !inCode {
				flushProse()
				inCode = true
				lang = strings.TrimPrefix(t, "```")
			} else {
				inCode = false
				flushCode()
			}
			continue
		}
		if inCode {
			code = append(code, ln)
		} else {
			prose.WriteString(ln + "\n")
		}
	}
	flushProse()
	flushCode()
	return segs
}

// renderCollapsible renders an assistant message: prose shown, code blocks
// collapsed to a one-line summary unless the user expanded them. open maps a
// segment index to its expanded state.
func renderCollapsible(md string, width int, open map[int]bool) string {
	segs := splitFences(md)
	var out []string
	codeIdx := 0
	for _, s := range segs {
		if !s.code {
			if strings.TrimSpace(s.text) != "" {
				out = append(out, renderMD(s.text, width))
			}
			continue
		}
		id := codeIdx
		codeIdx++
		lines := strings.Split(s.text, "\n")
		if open[id] {
			out = append(out, codeHeaderStyle.Render(fmt.Sprintf("[code %d/%d lines · %s · c collapse]", len(lines), len(lines), langLabel(s.lang)))+"\n"+renderMD("```"+s.lang+"\n"+s.text+"\n```", width))
			continue
		}
		summary := firstInteresting(lines)
		out = append(out, codeHeaderStyle.Render(
			fmt.Sprintf("[code %s · %d lines · %s · c expand]", langLabel(s.lang), len(lines), summary)))
	}
	return strings.Join(out, "\n\n")
}

// langLabel normalizes a fenced-code language tag for display.
func langLabel(l string) string {
	if l == "" {
		return "text"
	}
	return l
}

// firstInteresting picks a short summary line from a code block (first
// meaningful line, trimmed).
func firstInteresting(lines []string) string {
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || t == "{" || t == "}" {
			continue
		}
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") {
			return t
		}
		if len(t) > 4 && !strings.ContainsAny(t, "{}") {
			return t
		}
	}
	return ""
}
