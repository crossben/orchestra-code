package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// col is one table column: a fixed width (0 = take the remaining space).
type col struct {
	title string
	width int
}

// fit pads or truncates s to exactly w terminal cells. Widths are measured on
// the rendered text, so styled (ANSI-coloured) cells line up — fmt's %-10s
// counts escape bytes and skews every column after a coloured one.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// table lays out rows of pre-styled cells under a header, sized to width.
// The one zero-width column absorbs whatever space the others leave.
type table struct {
	cols  []col
	width int
}

func (t table) widths() []int {
	ws := make([]int, len(t.cols))
	used, flex := 0, -1
	for i, c := range t.cols {
		ws[i] = c.width
		if c.width == 0 {
			flex = i
		}
		used += c.width + 1
	}
	if flex >= 0 {
		ws[flex] = max(t.width-used, 8)
	}
	return ws
}

func (t table) header() string {
	ws := t.widths()
	cells := make([]string, len(t.cols))
	for i, c := range t.cols {
		cells[i] = fit(c.title, ws[i])
	}
	return headSty.Render(strings.Join(cells, " "))
}

func (t table) row(cells ...string) string {
	ws := t.widths()
	out := make([]string, len(cells))
	for i, c := range cells {
		if i < len(ws) {
			out[i] = fit(c, ws[i])
		}
	}
	return strings.Join(out, " ")
}

// listWindow returns the [start, end) slice of n rows to show in h lines so
// that sel stays visible.
func listWindow(n, sel, h int) (int, int) {
	if h < 1 {
		h = 1
	}
	if n <= h {
		return 0, n
	}
	start := sel - h/2
	if start < 0 {
		start = 0
	}
	if start+h > n {
		start = n - h
	}
	return start, start + h
}

// ansiTruncate shortens a (possibly styled) string to w cells.
func ansiTruncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// truncLeft keeps the end of a plain string (the useful part of a path).
func truncLeft(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w < 2 {
		return string(r[len(r)-w:])
	}
	return "…" + string(r[len(r)-w+1:])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
