// Package ui centralizes Orchestra's terminal styling: colors, the banner,
// spinners, and diff rendering. Everything is TTY-aware — when stdout is not a
// terminal (piped, CI) or NO_COLOR is set, output degrades to plain text so
// scripts and tests stay clean.
package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Enabled reports whether rich output (color + animation) should be used.
var Enabled = detectEnabled()

func detectEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	// Rich output only when stdout is a character device (a real terminal),
	// not a pipe or file — keeps scripts and tests clean.
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Palette — a cyan→magenta gradient feel with semantic accents.
var (
	accent  = lipgloss.Color("#7C3AED") // violet
	accent2 = lipgloss.Color("#06B6D4") // cyan
	green   = lipgloss.Color("#22C55E")
	red     = lipgloss.Color("#EF4444")
	yellow  = lipgloss.Color("#EAB308")
	gray    = lipgloss.Color("#6B7280")

	styAccent  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	styAccent2 = lipgloss.NewStyle().Foreground(accent2)
	stySuccess = lipgloss.NewStyle().Foreground(green).Bold(true)
	styDanger  = lipgloss.NewStyle().Foreground(red).Bold(true)
	styWarn    = lipgloss.NewStyle().Foreground(yellow)
	styDim     = lipgloss.NewStyle().Foreground(gray)
	styHeading = lipgloss.NewStyle().Foreground(accent2).Bold(true)
)

// render applies a style only when rich output is enabled.
func render(s lipgloss.Style, text string) string {
	if !Enabled {
		return text
	}
	return s.Render(text)
}

// Accent styles text in the primary accent color.
func Accent(s string) string { return render(styAccent, s) }

// Accent2 styles text in the secondary accent color.
func Accent2(s string) string { return render(styAccent2, s) }

// Success / Danger / Warn / Dim / Heading are semantic helpers.
func Success(s string) string { return render(stySuccess, s) }
func Danger(s string) string  { return render(styDanger, s) }
func Warn(s string) string    { return render(styWarn, s) }
func Dim(s string) string     { return render(styDim, s) }
func Heading(s string) string { return render(styHeading, s) }

// Agent styles an agent name.
func Agent(name string) string {
	return render(lipgloss.NewStyle().Foreground(accent2).Bold(true), name)
}

// Rule returns a horizontal divider of the given width.
func Rule(width int) string {
	return Dim(strings.Repeat("─", width))
}

// Diff colorizes a unified diff: additions green, deletions red, hunk headers
// cyan, file headers bold, everything else dim.
func Diff(diff string) string {
	if !Enabled {
		return diff
	}
	var b strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			b.WriteString(lipgloss.NewStyle().Bold(true).Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(lipgloss.NewStyle().Foreground(green).Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(lipgloss.NewStyle().Foreground(red).Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(styAccent2.Render(line))
		case strings.HasPrefix(line, "diff "):
			b.WriteString(styDim.Render(line))
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Println prints a line (thin wrapper for symmetry with future needs).
func Println(a ...any) { fmt.Println(a...) }
