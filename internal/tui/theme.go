package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette. Adaptive colours keep every view readable on light terminals as
// well as dark ones; all styles below derive from these.
var (
	accent  = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"} // violet
	accent2 = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#22D3EE"} // cyan
	green   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	red     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	amber   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	gray    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	faint   = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"} // rules, borders
	onPill  = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#111827"} // text on filled pills
	pillBg  = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
)

var (
	titleSty  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	headSty   = lipgloss.NewStyle().Foreground(accent2).Bold(true)
	dimSty    = lipgloss.NewStyle().Foreground(gray)
	faintSty  = lipgloss.NewStyle().Foreground(faint)
	okSty     = lipgloss.NewStyle().Foreground(green).Bold(true)
	badSty    = lipgloss.NewStyle().Foreground(red).Bold(true)
	warnSty   = lipgloss.NewStyle().Foreground(amber).Bold(true)
	promptSty = lipgloss.NewStyle().Foreground(accent).Bold(true)
	youSty    = lipgloss.NewStyle().Foreground(accent2).Bold(true)
	selSty    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	addSty    = lipgloss.NewStyle().Foreground(green)
	delSty    = lipgloss.NewStyle().Foreground(red)

	tabOn  = lipgloss.NewStyle().Foreground(onPill).Background(pillBg).Bold(true).Padding(0, 1)
	tabOff = lipgloss.NewStyle().Foreground(gray).Padding(0, 1)
	keySty = lipgloss.NewStyle().Foreground(accent2)
)

// pill renders a small filled label (mode indicators, badges).
func pill(text string, bg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Foreground(onPill).Background(bg).Bold(true).Padding(0, 1).Render(text)
}

// box renders content in a rounded border sized to w (outer width).
func box(title, content string, w int, border lipgloss.TerminalColor) string {
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	if w > 4 {
		st = st.Width(w - 2)
	}
	if title != "" {
		content = headSty.Render(title) + "\n" + content
	}
	return st.Render(content)
}

// panelBox renders content in a titled rounded box of exactly w×h cells.
func panelBox(title, content string, w, h int, border lipgloss.TerminalColor) string {
	innerW, innerH := max(w-4, 1), max(h-2, 1)
	lines := strings.Split(content, "\n")
	if title != "" {
		lines = append([]string{headSty.Render(title)}, lines...)
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for i, l := range lines {
		lines[i] = fit(l, innerW)
	}
	for len(lines) < innerH {
		lines = append(lines, strings.Repeat(" ", innerW))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Render(strings.Join(lines, "\n"))
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
