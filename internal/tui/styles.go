package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Palette vars are package-level so applyTheme can repaint them live
// (/themes command, opencode parity). Styles are rebuilt from the palette.
var (
	ember   lipgloss.Color // primary accent
	amber   lipgloss.Color // secondary accent
	green   lipgloss.Color // success
	red     lipgloss.Color // errors
	dimGray lipgloss.Color // dim text
	deep    lipgloss.Color // surface (status bar bg)
	paleRow lipgloss.Color // menu row text
	black   lipgloss.Color // pure black (input box backing)
)

var (
	headerStyle lipgloss.Style
	badgeStyle  lipgloss.Style
	dimStyle    lipgloss.Style
	okStyle     lipgloss.Style
	errStyle    lipgloss.Style
	warnStyle   lipgloss.Style
	planStyle   lipgloss.Style
	toolStyle   lipgloss.Style
	diffAdd     lipgloss.Style
	diffDel     lipgloss.Style
	inputStyle  lipgloss.Style // border + FULL black backing (entire box)
	barStyle    lipgloss.Style // status bar: separate black band with own padding
)

var (
	menuPanelStyle       lipgloss.Style
	menuTitleStyle       lipgloss.Style
	menuRowStyle         lipgloss.Style
	menuRowActiveStyle   lipgloss.Style
	menuRowDisabled      lipgloss.Style
	menuRowDisabledStyle lipgloss.Style
	menuHintStyle        lipgloss.Style
	menuCursorStyle      lipgloss.Style
	menuNavStyle         lipgloss.Style

	splashTagStyle   lipgloss.Style
	splashHintStyle  lipgloss.Style
	splashReadyStyle lipgloss.Style

	workingStyle    lipgloss.Style
	codeHeaderStyle lipgloss.Style

	doneStyle     lipgloss.Style
	doneTextStyle lipgloss.Style

	scrollThumbStyle lipgloss.Style
	scrollTrackStyle lipgloss.Style
)

// themeDef is one named palette.
type themeDef struct {
	name                     string
	ember, amber, green, red string
	dim, deep, paleRow       string
}

// themes: opencode-style switchable palettes. ember is the default.
var themes = []themeDef{
	{name: "ember", ember: "#FF9F1C", amber: "#FFD166", green: "#7AE582", red: "#FF6B6B", dim: "#6B5B4E", deep: "#1A120B", paleRow: "#E8DCC8"},
	{name: "dim", ember: "#A8A8A8", amber: "#CFCFCF", green: "#9BC4AB", red: "#D08787", dim: "#5C5C5C", deep: "#161616", paleRow: "#BDBDBD"},
	{name: "mono", ember: "#F2F2F2", amber: "#D9D9D9", green: "#C8C8C8", red: "#FF9494", dim: "#7A7A7A", deep: "#111111", paleRow: "#EFEFEF"},
}

// curTheme indexes the active palette (menu marks it IN USE).
var curTheme int

// applyTheme paints every style from themes[i]; safe to call mid-session.
func applyTheme(i int) {
	if i < 0 || i >= len(themes) {
		return
	}
	curTheme = i
	t := themes[i]
	ember = lipgloss.Color(t.ember)
	amber = lipgloss.Color(t.amber)
	green = lipgloss.Color(t.green)
	red = lipgloss.Color(t.red)
	dimGray = lipgloss.Color(t.dim)
	deep = lipgloss.Color(t.deep)
	paleRow = lipgloss.Color(t.paleRow)
	black = lipgloss.Color("#000000")

	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(ember).Padding(0, 1)
	badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#0D0805")).Background(amber).Padding(0, 1).Bold(true)
	dimStyle = lipgloss.NewStyle().Foreground(dimGray)
	okStyle = lipgloss.NewStyle().Foreground(green)
	errStyle = lipgloss.NewStyle().Foreground(red).Bold(true)
	warnStyle = lipgloss.NewStyle().Foreground(amber)
	planStyle = lipgloss.NewStyle().Foreground(dimGray).Italic(true).Padding(0, 1)
	toolStyle = lipgloss.NewStyle().Foreground(ember)
	diffAdd = lipgloss.NewStyle().Foreground(green)
	diffDel = lipgloss.NewStyle().Foreground(red)
	// input box: pure black backing over border + padding + content, so the
	// ENTIRE box is black on any terminal theme (user rule).
	inputStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ember).
		BorderBackground(black).
		Background(black).
		Padding(0, 1)
	barStyle = lipgloss.NewStyle().Foreground(amber).Background(black).Padding(0, 1)

	menuPanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ember).Padding(1, 2)
	menuTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(amber).Padding(0, 1)
	menuRowStyle = lipgloss.NewStyle().Foreground(paleRow)
	menuRowActiveStyle = lipgloss.NewStyle().Foreground(amber).Bold(true)
	menuRowDisabled = lipgloss.NewStyle().Foreground(dimGray)
	menuRowDisabledStyle = menuRowDisabled
	menuHintStyle = lipgloss.NewStyle().Foreground(dimGray)
	menuCursorStyle = lipgloss.NewStyle().Foreground(ember).Bold(true)
	menuNavStyle = lipgloss.NewStyle().Foreground(dimGray)

	splashTagStyle = lipgloss.NewStyle().Bold(true).Foreground(amber)
	splashHintStyle = lipgloss.NewStyle().Foreground(dimGray)
	splashReadyStyle = lipgloss.NewStyle().Bold(true).Foreground(green)

	workingStyle = lipgloss.NewStyle().Foreground(amber).Italic(true)
	codeHeaderStyle = lipgloss.NewStyle().Foreground(paleRow).Background(deep).Padding(0, 1)

	doneStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(green).Padding(0, 1)
	doneTextStyle = lipgloss.NewStyle().Foreground(green)

	scrollThumbStyle = lipgloss.NewStyle().Foreground(ember)
	scrollTrackStyle = lipgloss.NewStyle().Foreground(deep)
}

func init() { applyTheme(0) }

// banner is the big WHIS wordmark (ANSI-shadow style) shown on startup,
// one vivid gradient color per row.
var banner = []string{
	"██╗    ██╗ ██╗  ██╗ ██╗ ███████╗",
	"██║    ██║ ██║  ██║ ██║ ██╔════╝",
	"██║ █╗ ██║ ███████║ ██║ ███████╗",
	"██║███╗██║ ██╔══██║ ██║ ╚════██║",
	"╚███╔███╔╝ ██║  ██║ ██║ ███████║",
	" ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝",
}

// bannerColors: molten gradient, deep ember -> bright flame -> pale gold.
var bannerColors = []string{"#7A3410", "#B45A16", "#E07B1F", "#FF9F1C", "#FFB84D", "#FFD166"}
