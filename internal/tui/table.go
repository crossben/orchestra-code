package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// col is one table column: a fixed width (0 = take the remaining space).
// drop > 0 marks a column that may be hidden when the window is too narrow;
// the highest drop goes first.
type col struct {
	title string
	width int
	drop  int
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
// The one zero-width column absorbs whatever space the others leave;
// droppable columns are hidden, highest drop first, until it keeps minFlex.
type table struct {
	cols  []col
	width int
}

const minFlex = 12

// widths returns each column's width; a hidden column gets -1.
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
	for used+minFlex > t.width {
		worst := -1
		for i, c := range t.cols {
			if ws[i] >= 0 && c.drop > 0 && (worst < 0 || c.drop > t.cols[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		used -= t.cols[worst].width + 1
		ws[worst] = -1
	}
	if flex >= 0 {
		ws[flex] = max(t.width-used, 8)
	}
	return ws
}

func (t table) header() string {
	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = c.title
	}
	return headSty.Render(t.row(titles...))
}

func (t table) row(cells ...string) string {
	ws := t.widths()
	out := make([]string, 0, len(cells))
	for i, c := range cells {
		if i < len(ws) && ws[i] >= 0 {
			out = append(out, fit(c, ws[i]))
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
