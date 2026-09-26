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
			// models often paste code WITHOUT fences; catch and collapse it too
			if strings.TrimSpace(s.text) != "" {
				out = append(out, renderProseSmart(s.text, width, open, &codeIdx)...)
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

// looksLikeCode heuristically detects a raw code line (no fence markers).
func looksLikeCode(ln string) bool {
	t := strings.TrimSpace(ln)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "    ") || strings.HasPrefix(ln, "\t") {
		return true
	}
	markers := []string{"{", "}", ";", "</", "<div", "<html", "<head", "<body", "<script", "<style",
		"function ", "const ", "let ", "var ", "def ", "class ", "import ", "return", "=>", "<!DOCTYPE", "<!doctype"}
	for _, m := range markers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// renderProseSmart renders a prose chunk, but if it is mostly raw unfenced
// code it collapses that chunk like a fenced block would be.
func renderProseSmart(text string, width int, open map[int]bool, codeIdx *int) []string {
	lines := strings.Split(text, "\n")
	if len(lines) < 5 {
		return []string{renderMD(text, width)}
	}
	codeLike := 0
	for _, ln := range lines {
		if looksLikeCode(ln) {
			codeLike++
		}
	}
	if codeLike*100/len(lines) < 60 {
		return []string{renderMD(text, width)}
	}
	// treat the whole chunk as one collapsed code block
	id := *codeIdx
	*codeIdx++
	if open[id] {
		return []string{codeHeaderStyle.Render(fmt.Sprintf("[code %d/%d lines · text · c collapse]", len(lines), len(lines))) +
			"\n" + renderMD("```\n"+text+"\n```", width)}
	}
	summary := firstInteresting(lines)
	return []string{codeHeaderStyle.Render(fmt.Sprintf("[code text · %d lines · %s · c expand]", len(lines), summary))}
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
