package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"whis/internal/agent"
	"whis/internal/config"
	"whis/internal/provider"
	"whis/internal/session"
)

// overlayMode is the current menu/overlay state of the TUI.
type overlayMode int

const (
	overlayNone      overlayMode = iota
	overlaySlashMenu             // "/" → command options
	overlayProvider              // /model → pick provider
	overlayModel                 // provider chosen → pick model
	overlayKeyInput              // provider needs a key → masked input
	overlaySessions              // /sessions → pick session to resume
	overlayWorkMode              // /mode → pick work mode (plan/ask/auto)
	overlayHelp                  // /help → scrollable help panel (not chat)
	overlayThemes                // /themes → pick TUI palette
)

// menuItem is one selectable row in an overlay.
type menuItem struct {
	label    string // left text
	hint     string // dimmed right text (metrics / state)
	value    string // slug / command / session id
	disabled bool
	selected bool
}

// overlay holds all transient menu state.
type overlay struct {
	mode     overlayMode
	items    []menuItem
	cursor   int
	provider string // chosen provider for overlayModel / overlayKeyInput
	title    string
	lines    []string // free-text rows for overlayHelp
	scroll   int      // help panel scroll offset
}

// openHelp shows the help text in a scrollable panel instead of dumping it
// into the chat transcript.
func (o *overlay) openHelp(text string) {
	o.mode = overlayHelp
	o.title = "HELP · COMMANDS & KEYS"
	o.cursor = 0
	o.scroll = 0
	o.items = nil
	o.lines = strings.Split(text, "\n")
}

// openSlashMenu shows the "/" command options.
func (o *overlay) openSlashMenu() {
	o.mode = overlaySlashMenu
	o.title = "COMMANDS"
	o.cursor = 0
	o.items = []menuItem{
		{label: "mode", hint: "plan · ask · auto (how the agent acts)", value: "/mode"},
		{label: "model", hint: "switch provider or model", value: "/model"},
		{label: "provider", hint: "manage API keys & providers", value: "/provider"},
		{label: "sessions", hint: "resume a session from this folder", value: "/sessions"},
		{label: "task", hint: "run an isolated subagent task", value: "/task"},
		{label: "undo", hint: "roll back last change", value: "/undo"},
		{label: "init", hint: "(re)generate WHIS.md", value: "/init"},
		{label: "help", hint: "commands & keys in a panel", value: "@help"},
		{label: "themes", hint: "switch the TUI color palette", value: "/themes"},
	}
}

// openThemes lists the palettes; the active one gets an IN USE marker.
func (o *overlay) openThemes(current int) {
	o.mode = overlayThemes
	o.title = "SELECT THEME"
	o.cursor = 0
	o.items = nil
	for i, th := range themes {
		hint := themeHint(th)
		selected := false
		if i == current {
			hint = "IN USE · " + hint
			selected = true
		}
		o.items = append(o.items, menuItem{label: th.name, hint: hint, value: th.name, selected: selected})
	}
	for i, it := range o.items {
		if it.selected {
			o.cursor = i
		}
	}
}

// themeHint renders the accent in the hint column so the menu previews the
// palette (non-ASCII escape text is stripped by the row renderer).
func themeHint(th themeDef) string {
	return menuRowStyle.Foreground(lipgloss.Color(th.ember)).Render("accent ") +
		menuRowStyle.Foreground(lipgloss.Color(th.amber)).Render("second ") +
		menuRowStyle.Foreground(lipgloss.Color(th.dim)).Render("dim")
}

// openModeMenu lists work modes; current gets an IN USE marker.
func (o *overlay) openModeMenu(current string) {
	o.mode = overlayWorkMode
	o.title = "SELECT MODE"
	o.cursor = 0
	o.items = nil
	for _, m := range agent.Modes() {
		hint := m.Desc
		selected := false
		if m.Name == current {
			hint = "IN USE · " + m.Desc
			selected = true
		}
		o.items = append(o.items, menuItem{label: m.Name, hint: hint, value: m.Name, selected: selected})
	}
	for i, it := range o.items {
		if it.selected {
			o.cursor = i
		}
	}
}

// openProviderMenu lists providers; hints show masked key fingerprints so
// multiple configured keys stay distinguishable. Selecting a provider with
// a saved key opens the model list; "edit key" re-enters the key form so
// existing keys can be replaced (opencode parity).
func (o *overlay) openProviderMenu(keys map[string]string) {
	o.mode = overlayProvider
	o.title = "SELECT PROVIDER"
	o.cursor = 0
	o.items = nil
	for _, p := range provider.Providers() {
		hint := "key missing"
		if !p.NeedsKey {
			hint = "local · no key needed"
		} else if k := keys[p.Name]; k != "" {
			hint = "key saved · " + config.Mask(k)
		}
		o.items = append(o.items, menuItem{
			label: p.Label, hint: hint, value: p.Name,
			disabled: false, // selectable even without a key: prompts for it
		})
	}
	o.items = append(o.items, menuItem{
		label: "edit / re-enter a provider key", hint: "replace a saved key",
		value: "@editkey",
	})
}

// openProviderEditMenu lists key-holding providers for key replacement.
// Picking one jumps straight into the masked key form (existing key shows
// masked in the hint so you know what you are replacing).
func (o *overlay) openProviderEditMenu(keys map[string]string) {
	o.mode = overlayProvider
	o.title = "EDIT PROVIDER KEY"
	o.cursor = 0
	o.items = nil
	for _, p := range provider.Providers() {
		if !p.NeedsKey {
			continue
		}
		hint := "no key yet"
		if k := keys[p.Name]; k != "" {
			hint = "replace " + config.Mask(k)
		}
		o.items = append(o.items, menuItem{label: p.Label, hint: hint, value: "@set:" + p.Name})
	}
}

// openModelMenu lists live models for the chosen provider. Catalog entries
// render instantly; live /models fetch results replace them with real ids
// and metrics. The active model row shows usage stats for this session.
func (o *overlay) openModelMenu(prov string, keys map[string]string, current string, st Status) {
	o.mode = overlayModel
	o.title = "SELECT MODEL · " + strings.ToUpper(prov)
	o.provider = prov
	o.cursor = 0
	o.items = nil

	inUse := func(slug string) (string, bool) {
		if slug != "" && slug == current {
			return fmt.Sprintf("IN USE · %s tok · $%.4f", commify(st.In+st.Out), st.Cost), true
		}
		return "", false
	}

	fill := func(models []provider.ModelInfo) {
		o.items = nil
		for _, m := range models {
			slug := m.Slug
			if slug == "" {
				slug = staticSlugFor(prov, m.ID)
			}
			hint, used := inUse(slug)
			if !used {
				hint = modelHint(prov, m)
			}
			o.items = append(o.items, menuItem{
				label: m.ID, hint: hint, value: slug, selected: used,
			})
		}
		if prov == "openrouter" {
			o.items = append(o.items, menuItem{label: "custom model id…", hint: "type vendor/name", value: "@custom"})
		}
		for i, it := range o.items {
			if it.selected {
				o.cursor = i
			}
		}
	}

	switch prov {
	case "ollama":
		models, err := provider.FetchModels("ollama", "")
		if err != nil || len(models) == 0 {
			o.items = append(o.items, menuItem{
				label: "no local models found", hint: errHint(err, "start: ollama serve"), disabled: true,
			})
			return
		}
		fill(models)
	default:
		key := keys[prov]
		if key == "" && pNeedsKey(prov) {
			o.items = append(o.items, menuItem{label: "add API key for " + prov + "…", hint: "required", value: "@key"})
			return
		}
		// static catalog entries first so the menu is never empty
		for _, slug := range provider.ModelsForProvider(prov) {
			hint, used := inUse(slug)
			if !used {
				hint = "cloud"
			}
			o.items = append(o.items, menuItem{label: slug, hint: hint, value: slug, selected: used})
		}
		// live fetch replaces with real ids + metrics
		models, err := provider.FetchModels(prov, key)
		if err == nil && len(models) > 0 {
			fill(models)
			provider.SyncCatalogWithPricing(prov, models)
		}
	}
}

// staticSlugFor maps a provider model id to a whis slug, reusing catalog
// slugs when the id matches a known wire model.
func staticSlugFor(prov, id string) string {
	for _, slug := range provider.ModelsForProvider(prov) {
		if _, wire, _, _ := provider.Get(slug); wire == id {
			return slug
		}
	}
	switch prov {
	case "openrouter":
		return "or:" + id
	case "ollama-cloud":
		return "ollama-cloud:" + id
	}
	return prov + ":" + id
}

// modelHint builds the metrics hint column per provider.
func modelHint(prov string, m provider.ModelInfo) string {
	switch prov {
	case "ollama":
		if m.In > 0 { // size GiB packed into In by FetchModels
			return fmt.Sprintf("local · %.1f GiB", m.In)
		}
		return "local"
	case "openrouter":
		parts := []string{}
		if m.Context > 0 {
			parts = append(parts, commifyK(m.Context)+" ctx")
		}
		if m.In > 0 || m.Out > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f/$%.2f per M", m.In, m.Out))
		}
		if len(parts) == 0 {
			return "cloud"
		}
		return strings.Join(parts, " · ")
	case "anthropic", "openai", "deepseek", "ollama-cloud", "gemini", "xai", "mistral",
		"moonshot", "qwen", "zai", "minimax", "groq":
		if m.Context > 0 {
			return "cloud · " + commifyK(m.Context) + " ctx"
		}
		return "cloud"
	}
	return ""
}

func errHint(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// commifyK renders 128000 as "128k".
func commifyK(n int) string {
	if n >= 1000 && n%1000 == 0 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func pNeedsKey(prov string) bool {
	for _, p := range provider.Providers() {
		if p.Name == prov {
			return p.NeedsKey
		}
	}
	return true
}

// openKeyInput shows the masked key form for a provider.
func (o *overlay) openKeyInput(prov string) {
	o.mode = overlayKeyInput
	o.title = "API KEY · " + strings.ToUpper(prov)
	o.provider = prov
}

// scrollHelp shifts the help panel scroll offset (d clamps internally).
func (o *overlay) scrollHelp(d int) {
	max := len(o.lines) - 1
	o.scroll += d
	if o.scroll < 0 {
		o.scroll = 0
	}
	if o.scroll > max {
		o.scroll = max
	}
}

// openSessions lists sessions for the current workspace.
func (o *overlay) openSessions(root string) {
	o.mode = overlaySessions
	o.title = "SESSIONS · THIS FOLDER"
	o.cursor = 0
	o.items = nil
	sums := session.ListFor(root)
	if len(sums) == 0 {
		o.items = append(o.items, menuItem{label: "no sessions in this folder yet", hint: "start chatting", disabled: true})
		return
	}
	for _, s := range sums {
		hint := s.Model
		if s.Tasks > 0 {
			hint = fmt.Sprintf("%d task%s · %s", s.Tasks, pluralS(s.Tasks), s.Model)
		}
		o.items = append(o.items, menuItem{label: s.Title, hint: hint, value: s.ID})
	}
}

// pluralS is the one-letter pluralizer (1 task / 2 tasks).
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// move shifts the cursor to the next selectable row; returns true if moved.
func (o *overlay) move(d int) bool {
	if len(o.items) == 0 {
		return false
	}
	for range len(o.items) {
		o.cursor += d
		if o.cursor < 0 {
			o.cursor = len(o.items) - 1
		}
		if o.cursor >= len(o.items) {
			o.cursor = 0
		}
		if !o.items[o.cursor].disabled {
			return true
		}
	}
	return false
}

// bodyRows returns the rendered body height (rows inside the panel border)
// for the current overlay. inRows is the textarea's row count, needed when
// the key-entry form is embedded. Item menus WINDOW their rows (scroll);
// the cap of 40 bounds the panel size on huge model lists.
func (o *overlay) bodyRows(inRows int) int {
	if o.mode == overlayNone {
		return 0
	}
	n := 0
	if o.mode == overlayHelp {
		n = len(o.lines) + 4 // title + blank + lines + nav
	} else {
		n = 3 + len(o.items) // title + blank + items + nav line
		if len(o.items) > 0 {
			n++ // blank before the nav row
		}
		if n > 40 {
			n = 40 // windowed: title + blank + 36 items + blank + nav
		}
	}
	if o.mode == overlayKeyInput {
		n += 2 + inRows // blank + blank + the embedded input view
	}
	if n < 1 {
		n = 1
	}
	return clampInt(n, 1, 40)
}

// itemWindow returns the [lo,hi) slice of items visible given the body-row
// budget, keeping the cursor on screen. rows = maxRows - 4 fixed rows
// (title + blank + blank + nav).
func (o *overlay) itemWindow(rows int) (int, int) {
	total := len(o.items)
	if rows < 1 {
		rows = 1
	}
	if total <= rows {
		return 0, total
	}
	lo := o.cursor - rows + 1 // keep the cursor visible (bottom-biased)
	if lo > total-rows {
		lo = total - rows
	}
	if lo < 0 {
		lo = 0
	}
	return lo, lo + rows
}

// current returns the highlighted item.
func (o *overlay) current() menuItem {
	if o.cursor < 0 || o.cursor >= len(o.items) {
		return menuItem{}
	}
	return o.items[o.cursor]
}

// view renders the overlay panel content (full terminal width; the caller
// adds the border). maxRows is the row budget so help can window its lines.
func (o overlay) view(width, maxRows int) string {
	var b strings.Builder
	if o.mode == overlayHelp {
		b.WriteString(menuTitleStyle.Render(" "+o.title+" ") + "\n\n")
		room := maxRows - 4 // title + blank + nav
		if room < 1 {
			room = 1
		}
		start := o.scroll
		if start > len(o.lines)-room {
			start = len(o.lines) - room
		}
		if start < 0 {
			start = 0
		}
		end := start + room
		if end > len(o.lines) {
			end = len(o.lines)
		}
		for _, ln := range o.lines[start:end] {
			b.WriteString(menuRowStyle.Render(ln) + "\n")
		}
		if len(o.lines) > room {
			b.WriteString(menuNavStyle.Render(fmt.Sprintf("lines %d-%d of %d · up/down or wheel · esc close",
				start+1, end, len(o.lines))))
		} else {
			b.WriteString(menuNavStyle.Render("esc close"))
		}
		return b.String()
	}
	b.WriteString(menuTitleStyle.Render(" "+o.title+" ") + "\n\n")
	// window long lists (big live /models catalogs); rows = budget minus the
	// fixed rows (title + blank + blank + nav). Scroll indicator in the nav.
	rows := maxRows - 4
	if rows < 1 {
		rows = 1
	}
	lo, hi := o.itemWindow(rows)
	windowed := hi-lo < len(o.items)
	for i := lo; i < hi; i++ {
		it := o.items[i]
		cursor := "  "
		style := menuRowStyle
		if i == o.cursor {
			cursor = menuCursorStyle.Render("> ")
			style = menuRowActiveStyle
		}
		if it.disabled {
			style = menuRowDisabledStyle
		}
		label := it.label
		if max := width - 24; max > 20 && len(label) > max {
			label = label[:max-1] + "…"
		}
		// hints may embed raw ANSI color previews; never render that text.
		hint := it.hint
		if strings.Contains(hint, "\x1b[") {
			hint = stripANSI(hint)
		}
		b.WriteString(cursor + style.Render(label) + "  " + menuHintStyle.Render(hint) + "\n")
	}
	nav := "\nup/down move · enter select · esc back"
	if windowed {
		nav = fmt.Sprintf("\n%d-%d of %d · up/down scrolls", lo+1, hi, len(o.items))
	}
	switch o.mode {
	case overlaySlashMenu:
		nav = "\nup/down move · enter run · esc back"
	case overlayThemes:
		nav = "\nenter apply · esc keep current"
	case overlaySessions:
		nav = "\nenter resume · esc back"
	case overlayKeyInput:
		nav = "\npaste key · enter save · esc back"
	}
	b.WriteString(menuNavStyle.Render(nav))
	return b.String()
}
