package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette. Adaptive pairs so the TUI stays legible on a light or a dark
// terminal without a theme switch.
var (
	accentColor = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#d2a8ff"}
	barColor    = lipgloss.AdaptiveColor{Light: "#2da44e", Dark: "#2ea043"}
	lineColor   = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#3d444d"}
	pickColor   = lipgloss.AdaptiveColor{Light: "#ddf4ff", Dark: "#253040"}
	okColor     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
	warnColor   = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
	errColor    = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	ruleStyle  = lipgloss.NewStyle().Foreground(lineColor)

	appStyle = lipgloss.NewStyle().Padding(1, 2)

	// Tabs: the active one is filled, the rest recede.
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Padding(0, 2).Foreground(lipgloss.Color("231")).Background(accentColor)
	tabInactiveStyle = lipgloss.NewStyle().Faint(true).Padding(0, 2)

	// Tables.
	headStyle = lipgloss.NewStyle().Faint(true).Bold(true)
	rowStyle  = lipgloss.NewStyle()
	pickStyle = lipgloss.NewStyle().Background(pickColor).Bold(true)

	badgeStyle = lipgloss.NewStyle().Foreground(accentColor)
	labelStyle = lipgloss.NewStyle().Faint(true)

	okStyle   = lipgloss.NewStyle().Foreground(okColor)
	warnStyle = lipgloss.NewStyle().Foreground(warnColor)
	errStyle  = lipgloss.NewStyle().Foreground(errColor)

	// Chips: a row of mutually exclusive choices, the active one filled.
	chipStyle       = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	chipActiveStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("231")).Background(accentColor)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lineColor).
			Padding(0, 1)

	tileStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lineColor).
			Padding(0, 2)
	tileValueStyle = lipgloss.NewStyle().Bold(true)
)

// section renders a section heading followed by a rule filling the rest of the
// line, which separates the page's blocks without spending a whole row on a
// border.
func section(title string, width int) string {
	head := titleStyle.Render(title)
	rest := width - lipgloss.Width(head) - 1
	if rest < 1 {
		return head
	}
	return head + " " + ruleStyle.Render(strings.Repeat("─", rest))
}

// tile renders one headline number: a quiet label above a bold value.
func tile(label, value string) string {
	return tileStyle.Render(dimStyle.Render(label) + "\n" + tileValueStyle.Render(value))
}

// bar renders a proportion bar so relative magnitude reads at a glance.
func bar(v, max int64, width int) string {
	if max <= 0 || width <= 0 {
		return ""
	}
	n := int(float64(v) / float64(max) * float64(width))
	if n < 1 && v > 0 {
		n = 1
	}
	if n > width {
		n = width
	}
	return lipgloss.NewStyle().Foreground(barColor).Render(strings.Repeat("▇", n))
}
