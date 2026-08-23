package interact

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// Adaptive colors that auto-detect dark/light terminal background.
var (
	colorGreen  = compat.AdaptiveColor{Light: lipgloss.Color("#00873E"), Dark: lipgloss.Color("#3BDB80")}
	colorYellow = compat.AdaptiveColor{Light: lipgloss.Color("#B8860B"), Dark: lipgloss.Color("#FFD700")}
	colorRed    = compat.AdaptiveColor{Light: lipgloss.Color("#CC0000"), Dark: lipgloss.Color("#FF5555")}
	colorBlue   = compat.AdaptiveColor{Light: lipgloss.Color("#0057B8"), Dark: lipgloss.Color("#66B2FF")}
	colorMuted  = compat.AdaptiveColor{Light: lipgloss.Color("#666666"), Dark: lipgloss.Color("#999999")}
	colorBold   = compat.AdaptiveColor{Light: lipgloss.Color("#1A1A1A"), Dark: lipgloss.Color("#F0F0F0")}
)

// Theme holds lipgloss styles for consistent terminal output.
type Theme struct {
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Info    lipgloss.Style
	Muted   lipgloss.Style
	Header  lipgloss.Style
	Key     lipgloss.Style
	Value   lipgloss.Style
	Bold    lipgloss.Style
}

// NewTheme returns a Theme that adapts to the terminal's color scheme.
func NewTheme() *Theme {
	return &Theme{
		Success: lipgloss.NewStyle().Foreground(colorGreen),
		Warning: lipgloss.NewStyle().Foreground(colorYellow),
		Error:   lipgloss.NewStyle().Foreground(colorRed),
		Info:    lipgloss.NewStyle().Foreground(colorBlue),
		Muted:   lipgloss.NewStyle().Foreground(colorMuted),
		Header:  lipgloss.NewStyle().Foreground(colorBold).Bold(true).Underline(true),
		Key:     lipgloss.NewStyle().Foreground(colorMuted),
		Value:   lipgloss.NewStyle().Foreground(colorBold).Bold(true),
		Bold:    lipgloss.NewStyle().Foreground(colorBold).Bold(true),
	}
}
