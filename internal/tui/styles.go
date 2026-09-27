package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Ember palette: hot orange + amber yellow on pitch black.
var (
	ember   = lipgloss.Color("#FF9F1C") // hot orange (primary accent)
	amber   = lipgloss.Color("#FFD166") // warm yellow (secondary accent)
	green   = lipgloss.Color("#7AE582") // success (kept, warm-tinted)
	red     = lipgloss.Color("#FF6B6B") // errors
	dimGray = lipgloss.Color("#6B5B4E") // warm dim gray
	deep    = lipgloss.Color("#1A120B") // near-black warm surface
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(ember).Padding(0, 1)
	badgeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#0D0805")).Background(amber).Padding(0, 1).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(dimGray)
	okStyle     = lipgloss.NewStyle().Foreground(green)
	errStyle    = lipgloss.NewStyle().Foreground(red).Bold(true)
	warnStyle   = lipgloss.NewStyle().Foreground(amber)
	planStyle   = lipgloss.NewStyle().Foreground(dimGray).Italic(true).Padding(0, 1)
	toolStyle   = lipgloss.NewStyle().Foreground(ember)
	diffAdd     = lipgloss.NewStyle().Foreground(green)
	diffDel     = lipgloss.NewStyle().Foreground(red)
	inputStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ember).Padding(0, 1)
	barStyle    = lipgloss.NewStyle().Foreground(amber).Background(deep).Padding(0, 1)
)

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

// Overlay menu styles (orange/yellow).
var (
	menuPanelStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ember).Padding(1, 2)
	menuTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(amber).Padding(0, 1)
	menuRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#E8DCC8"))
	menuRowActiveStyle   = lipgloss.NewStyle().Foreground(amber).Bold(true)
	menuRowDisabled      = lipgloss.NewStyle().Foreground(dimGray)
	menuHintStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#8A7A66"))
	menuCursorStyle      = lipgloss.NewStyle().Foreground(ember).Bold(true)
	menuNavStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#5E5245"))
	menuRowDisabledStyle = menuRowDisabled

	splashTagStyle   = lipgloss.NewStyle().Bold(true).Foreground(amber)
	splashHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8A7A66"))
	splashReadyStyle = lipgloss.NewStyle().Bold(true).Foreground(green)

	workingStyle    = lipgloss.NewStyle().Foreground(amber).Italic(true)
	codeHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#C9B8A3")).Background(lipgloss.Color("#241A10")).Padding(0, 1)

	doneStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0D0805")).Background(green).Padding(0, 1)
	doneTextStyle = lipgloss.NewStyle().Foreground(green)

	// scrollbar thumb/track for the chat viewport (ember on warm black).
	scrollThumbStyle = lipgloss.NewStyle().Foreground(ember)
	scrollTrackStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#33261A"))
)
