// Package tui is Orchestra's read-only dashboard — a Bubble Tea full-screen view
// over your agents (with live health probing), run history, and benchmark
// results. It observes state (config + SQLite memory); it does not launch work.
// Live in-run monitoring is a future addition that needs the engine to emit
// events.
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/config"
	"github.com/crossben/orchestra/internal/memory"
	"github.com/crossben/orchestra/internal/scheduler"
)

type tab int

const (
	tabAgents tab = iota
	tabHistory
	tabBench
)

var tabNames = []string{"Agents", "History", "Benchmarks"}

// palette (cyan → violet accents, semantic green/red).
var (
	accent  = lipgloss.Color("#7C3AED")
	accent2 = lipgloss.Color("#06B6D4")
	green   = lipgloss.Color("#22C55E")
	red     = lipgloss.Color("#EF4444")
	gray    = lipgloss.Color("#6B7280")

	titleSty  = lipgloss.NewStyle().Foreground(accent2).Bold(true)
	tabOn     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(accent).Bold(true).Padding(0, 2)
	tabOff    = lipgloss.NewStyle().Foreground(gray).Padding(0, 2)
	headSty   = lipgloss.NewStyle().Foreground(accent2).Bold(true)
	dimSty    = lipgloss.NewStyle().Foreground(gray)
	okSty     = lipgloss.NewStyle().Foreground(green).Bold(true)
	badSty    = lipgloss.NewStyle().Foreground(red).Bold(true)
	footerSty = lipgloss.NewStyle().Foreground(gray)
)

// Model is the dashboard state.
type Model struct {
	cfg *config.Config
	reg *agent.Registry
	mem *memory.Store
	dir string

	active        tab
	width, height int

	runs    []memory.Run
	benches []memory.BenchRow

	probing bool
	probed  map[string]agent.ProbeResult

	status string
}

// New builds the dashboard model, loading initial data from memory.
func New(cfg *config.Config, reg *agent.Registry, mem *memory.Store, dir string) Model {
	m := Model{
		cfg:    cfg,
		reg:    reg,
		mem:    mem,
		dir:    dir,
		width:  80, // sensible defaults until the first WindowSizeMsg arrives
		height: 24,
		probed: map[string]agent.ProbeResult{},
	}
	m.reload()
	return m
}

func (m *Model) reload() {
	if m.mem == nil {
		return
	}
	if r, err := m.mem.Recent(m.dir, 100); err == nil {
		m.runs = r
	}
	if b, err := m.mem.RecentBenchmarks(m.dir, 100); err == nil {
		m.benches = b
	}
}

func (m Model) Init() tea.Cmd { return nil }

// --- messages ---

type probeDoneMsg struct{ results map[string]agent.ProbeResult }

func (m Model) probeCmd() tea.Cmd {
	agents := m.reg.All()
	return func() tea.Msg {
		results := map[string]agent.ProbeResult{}
		var mu sync.Mutex
		scheduler.Bounded(context.Background(), len(agents), len(agents), func(i int) error {
			a := agents[i]
			var res agent.ProbeResult
			switch {
			case a.Health() != nil:
				res = agent.ProbeResult{OK: false, Detail: "not installed"}
			default:
				if p, ok := a.(agent.Prober); ok {
					res = p.Probe(context.Background(), 45*time.Second)
				} else {
					res = agent.ProbeResult{OK: false, Detail: "not probeable"}
				}
			}
			mu.Lock()
			results[a.Name()] = res
			mu.Unlock()
			return nil
		})
		return probeDoneMsg{results: results}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case probeDoneMsg:
		m.probed = msg.results
		m.probing = false
		m.status = "probe complete"
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "tab", "right", "l":
			m.active = (m.active + 1) % 3
		case "shift+tab", "left", "h":
			m.active = (m.active + 2) % 3
		case "1":
			m.active = tabAgents
		case "2":
			m.active = tabHistory
		case "3":
			m.active = tabBench
		case "r":
			m.reload()
			m.status = "refreshed"
		case "p":
			if m.active == tabAgents && !m.probing {
				m.probing = true
				m.status = "probing agents…"
				return m, m.probeCmd()
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	switch m.active {
	case tabAgents:
		b.WriteString(m.agentsView())
	case tabHistory:
		b.WriteString(m.historyView())
	case tabBench:
		b.WriteString(m.benchView())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header() string {
	title := titleSty.Render("⬡ ORCHESTRA")
	var tabs []string
	for i, name := range tabNames {
		if tab(i) == m.active {
			tabs = append(tabs, tabOn.Render(fmt.Sprintf("%d %s", i+1, name)))
		} else {
			tabs = append(tabs, tabOff.Render(fmt.Sprintf("%d %s", i+1, name)))
		}
	}
	line := title + "   " + strings.Join(tabs, " ")
	return line + "\n" + dimSty.Render(strings.Repeat("─", min(m.width, 80)))
}

func (m Model) footer() string {
	keys := "tab: switch • r: refresh • q: quit"
	if m.active == tabAgents {
		keys = "tab: switch • p: probe agents • r: refresh • q: quit"
	}
	status := ""
	if m.status != "" {
		status = "   " + m.status
	}
	return footerSty.Render(strings.Repeat("─", min(m.width, 80)) + "\n" + keys + status)
}

func (m Model) agentsView() string {
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-12s %-14s %-22s %s", "AGENT", "INSTALLED", "PROBE", "CAPABILITIES")) + "\n")
	for _, a := range m.reg.All() {
		installed := okSty.Render("✓")
		if a.Health() != nil {
			installed = dimSty.Render("✗")
		}
		probe := dimSty.Render("— (press p)")
		if m.probing {
			probe = dimSty.Render("…")
		}
		if res, ok := m.probed[a.Name()]; ok {
			if res.OK {
				probe = okSty.Render("✓ works")
			} else {
				probe = badSty.Render("✗ " + truncate(res.Detail, 30))
			}
		}
		name := a.Name()
		if name == m.cfg.DefaultAgent {
			name += "*"
		}
		b.WriteString(fmt.Sprintf("%-12s %-14s %-22s %s\n",
			name, installed, probe, dimSty.Render(caps(a))))
	}
	b.WriteString("\n" + dimSty.Render("* default agent   ·   press p to live-probe whether each agent can actually run"))
	return b.String()
}

func (m Model) historyView() string {
	if len(m.runs) == 0 {
		return dimSty.Render("no run history yet — try `orchestra run` or `orchestra do`")
	}
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-17s %-10s %-10s %-4s %s", "WHEN", "AGENT", "OUTCOME", "ATT", "TASK")) + "\n")
	for _, r := range m.runs {
		if m.linesShown(&b) {
			break
		}
		outcome := r.Outcome
		switch r.Outcome {
		case "accepted":
			outcome = okSty.Render("accepted")
		case "rejected":
			outcome = badSty.Render("rejected")
		default:
			outcome = dimSty.Render(r.Outcome)
		}
		b.WriteString(fmt.Sprintf("%-17s %-10s %-10s %-4d %s\n",
			r.Time.Local().Format("01-02 15:04:05"), r.Agent, outcome, r.Attempts, truncate(firstLine(r.Prompt), m.taskWidth())))
	}
	return b.String()
}

func (m Model) benchView() string {
	if len(m.benches) == 0 {
		return dimSty.Render("no benchmarks yet — try `orchestra benchmark \"<task>\"`")
	}
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-17s %-10s %-6s %-6s %-8s %s", "WHEN", "AGENT", "WON", "VALID", "TIME", "TASK")) + "\n")
	for _, r := range m.benches {
		won := dimSty.Render("")
		if r.Won {
			won = okSty.Render("★")
		}
		valid := badSty.Render("✗")
		if r.Valid {
			valid = okSty.Render("✓")
		}
		b.WriteString(fmt.Sprintf("%-17s %-10s %-6s %-6s %-8s %s\n",
			r.Time.Local().Format("01-02 15:04:05"), r.Agent, won, valid,
			r.Duration.Round(time.Second/10).String(), truncate(firstLine(r.Task), m.taskWidth())))
	}
	return b.String()
}

// --- helpers ---

func (m Model) taskWidth() int {
	w := m.width - 52
	if w < 20 {
		return 20
	}
	if w > 60 {
		return 60
	}
	return w
}

// linesShown is a tiny guard to avoid overflowing the view height.
func (m Model) linesShown(b *strings.Builder) bool {
	if m.height <= 0 {
		return false
	}
	return strings.Count(b.String(), "\n") >= m.height-8
}

func caps(a agent.Agent) string {
	out := make([]string, 0, len(a.Capabilities()))
	for _, c := range a.Capabilities() {
		out = append(out, string(c))
	}
	return strings.Join(out, ",")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		if n < 1 {
			return ""
		}
		return string(r[:n-1]) + "…"
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
