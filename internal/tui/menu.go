package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	fetchRun int      // async /models fetch run guard (stale results dropped)
	// type-to-search: printable keys typed while a searchable menu is open
	// filter the rows (matched against label+hint, case-insensitive). The
	// full row set lives in allItems; items is rebuilt on each keystroke so
	// cursor moves / click hit-testing / wheel keep working unchanged.
	filter     string
	filterable bool
	allItems   []menuItem
}

// setItems stores the full row set and re-applies any active filter.
func (o *overlay) setItems(items []menuItem) {
	o.allItems = items
	o.refilter()
}

// refilter rebuilds the visible rows from allItems per the active filter.
func (o *overlay) refilter() {
	src := o.allItems
	if src == nil {
		src = o.items // menus that predate setItems
	}
	if !o.filterable || o.filter == "" {
		o.items = src
		if o.cursor >= len(o.items) {
			o.cursor = len(o.items) - 1
		}
		if o.cursor < 0 {
			o.cursor = 0
		}
		return
	}
	q := strings.ToLower(o.filter)
	out := make([]menuItem, 0, len(src))
	for _, it := range src {
		if strings.Contains(strings.ToLower(stripANSI(it.label)), q) ||
			strings.Contains(strings.ToLower(stripANSI(it.hint)), q) {
			out = append(out, it)
		}
	}
	o.items = out
	o.cursor = 0
}

// typeFilter appends a typed rune to the search filter.
func (o *overlay) typeFilter(r rune) { o.filter += string(r); o.refilter() }

// backspaceFilter drops the last filter character.
func (o *overlay) backspaceFilter() {
	if o.filter == "" {
		return
	}
	r := []rune(o.filter)
	o.filter = string(r[:len(r)-1])
	o.refilter()
}

// clearFilter resets the search and restores the full row set.
func (o *overlay) clearFilter() { o.filter = ""; o.refilter() }

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
	o.filterable, o.filter = true, "" // type-to-search
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
	o.allItems = o.items // snapshot full set for the filter
}

// openProviderEditMenu lists key-holding providers for key replacement.
// Picking one jumps straight into the masked key form (existing key shows
// masked in the hint so you know what you are replacing).
func (o *overlay) openProviderEditMenu(keys map[string]string) {
	o.mode = overlayProvider
	o.title = "EDIT PROVIDER KEY"
	o.cursor = 0
	o.filterable, o.filter = true, "" // type-to-search
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

// openModelMenu lists models for the chosen provider. Static catalog rows
// render INSTANTLY; the live /models fetch runs ASYNC (menuModelsLoaded
// replaces the rows when it lands). A blocking fetch here stalled the UI
// for up to 8s and desynced mouse release events — the root cause of menu
// self-clicks right after opening. The active model row shows usage stats.
func (o *overlay) openModelMenu(prov string, keys map[string]string, current string, st Status) tea.Cmd {
	o.mode = overlayModel
	o.title = "SELECT MODEL · " + strings.ToUpper(prov)
	o.provider = prov
	o.cursor = 0
	o.filterable, o.filter = true, "" // type-to-search
	o.items = nil

	// static catalog entries first so the menu is never empty
	for _, slug := range provider.ModelsForProvider(prov) {
		hint, used := o.inUseHint(slug, current, st)
		if !used {
			hint = "cloud"
		}
		o.items = append(o.items, menuItem{label: slug, hint: hint, value: slug, selected: used})
	}
	for i, it := range o.items {
		if it.selected {
			o.cursor = i
		}
	}
	key := keys[prov]
	if key == "" && pNeedsKey(prov) {
		// no key: static rows only (the UI routes keyless providers to the
		// key form before reaching here; belt-and-suspenders)
		return nil
	}
	o.items = append(o.items, menuItem{
		label: "fetching live models…", hint: "one moment", value: "@fetching", disabled: true,
	})
	o.allItems = o.items // snapshot full set for the filter
	// async fetch OFF the UI thread; run number guards against stale results
	o.fetchRun++
	run := o.fetchRun
	return func() tea.Msg {
		models, err := provider.FetchModels(prov, key)
		return menuModelsLoaded{run: run, prov: prov, models: models, err: err}
	}
}

// inUseHint renders the usage hint for the session's active model.
func (o *overlay) inUseHint(slug, current string, st Status) (string, bool) {
	if slug != "" && slug == current {
		return fmt.Sprintf("IN USE · %s tok · $%.4f", commify(st.In+st.Out), st.Cost), true
	}
	return "", false
}

// fillModelItems replaces the rows with live-fetched models (real ids,
// context/pricing metrics, custom row for openrouter).
func (o *overlay) fillModelItems(prov, current string, st Status, models []provider.ModelInfo) {
	o.items = nil
	for _, m := range models {
		slug := m.Slug
		if slug == "" {
			slug = staticSlugFor(prov, m.ID)
		}
		hint, used := o.inUseHint(slug, current, st)
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
	o.setItems(o.items) // route through setItems so any active filter re-applies
	o.cursor = 0
	for i, it := range o.items {
		if it.selected {
			o.cursor = i
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
	// searchable menus show the live filter in the title row — no extra
	// geometry (title stays 1 row) so mouse hit-testing stays aligned.
	title := o.title
	if o.filterable {
		fl := o.filter
		if fl == "" {
			fl = "type to search…"
		}
		title = fmt.Sprintf("%s  ·  search: %s", o.title, fl)
	}
	b.WriteString(menuTitleStyle.Render(" "+title+" ") + "\n\n")
	// window long lists (big live /models catalogs); rows = budget minus the
	// fixed rows (title + blank + blank + nav). Scroll indicator in the nav.
	rows := maxRows - 4
	if rows < 1 {
		rows = 1
	}
	lo, hi := o.itemWindow(rows)
	windowed := hi-lo < len(o.items)
	if len(o.items) == 0 && o.filter != "" {
		b.WriteString(menuRowDisabledStyle.Render("no matches for \""+o.filter+"\"") + "\n")
	}
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
