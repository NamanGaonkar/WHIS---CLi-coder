package tui

import (
	"fmt"
	"strings"

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
		{label: "help", hint: "all commands & keys", value: "/help"},
	}
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
// multiple configured keys stay distinguishable without ever exposing them.
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
	case "anthropic", "openai", "deepseek", "ollama-cloud":
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
		o.items = append(o.items, menuItem{label: s.Title, hint: s.Model, value: s.ID})
	}
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

// current returns the highlighted item.
func (o *overlay) current() menuItem {
	if o.cursor < 0 || o.cursor >= len(o.items) {
		return menuItem{}
	}
	return o.items[o.cursor]
}

// view renders the overlay panel content (full terminal width; the caller
// adds the border). Rows are label + metrics hint, cursor left.
func (o overlay) view(width int) string {
	var b strings.Builder
	b.WriteString(menuTitleStyle.Render(" "+o.title+" ") + "\n\n")
	for i, it := range o.items {
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
		b.WriteString(cursor + style.Render(label) + "  " + menuHintStyle.Render(it.hint) + "\n")
	}
	nav := "\nup/down move · enter select · esc back"
	switch o.mode {
	case overlaySlashMenu:
		nav = "\nup/down move · enter run · esc back"
	case overlaySessions:
		nav = "\nenter resume · esc back"
	case overlayKeyInput:
		nav = "\npaste key · enter save · esc back"
	}
	b.WriteString(menuNavStyle.Render(nav))
	return b.String()
}
