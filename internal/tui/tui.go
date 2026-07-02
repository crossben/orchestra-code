// Package tui is Orchestra's dashboard — a Bubble Tea full-screen view over your
// agents (with live health probing), run history, benchmark results, and an
// in-pane Chat tab. Chat runs the agent quietly in the background (spinner while
// it works), then shows the diff for accept/reject right inside the dashboard —
// no screen flip. It reuses the engine's quiet Produce path so nothing streams
// into the render loop.
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

type chatState int

const (
	chatIdle chatState = iota
	chatRunning
	chatReviewing
)

// palette
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
	youSty    = lipgloss.NewStyle().Foreground(accent2).Bold(true)
	addSty    = lipgloss.NewStyle().Foreground(green)
	delSty    = lipgloss.NewStyle().Foreground(red)
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Deps bundles everything the dashboard needs.
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

type chatLine struct {
	role string // "you" | "agent" | "sys"
	text string
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

	// chat
	input    string
	cstate   chatState
	messages []chatLine
	pending  engine.Turn // the turn awaiting accept/reject
	frame    int         // spinner

	status string
}

// New builds the dashboard model, loading initial data from memory.
func New(d Deps) Model {
	m := Model{d: d, width: 80, height: 24, probed: map[string]agent.ProbeResult{}}
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
type turnMsg struct{ turn engine.Turn }
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

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

// produceCmd runs one chat turn quietly in the background.
func (m Model) produceCmd(text string) tea.Cmd {
	d := m.d
	return func() tea.Msg {
		if !gitutil.IsRepo(d.Dir) {
			return turnMsg{engine.Turn{Err: errNotRepo}}
		}
		if clean, _ := gitutil.IsClean(d.Dir); !clean {
			return turnMsg{engine.Turn{Err: errDirty}}
		}
		ag, ok := d.Reg.Get(d.DefaultAgent)
		if !ok {
			return turnMsg{engine.Turn{Err: fmt.Errorf("agent %q not available", d.DefaultAgent)}}
		}
		t := engine.Produce(d.Ctx, engine.Options{
			Agent:      ag,
			Prompt:     text,
			Dir:        d.Dir,
			Stages:     d.Stages,
			MaxRetries: d.MaxRetries,
			Timeout:    d.Timeout,
			Principles: d.Principles,
		})
		return turnMsg{t}
	}
}

var (
	errNotRepo = fmt.Errorf("not a git repository — chat needs one for the supervised loop")
	errDirty   = fmt.Errorf("working tree has uncommitted changes — commit/stash first")
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case probeDoneMsg:
		m.probed = msg.results
		m.probing = false
		m.status = "probe complete"
	case tickMsg:
		if m.cstate == chatRunning {
			m.frame++
			return m, tickCmd()
		}
	case turnMsg:
		return m.onTurn(msg.turn)
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

// updateChat handles keys in the Chat tab. tab/shift+tab always navigate (so you
// can leave chat while typing); ctrl+c always quits.
func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.active = (m.active + 1) % 4
		return m, nil
	case "shift+tab":
		m.active = (m.active + 3) % 4
		return m, nil
	}

	if m.cstate == chatReviewing {
		switch msg.String() {
		case "y", "Y":
			return m.accept()
		case "n", "N", "esc":
			return m.reject()
		}
		return m, nil
	}
	if m.cstate == chatRunning {
		return m, nil // busy; ignore input
	}

	// idle
	switch msg.Type {
	case tea.KeyEsc:
		m.active = tabAgents
	case tea.KeyEnter:
		return m.submitChat()
	case tea.KeyBackspace:
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	}
	return m, nil
}

func (m Model) submitChat() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	if text == "" {
		return m, nil
	}
	m.input = ""
	m.messages = append(m.messages, chatLine{"you", text})
	m.cstate = chatRunning
	m.frame = 0
	m.status = ""
	return m, tea.Batch(tickCmd(), m.produceCmd(text))
}

func (m Model) onTurn(t engine.Turn) (tea.Model, tea.Cmd) {
	switch {
	case t.Err != nil:
		m.messages = append(m.messages, chatLine{"sys", "error: " + t.Err.Error()})
		m.cstate = chatIdle
	case !t.HadChanges:
		resp := strings.TrimSpace(t.AgentText)
		if resp == "" {
			resp = "(the agent made no file changes)"
		}
		m.messages = append(m.messages, chatLine{"agent", resp})
		m.cstate = chatIdle
	default:
		m.pending = t
		m.cstate = chatReviewing
	}
	return m, nil
}

func (m Model) accept() (tea.Model, tea.Cmd) {
	last := ""
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role == "you" {
			last = m.messages[i].text
			break
		}
	}
	if err := gitutil.Commit(m.d.Dir, "orchestra: "+firstLine(last)); err != nil {
		m.messages = append(m.messages, chatLine{"sys", "commit failed: " + err.Error()})
	} else {
		m.messages = append(m.messages, chatLine{"sys", "✓ accepted & committed"})
	}
	m.cstate = chatIdle
	m.pending = engine.Turn{}
	m.reload()
	return m, nil
}

func (m Model) reject() (tea.Model, tea.Cmd) {
	if err := gitutil.Restore(m.d.Dir); err != nil {
		m.messages = append(m.messages, chatLine{"sys", "restore failed: " + err.Error()})
	} else {
		m.messages = append(m.messages, chatLine{"sys", "↺ rejected & reverted"})
	}
	m.cstate = chatIdle
	m.pending = engine.Turn{}
	return m, nil
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
	return title + "   " + strings.Join(tabs, " ") + "\n" + dimSty.Render(strings.Repeat("─", min(m.width, 80)))
}

func (m Model) footer() string {
	var keys string
	switch m.active {
	case tabAgents:
		keys = "tab: switch • p: probe • r: refresh • q: quit"
	case tabChat:
		switch m.cstate {
		case chatReviewing:
			keys = "y: accept & commit • n: reject & revert • tab: switch"
		case chatRunning:
			keys = "working… • tab: switch • ctrl+c: quit"
		default:
			keys = "type & enter: send • tab: switch • esc: leave • ctrl+c: quit"
		}
	default:
		keys = "tab: switch • r: refresh • q: quit"
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
		b.WriteString(fmt.Sprintf("%-12s %-14s %-22s %s\n", name, installed, probe, dimSty.Render(caps(a))))
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
	b.WriteString(headSty.Render("Chat") + dimSty.Render("   (agent: "+m.d.DefaultAgent+")") + "\n\n")

	// Transcript (last messages that fit).
	budget := m.height - 12
	if budget < 3 {
		budget = 3
	}
	msgs := m.messages
	if len(msgs) > budget {
		msgs = msgs[len(msgs)-budget:]
	}
	if len(msgs) == 0 {
		b.WriteString(dimSty.Render("Ask the agent to do something — it runs in the background and shows the diff\nhere for you to accept or reject. Nothing leaves this screen.\n"))
	}
	for _, msg := range msgs {
		switch msg.role {
		case "you":
			b.WriteString(youSty.Render("you › ") + truncate(firstLine(msg.text), m.width-8) + "\n")
		case "agent":
			b.WriteString(headSty.Render("agent ") + "\n" + indentWrap(msg.text, m.width-2, 6) + "\n")
		case "sys":
			b.WriteString(dimSty.Render("  · "+msg.text) + "\n")
		}
	}

	b.WriteString("\n")
	switch m.cstate {
	case chatRunning:
		b.WriteString(titleSty.Render(spinnerFrames[m.frame%len(spinnerFrames)]) + dimSty.Render(" agent is working…"))
	case chatReviewing:
		b.WriteString(m.reviewPane())
	default:
		b.WriteString(promptSty.Render("› ") + m.input + dimSty.Render("▏"))
	}
	return b.String()
}

func (m Model) reviewPane() string {
	var b strings.Builder
	// validation summary
	switch {
	case m.pending.Report.Skipped:
		b.WriteString(dimSty.Render("validation: skipped") + "\n")
	case m.pending.Report.Passed():
		b.WriteString(okSty.Render("validation: ✓ passed") + "\n")
	default:
		b.WriteString(badSty.Render("validation: ✗ failed") + "\n")
	}
	// diff, truncated to fit
	maxLines := m.height - 14
	if maxLines < 4 {
		maxLines = 4
	}
	lines := strings.Split(m.pending.Diff, "\n")
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	for _, ln := range shown {
		switch {
		case strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++"):
			b.WriteString(addSty.Render(truncate(ln, m.width-1)) + "\n")
		case strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---"):
			b.WriteString(delSty.Render(truncate(ln, m.width-1)) + "\n")
		default:
			b.WriteString(dimSty.Render(truncate(ln, m.width-1)) + "\n")
		}
	}
	if len(lines) > maxLines {
		b.WriteString(dimSty.Render(fmt.Sprintf("… %d more diff lines\n", len(lines)-maxLines)))
	}
	b.WriteString(promptSty.Render("accept these changes?") + " [y]es / [n]o")
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

// indentWrap word-wraps text to width and indents each line by pad spaces,
// showing at most a few lines so an agent's chatty response doesn't dominate.
func indentWrap(text string, width, pad int) string {
	text = strings.TrimSpace(text)
	if width < 20 {
		width = 20
	}
	prefix := strings.Repeat(" ", pad)
	var out []string
	for _, para := range strings.Split(text, "\n") {
		line := prefix
		for _, word := range strings.Fields(para) {
			if len(line)+len(word)+1 > width {
				out = append(out, line)
				line = prefix
			}
			line += word + " "
		}
		out = append(out, strings.TrimRight(line, " "))
		if len(out) >= 8 {
			break
		}
	}
	if len(out) >= 8 {
		out = append(out[:8], prefix+dimSty.Render("…"))
	}
	return strings.Join(out, "\n")
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
