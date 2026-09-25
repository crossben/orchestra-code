package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/agent"
)

func outcomeCell(o string) string {
	switch o {
	case "accepted":
		return okSty.Render("✓ accepted")
	case "rejected":
		return badSty.Render("✗ rejected")
	case "failed":
		return badSty.Render("! failed")
	case "cancelled":
		return warnSty.Render("↺ cancelled")
	case "no-change":
		return dimSty.Render("○ no change")
	}
	return dimSty.Render(o)
}

func whenCell(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	if y, mo, d := time.Now().Date(); t.Year() == y && t.Month() == mo && t.Day() == d {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 02 15:04")
}

// selRow marks the selected row with a pointer and accent colour.
func selRow(selected bool, row string) string {
	if selected {
		return selSty.Render("▸ ") + row
	}
	return "  " + row
}

func (m Model) changesView() string {
	idx := m.changeIdx()
	h := m.bodyHeight()
	if len(idx) == 0 {
		return emptyState("No changes yet", "Accepted and rejected diffs from Chat show up here — and stay after a restart.", m.width, h)
	}
	t := table{width: m.width - 2, cols: []col{{"WHEN", 12}, {"AGENT", 12}, {"OUTCOME", 12}, {"FILES", 5}, {"LINES", 12}, {"TASK", 0}}}
	var b strings.Builder
	b.WriteString("  " + t.header() + "\n")
	start, end := listWindow(len(idx), m.changeSel, h-1)
	for n := start; n < end; n++ {
		i := idx[n]
		r, st := m.runs[i], m.runStats[i]
		b.WriteString(selRow(n == m.changeSel, t.row(
			dimSty.Render(whenCell(r.Time)), youSty.Render(r.Agent), outcomeCell(r.Outcome),
			fmt.Sprint(st.files), counts(st.added, st.removed), firstLine(r.Prompt))) + "\n")
	}
	return b.String()
}

func (m Model) historyView() string {
	h := m.bodyHeight()
	if len(m.runs) == 0 {
		return emptyState("No run history yet", "Runs from Chat, orchestra run and orchestra do are listed here.", m.width, h)
	}
	t := table{width: m.width - 2, cols: []col{{"WHEN", 12}, {"AGENT", 12}, {"OUTCOME", 12}, {"TRIES", 5}, {"CHECKS", 7}, {"LINES", 12}, {"TASK", 0}}}
	var b strings.Builder
	b.WriteString("  " + t.header() + "\n")
	start, end := listWindow(len(m.runs), m.histSel, h-1)
	for i := start; i < end; i++ {
		r, st := m.runs[i], m.runStats[i]
		checks := dimSty.Render("–")
		if r.Passed {
			checks = okSty.Render("✓")
		}
		lines := dimSty.Render("–")
		if st.files > 0 {
			lines = counts(st.added, st.removed)
		}
		b.WriteString(selRow(i == m.histSel, t.row(
			dimSty.Render(whenCell(r.Time)), youSty.Render(r.Agent), outcomeCell(r.Outcome),
			fmt.Sprint(r.Attempts), checks, lines, firstLine(r.Prompt))) + "\n")
	}
	return b.String()
}

func (m Model) agentsView() string {
	t := table{width: m.width - 2, cols: []col{{"AGENT", 14}, {"INSTALLED", 10}, {"LIVE PROBE", 26}, {"RUNS", 5}, {"ACCEPTED", 9}, {"LAST USED", 13}, {"CAN", 0}}}
	var b strings.Builder
	b.WriteString("  " + t.header() + "\n")
	for _, a := range m.d.Reg.All() {
		installed := okSty.Render("✓ yes")
		if a.Health() != nil {
			installed = dimSty.Render("✗ no")
		}
		probe := dimSty.Render("press p")
		if m.probing {
			probe = warnSty.Render(spin(m.frame) + " probing…")
		}
		if res, ok := m.probed[a.Name()]; ok {
			if res.OK {
				probe = okSty.Render("✓ works")
			} else {
				probe = badSty.Render("✗ " + res.Detail)
			}
		}
		name := youSty.Render(a.Name())
		if a.Name() == m.d.DefaultAgent {
			name += warnSty.Render(" ★")
		}
		st := m.stats[a.Name()]
		runs, rate, last := dimSty.Render("–"), dimSty.Render("–"), dimSty.Render("never")
		if st.Runs > 0 {
			runs = fmt.Sprint(st.Runs)
			rate = fmt.Sprintf("%d%%", st.Accepted*100/st.Runs)
			last = dimSty.Render(whenCell(st.LastUsed))
		}
		b.WriteString("  " + t.row(name, installed, probe, runs, rate, last, dimSty.Render(caps(a))) + "\n")
	}
	b.WriteString("\n  " + dimSty.Render("★ default agent  ·  ACCEPTED = share of runs whose changes you kept  ·  p probes whether each agent can actually run"))
	return b.String()
}

func (m Model) benchView() string {
	h := m.bodyHeight()
	if len(m.benches) == 0 {
		return emptyState("No benchmarks yet", "Run  orchestra benchmark \"<task>\"  to race every agent on one task.", m.width, h)
	}
	t := table{width: m.width - 2, cols: []col{{"WHEN", 12}, {"AGENT", 12}, {"WON", 4}, {"VALID", 6}, {"TIME", 8}, {"RETRIES", 8}, {"LINES", 12}, {"TASK", 0}}}
	var b strings.Builder
	b.WriteString("  " + t.header() + "\n")
	for i, r := range m.benches {
		if i >= h-1 {
			break
		}
		won := ""
		if r.Won {
			won = warnSty.Render("★")
		}
		valid := badSty.Render("✗")
		if r.Valid {
			valid = okSty.Render("✓")
		}
		b.WriteString("  " + t.row(dimSty.Render(whenCell(r.Time)), youSty.Render(r.Agent), won, valid,
			r.Duration.Round(time.Second/10).String(), fmt.Sprint(r.Retries), counts(r.Added, r.Removed), firstLine(r.Task)) + "\n")
	}
	return b.String()
}

func (m Model) logsView() string {
	h := m.bodyHeight()
	if len(m.logs) == 0 {
		return emptyState("Nothing logged yet", "Routing, runs, retries, probes and errors from this session appear here.", m.width, h)
	}
	t := table{width: m.width - 2, cols: []col{{"TIME", 9}, {"KIND", 6}, {"MESSAGE", 0}}}
	var b strings.Builder
	b.WriteString("  " + t.header() + "\n")
	start := max(len(m.logs)-(h-1), 0)
	for _, e := range m.logs[start:] {
		kind := dimSty.Render(e.kind)
		switch e.kind {
		case "error":
			kind = badSty.Render(e.kind)
		case "turn":
			kind = okSty.Render(e.kind)
		case "route":
			kind = youSty.Render(e.kind)
		case "probe":
			kind = warnSty.Render(e.kind)
		}
		b.WriteString("  " + t.row(dimSty.Render(e.time.Local().Format("15:04:05")), kind, e.text) + "\n")
	}
	return b.String()
}

func caps(a agent.Agent) string {
	out := make([]string, 0, len(a.Capabilities()))
	for _, c := range a.Capabilities() {
		out = append(out, string(c))
	}
	return strings.Join(out, ", ")
}
