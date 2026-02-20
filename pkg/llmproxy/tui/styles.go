// Package tui provides a terminal-based management interface for CLIProxyAPI.
package tui

import "github.com/charmbracelet/lipgloss"

// Color palette
var (
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#6366F1") // indigo
	colorSuccess   = lipgloss.Color("#22C55E") // green
	colorWarning   = lipgloss.Color("#EAB308") // yellow
	colorError     = lipgloss.Color("#EF4444") // red
	colorInfo      = lipgloss.Color("#3B82F6") // blue
	colorMuted     = lipgloss.Color("#6B7280") // gray
	colorBg        = lipgloss.Color("#1E1E2E") // dark bg
	colorSurface   = lipgloss.Color("#313244") // slightly lighter
	colorText      = lipgloss.Color("#CDD6F4") // light text
	colorSubtext   = lipgloss.Color("#A6ADC8") // dimmer text
	colorBorder    = lipgloss.Color("#45475A") // border
	colorHighlight = lipgloss.Color("#F5C2E7") // pink highlight
)

// Tab bar styles
var (
	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorPrimary).
			Padding(0, 2)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(colorSubtext).
				Background(colorSurface).
				Padding(0, 2)

	tabBarStyle = lipgloss.NewStyle().
			Background(colorSurface).
			PaddingLeft(1).
			PaddingBottom(0)
)

// Content styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorHighlight).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Italic(true)

	labelStyle = lipgloss.NewStyle().
			Foreground(colorInfo).
			Bold(true).
			Width(24)

	valueStyle = lipgloss.NewStyle().
			Foreground(colorText)

	sectionStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	warningStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Background(colorSurface).
			PaddingLeft(1).
			PaddingRight(1)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
)

// Log level styles
var (
	logDebugStyle = lipgloss.NewStyle().Foreground(colorMuted)
	logInfoStyle  = lipgloss.NewStyle().Foreground(colorInfo)
	logWarnStyle  = lipgloss.NewStyle().Foreground(colorWarning)
	logErrorStyle = lipgloss.NewStyle().Foreground(colorError)
)

// Table styles
var (
	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorHighlight).
				BorderBottom(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(colorBorder)

	tableCellStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingRight(2)

	tableSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(colorPrimary).
				Bold(true)
)

func logLevelStyle(level string) lipgloss.Style {
	switch level {
	case "debug":
		return logDebugStyle
	case "info":
		return logInfoStyle
	case "warn", "warning":
		return logWarnStyle
	case "error", "fatal", "panic":
		return logErrorStyle
	default:
		return logInfoStyle
	}
}
