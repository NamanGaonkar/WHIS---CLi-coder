package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// menuPanelStyle width is set dynamically at render time (full-width).
	menuPanelStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cyber).Padding(1, 2)
	menuTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0F19")).Background(cyan).Padding(0, 1)
	menuRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	menuRowActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true)
	menuRowDisabled      = lipgloss.NewStyle().Foreground(dimGray)
	menuHintStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	menuCursorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true)
	menuNavStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	menuRowDisabledStyle = menuRowDisabled

	splashTagStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E879F9"))
	splashHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	splashReadyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#34D399"))

	workingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Italic(true)
	codeHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Background(lipgloss.Color("#1F2937")).Padding(0, 1)

	doneStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0F19")).Background(lipgloss.Color("#34D399")).Padding(0, 1)
	doneTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
)
