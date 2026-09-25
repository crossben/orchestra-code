package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// header is the wordmark + tab pills, the project context on the right, and a
// rule underneath.
func (m Model) header() string {
	title := titleSty.Render("⬡ ORCHESTRA")
	var tabs []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if tab(i) == tabChat {
			switch m.cstate {
			case chatRunning:
				label += " " + spinnerFrames[m.frame%len(spinnerFrames)]
			case chatReviewing:
				label += " ●"
			}
		}
		if tab(i) == tabChanges {
			if n := len(m.changeIdx()); n > 0 {
				label += fmt.Sprintf(" %d", n)
			}
		}
		if tab(i) == m.active {
			tabs = append(tabs, tabOn.Render(label))
		} else {
			tabs = append(tabs, tabOff.Render(label))
		}
	}
	left := title + "  " + strings.Join(tabs, "")

	ctx := dimSty.Render(m.repoName)
	switch {
	case m.branch != "":
		ctx += dimSty.Render(" on ") + youSty.Render("⎇ "+m.branch)
	case !m.inRepo:
		ctx += dimSty.Render(" · plain folder")
	}
	line := left
	if gap := m.width - lipgloss.Width(left) - lipgloss.Width(ctx) - 1; gap >= 2 {
		line = left + strings.Repeat(" ", gap) + ctx
	}
	return ansiTruncate(line, m.width) + "\n" + faintSty.Render(strings.Repeat("─", max(m.width, 1)))
}

// statusBar: mode pill, context, key hints, and a transient status or the
// run timer on the right.
func (m Model) statusBar() string {
	var mode string
	switch {
	case m.cstate == chatRunning:
		mode = pill("RUNNING", amber)
	case m.cstate == chatReviewing:
		mode = pill("REVIEW", pillBg)
	default:
		mode = pill("READY", accent2)
	}

	hints := renderHints(m.hints())

	right := ""
	switch {
	case m.status != "" && time.Since(m.statusAt) < 6*time.Second:
		right = dimSty.Render(m.status)
	case m.cstate == chatRunning && m.run != nil:
		right = warnSty.Render(fmtElapsed(time.Since(m.run.start)))
	}

	left := mode + " " + hints
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		return ansiTruncate(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// hints are the keys that do something right now, as "key label" pairs.
func (m Model) hints() []string {
	if m.browsing {
		return append(m.browse.hints(), "tab switch")
	}
	switch m.active {
	case tabChat:
		switch m.cstate {
		case chatRunning:
			return []string{"esc cancel", "pgup/pgdn scroll", "tab switch"}
		case chatReviewing:
			return append(m.rv.hints(), "tab switch")
		}
		return []string{"enter send", "ctrl+j newline", "pgup/pgdn scroll", "tab switch", "ctrl+c quit"}
	case tabChanges, tabHistory:
		return []string{"↑↓ select", "enter open", "r refresh", "tab switch", "q quit"}
	case tabAgents:
		return []string{"p probe", "r refresh", "tab switch", "q quit"}
	}
	return []string{"r refresh", "tab switch", "q quit"}
}

func renderHints(hs []string) string {
	out := make([]string, len(hs))
	for i, h := range hs {
		k, label, _ := strings.Cut(h, " ")
		out[i] = keySty.Render(k) + " " + dimSty.Render(label)
	}
	return strings.Join(out, dimSty.Render("  "))
}

func fmtElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// padLines forces s to exactly n lines of at most w cells, so a view can never
// wrap or leave stale rows from the previous frame.
func padLines(s string, n, w int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansiTruncate(l, w)
		}
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// emptyState is a centred hint box for tabs with nothing to show yet.
func emptyState(title, hint string, w, h int) string {
	b := box("", headSty.Render(title)+"\n"+dimSty.Render(hint), min(max(lipgloss.Width(hint)+6, 40), w), faint)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, b)
}
