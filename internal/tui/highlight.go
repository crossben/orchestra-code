package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Diff line styles: added/removed lines get a tinted full-width band (like a
// code-review UI), hunk headers are cyan, file headers muted.
var (
	diffAddBg = lipgloss.AdaptiveColor{Light: "#E6F4EA", Dark: "#16291F"}
	diffDelBg = lipgloss.AdaptiveColor{Light: "#FBE9EB", Dark: "#2E1719"}

	diffAddSty  = lipgloss.NewStyle().Foreground(green).Background(diffAddBg)
	diffDelSty  = lipgloss.NewStyle().Foreground(red).Background(diffDelBg)
	diffHunkSty = lipgloss.NewStyle().Foreground(accent2)
	diffMetaSty = lipgloss.NewStyle().Foreground(gray).Bold(true)
)

// highlightDiff colours a unified diff for display, padding changed lines to
// width so their background forms a band (width <= 0 disables padding).
func highlightDiff(src string, width int) string {
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\t", "    ")
		band := func(st lipgloss.Style) string {
			if pad := width - lipgloss.Width(l); pad > 0 {
				l += strings.Repeat(" ", pad)
			}
			return st.Render(l)
		}
		switch {
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "),
			strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "index "),
			strings.HasPrefix(l, "new file mode"), strings.HasPrefix(l, "deleted file mode"),
			strings.HasPrefix(l, "rename "), strings.HasPrefix(l, "similarity "):
			lines[i] = diffMetaSty.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = diffHunkSty.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = band(diffAddSty)
		case strings.HasPrefix(l, "-"):
			lines[i] = band(diffDelSty)
		default:
			lines[i] = l
		}
	}
	return strings.Join(lines, "\n")
}
