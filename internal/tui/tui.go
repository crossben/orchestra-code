// Package tui is Orchestra's dashboard — a Bubble Tea full-screen view over your
// agents (with live health probing), run history, and benchmark results, plus a
// Chat tab. Chat hands the terminal to the full supervised engine (via tea.Exec)
// so you get the same route → run → validate → accept/reject flow as the shell,
// then returns to the dashboard. It observes state (config + SQLite memory) and,
// on chat, drives the real engine — it does not reimplement chat in the render loop.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/config"
	"github.com/crossben/orchestra/internal/engine"
	"github.com/crossben/orchestra/internal/gitutil"
	"github.com/crossben/orchestra/internal/memory"
	"github.com/crossben/orchestra/internal/router"
	"github.com/crossben/orchestra/internal/scheduler"
	"github.com/crossben/orchestra/internal/validate"
)

type tab int

const (
	tabAgents tab = iota
	tabHistory
	tabBench
	tabChat
)

var tabNames = []string{"Agents", "History", "Benchmarks", "Chat"}

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
	promptSty = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

// Deps bundles everything the dashboard needs (read-only data + the engine
// dependencies for the Chat tab).
type Deps struct {
	Ctx          context.Context
	Cfg          *config.Config
	Reg          *agent.Registry
	Mem          *memory.Store
	Dir          string
	Router       *router.Router
	RoutingOn    bool
	Stages       []validate.Stage
	MaxRetries   int
	Timeout      time.Duration
	Principles   string
	DefaultAgent string
}

// Model is the dashboard state.
type Model struct {
	d             Deps
	active        tab
	width, height int

	runs    []memory.Run
	benches []memory.BenchRow

	probing bool
	probed  map[string]agent.ProbeResult

	input  string // chat input buffer
	status string
}

// New builds the dashboard model, loading initial data from memory.
func New(d Deps) Model {
	m := Model{
		d:      d,
		width:  80,
		height: 24,
		probed: map[string]agent.ProbeResult{},
	}
	m.reload()
	return m
}

func (m *Model) reload() {
	if m.d.Mem == nil {
		return
	}
	if r, err := m.d.Mem.Recent(m.d.Dir, 100); err == nil {
		m.runs = r
	}
	if b, err := m.d.Mem.RecentBenchmarks(m.d.Dir, 100); err == nil {
		m.benches = b
	}
}

func (m Model) Init() tea.Cmd { return nil }

// --- messages ---

type probeDoneMsg struct{ results map[string]agent.ProbeResult }
type chatDoneMsg struct{ err error }

func (m Model) probeCmd() tea.Cmd {
	agents := m.d.Reg.All()
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
	case chatDoneMsg:
		m.reload()
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = "done — history updated"
		}
	case tea.KeyMsg:
		if m.active == tabChat {
			return m.updateChat(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "tab", "right", "l":
			m.active = (m.active + 1) % 4
		case "shift+tab", "left", "h":
			m.active = (m.active + 3) % 4
		case "1":
			m.active = tabAgents
		case "2":
			m.active = tabHistory
		case "3":
			m.active = tabBench
		case "4":
			m.active = tabChat
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

// updateChat handles keys while the Chat tab's input is focused.
func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.active = tabAgents
	case tea.KeyEnter:
		return m.submitChat()
	case tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	}
	return m, nil
}

// submitChat hands the terminal to the supervised engine for one turn.
func (m Model) submitChat() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	if text == "" {
		return m, nil
	}
	m.input = ""
	m.status = "running…"
	ex := &engineExec{
		ctx: m.d.Ctx, dir: m.d.Dir, msg: text,
		reg: m.d.Reg, current: m.d.DefaultAgent,
		router: m.d.Router, routingOn: m.d.RoutingOn,
		stages: m.d.Stages, maxRetries: m.d.MaxRetries,
		timeout: m.d.Timeout, principles: m.d.Principles, mem: m.d.Mem,
	}
	return m, tea.Exec(ex, func(err error) tea.Msg { return chatDoneMsg{err: err} })
}

func (m Model) View() string {
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
	case tabChat:
		b.WriteString(m.chatView())
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
	switch m.active {
	case tabAgents:
		keys = "tab: switch • p: probe agents • r: refresh • q: quit"
	case tabChat:
		keys = "type a message • enter: send • esc: back • ctrl+c: quit"
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
	for _, a := range m.d.Reg.All() {
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
		if name == m.d.DefaultAgent {
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
		return dimSty.Render("no run history yet — try the Chat tab, `orchestra run`, or `orchestra do`")
	}
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-17s %-10s %-10s %-4s %s", "WHEN", "AGENT", "OUTCOME", "ATT", "TASK")) + "\n")
	for _, r := range m.runs {
		if m.linesShown(&b) {
			break
		}
		outcome := dimSty.Render(r.Outcome)
		switch r.Outcome {
		case "accepted":
			outcome = okSty.Render("accepted")
		case "rejected":
			outcome = badSty.Render("rejected")
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
		if m.linesShown(&b) {
			break
		}
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

func (m Model) chatView() string {
	var b strings.Builder
	mode := "agent: " + m.d.DefaultAgent
	if m.d.RoutingOn && m.d.Router != nil {
		mode = "AI routing on"
	}
	b.WriteString(headSty.Render("Chat") + dimSty.Render("   ("+mode+")") + "\n\n")
	b.WriteString(promptSty.Render("› ") + m.input + dimSty.Render("▏") + "\n\n")
	b.WriteString(dimSty.Render("press enter to send this to the agent — it runs the full supervised loop\n" +
		"(route → run → validate → accept/reject) on the terminal, then returns here.\n"))

	// A little recent activity for context.
	if len(m.runs) > 0 {
		b.WriteString("\n" + dimSty.Render("recent:") + "\n")
		for i, r := range m.runs {
			if i >= 5 {
				break
			}
			b.WriteString(dimSty.Render(fmt.Sprintf("  %s  %-8s  %s  %s\n",
				r.Time.Local().Format("15:04:05"), r.Agent, r.Outcome, truncate(firstLine(r.Prompt), 40))))
		}
	}
	return b.String()
}

// engineExec runs one supervised chat turn on the released terminal.
type engineExec struct {
	ctx        context.Context
	dir        string
	msg        string
	reg        *agent.Registry
	current    string
	router     *router.Router
	routingOn  bool
	stages     []validate.Stage
	maxRetries int
	timeout    time.Duration
	principles string
	mem        *memory.Store
}

func (e *engineExec) SetStdin(io.Reader)  {}
func (e *engineExec) SetStdout(io.Writer) {}
func (e *engineExec) SetStderr(io.Writer) {}

func (e *engineExec) Run() error {
	if !gitutil.IsRepo(e.dir) {
		fmt.Println("orchestra: not a git repository — chat needs one for the supervised loop")
		return waitEnter()
	}
	if clean, _ := gitutil.IsClean(e.dir); !clean {
		fmt.Println("orchestra: working tree has uncommitted changes — commit/stash first")
		return waitEnter()
	}

	in := bufio.NewReader(os.Stdin)
	ag, ok := e.reg.Get(e.current)

	// Route if enabled.
	if e.router != nil && e.routingOn {
		d := e.router.Route(e.ctx, e.msg, e.dir)
		if d.IsQuestion() {
			if ans, err := e.router.Answer(e.ctx, e.msg, e.dir, e.timeout); err == nil {
				fmt.Println(strings.TrimSpace(ans))
			}
			return waitEnter()
		}
		if a, found := e.reg.Get(d.Agent); found {
			ag, ok = a, true
			fmt.Printf("↳ routing to %s (%s)\n", d.Agent, d.Reason)
		}
	}
	if !ok {
		fmt.Printf("orchestra: agent %q not available\n", e.current)
		return waitEnter()
	}

	_, err := engine.Execute(e.ctx, in, engine.Options{
		Agent:          ag,
		Prompt:         e.msg,
		Dir:            e.dir,
		Stages:         e.stages,
		MaxRetries:     e.maxRetries,
		Timeout:        e.timeout,
		CommitOnAccept: true,
		Memory:         e.mem,
		Principles:     e.principles,
	})
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}
	_ = waitEnter()
	return nil
}

func waitEnter() error {
	fmt.Print("\n\033[2m(press enter to return to the dashboard)\033[0m ")
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
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
