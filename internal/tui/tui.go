// Package tui is Orchestra's dashboard — a Bubble Tea full-screen view over your
// agents (with live health probing), run history, benchmark results, and an
// in-pane Chat tab. Chat runs the agent quietly in the background (spinner while
// it works), then shows the diff in a scrollable pane for accept/reject right in
// the dashboard. The transcript uses bubbles' viewport and the input uses
// bubbles' textarea (multi-line) so redraw and scrolling are handled correctly.
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/router"
	"github.com/crossben/orchestra-code/internal/scheduler"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/validate"
)

type tab int

const (
	tabLogs tab = iota
	tabAgents
	tabHistory
	tabBench
	tabChat
)

var tabNames = []string{"Logs", "Agents", "History", "Benchmarks", "Chat"}

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

type logEntry struct {
	time time.Time
	kind string // "info" | "error" | "turn" | "route" | "probe"
	text string
}

// Model is the dashboard state.
type Model struct {
	d             Deps
	active        tab
	width, height int
	ready         bool

	runs    []memory.Run
	benches []memory.BenchRow

	probing bool
	probed  map[string]agent.ProbeResult

	// chat
	ta       textarea.Model
	vp       viewport.Model
	cstate   chatState
	messages []chatLine
	pending  engine.Turn
	frame    int

	// logs
	logs []logEntry

	status string
}

// New builds the dashboard model.
func New(d Deps) Model {
	ta := textarea.New()
	ta.Placeholder = "ask the agent to do something…"
	ta.Prompt = promptSty.Render("┃ ")
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(4)
	ta.MaxHeight = 8
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.KeyMap.InsertNewline.SetEnabled(false) // enter → newline disabled; ctrl+enter for submit handled in updateChat
	ta.Focus()

	m := Model{
		d:      d,
		width:  80,
		height: 24,
		probed: map[string]agent.ProbeResult{},
		ta:     ta,
		vp:     viewport.New(80, 12),
	}
	m.reload()
	m.setChatContent()
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
type turnMsg struct {
	turn engine.Turn
	note string // e.g. "routed to opencode — implementation"
}
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

var (
	errNotRepo = fmt.Errorf("not a git repository — chat needs one for the supervised loop")
	errDirty   = fmt.Errorf("working tree has uncommitted changes — commit/stash first")
)

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

func (m Model) produceCmd(text string) tea.Cmd {
	d := m.d
	return func() tea.Msg {
		if !gitutil.IsRepo(d.Dir) {
			return turnMsg{turn: engine.Turn{Err: errNotRepo}}
		}
		if clean, _ := gitutil.IsClean(d.Dir); !clean {
			return turnMsg{turn: engine.Turn{Err: errDirty}}
		}

		agentName := d.DefaultAgent
		note := ""
		if d.RoutingOn && d.Router != nil {
			dec := d.Router.Route(d.Ctx, text, d.Dir) // quiet: safe in the TUI
			if dec.IsQuestion() {
				ans, err := d.Router.Answer(d.Ctx, text, d.Dir, d.Timeout)
				if err != nil {
					return turnMsg{turn: engine.Turn{Err: err}}
				}
				return turnMsg{turn: engine.Turn{AgentText: ans, HadChanges: false}}
			}
			agentName = dec.Agent
			note = fmt.Sprintf("↳ routed to %s — %s", dec.Agent, dec.Reason)
		}

		ag, ok := d.Reg.Get(agentName)
		if !ok {
			return turnMsg{turn: engine.Turn{Err: fmt.Errorf("agent %q not available", agentName)}}
		}
		t := engine.Produce(d.Ctx, engine.Options{
			Agent: ag, Prompt: text, Dir: d.Dir,
			Stages: d.Stages, MaxRetries: d.MaxRetries,
			Timeout: d.Timeout, Principles: d.Principles,
		})
		return turnMsg{turn: t, note: note}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		m.setChatContent()
		return m, nil
	case probeDoneMsg:
		m.probed = msg.results
		m.probing = false
		m.logEvent("probe", fmt.Sprintf("probed %d agents", len(msg.results)))
		m.status = "probe complete"
		return m, nil
	case tickMsg:
		if m.cstate == chatRunning {
			m.frame++
			return m, tickCmd()
		}
		return m, nil
	case turnMsg:
		return m.onTurn(msg.turn, msg.note)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.active == tabChat {
			return m.updateChat(msg)
		}
		prev := m.active
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "tab", "right", "l":
			m.active = (m.active + 1) % 5
		case "shift+tab", "left", "h":
			m.active = (m.active + 4) % 5
		case "1":
			m.active = tabLogs
		case "2":
			m.active = tabAgents
		case "3":
			m.active = tabHistory
		case "4":
			m.active = tabBench
		case "5":
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
		if m.active != prev {
			if prev == tabChat {
				m.ta.Blur()
			}
			if m.active == tabChat {
				m.ta.Focus()
			}
			m.layout()
			m.setChatContent()
		}
		return m, nil
	}
	// Non-key messages (e.g. cursor blink) go to the input.
	if m.active == tabChat {
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updateChat: tab/shift+tab always navigate; otherwise route to
// the input (idle), the review keys (reviewing), or scroll (running).
func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		m.active = (m.active + 1) % 5
		m.layout()
		m.setChatContent()
		return m, nil
	case "shift+tab":
		m.active = (m.active + 4) % 5
		m.layout()
		m.setChatContent()
		return m, nil
	}

	switch m.cstate {
	case chatReviewing:
		switch msg.String() {
		case "y", "Y":
			return m.accept()
		case "n", "N", "esc":
			return m.reject()
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg) // scroll the diff
		return m, cmd
	case chatRunning:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	// idle
	switch msg.String() {
	case "esc":
		m.active = tabAgents
		return m, nil
	case "ctrl+j": // ctrl+enter
		return m.submitChat()
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m Model) submitChat() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	m.ta.Reset()
	m.messages = append(m.messages, chatLine{"you", text})
	m.cstate = chatRunning
	m.frame = 0
	m.status = ""
	m.setChatContent()
	return m, tea.Batch(tickCmd(), m.produceCmd(text))
}

func (m Model) onTurn(t engine.Turn, note string) (tea.Model, tea.Cmd) {
	if note != "" {
		m.messages = append(m.messages, chatLine{"sys", note})
		m.logEvent("route", note)
	}
	switch {
	case t.Err != nil:
		m.messages = append(m.messages, chatLine{"sys", "error: " + t.Err.Error()})
		m.logEvent("error", t.Err.Error())
		m.cstate = chatIdle
	case !t.HadChanges:
		resp := strings.TrimSpace(t.AgentText)
		if resp == "" {
			resp = "(the agent made no file changes)"
		}
		m.messages = append(m.messages, chatLine{"agent", resp})
		m.logEvent("turn", "agent replied ("+fmt.Sprintf("%d", len(resp))+" chars)")
		ui.Notify("Orchestra", "Agent finished — "+firstLine(resp))
		m.cstate = chatIdle
	default:
		m.pending = t
		m.logEvent("turn", "agent produced changes — review required")
		ui.Notify("Orchestra", "Agent produced changes — review required")
		m.cstate = chatReviewing
	}
	m.setChatContent()
	return m, nil
}

func (m Model) accept() (tea.Model, tea.Cmd) {
	last := lastUserMsg(m.messages)
	if err := gitutil.Commit(m.d.Dir, "orchestra: "+firstLine(last)); err != nil {
		m.messages = append(m.messages, chatLine{"sys", "commit failed: " + err.Error()})
	} else {
		m.messages = append(m.messages, chatLine{"sys", "✓ accepted & committed"})
	}
	m.cstate = chatIdle
	m.pending = engine.Turn{}
	m.reload()
	m.setChatContent()
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
	m.setChatContent()
	return m, nil
}

func (m *Model) logEvent(kind, text string) {
	m.logs = append(m.logs, logEntry{time: time.Now(), kind: kind, text: text})
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
}

// layout sizes the viewport and input to the window.
func (m *Model) layout() {
	w := m.width
	vpH := m.height - 5 // header(2) + footer(2) + padding(1)
	if m.active == tabChat {
		vpH -= 6 // textarea(4) + gap(2)
	}
	if vpH < 3 {
		vpH = 3
	}
	m.vp.Width = w
	m.vp.Height = vpH
	m.ta.SetWidth(w)
}

// setChatContent refreshes the viewport with the transcript or the diff.
func (m *Model) setChatContent() {
	if m.cstate == chatReviewing {
		m.vp.SetContent(m.renderDiff())
		m.vp.GotoTop()
		return
	}
	m.vp.SetContent(m.renderTranscript())
	m.vp.GotoBottom()
}

func (m Model) renderTranscript() string {
	if len(m.messages) == 0 {
		return dimSty.Render("Ask the agent to do something. It runs in the background; the diff\n" +
			"appears here for you to accept or reject — all without leaving this screen.")
	}
	wrap := lipgloss.NewStyle().Width(max(m.vp.Width-1, 20))
	var b strings.Builder
	for i, msg := range m.messages {
		if i > 0 {
			b.WriteString("\n")
		}
		switch msg.role {
		case "you":
			b.WriteString(youSty.Render("you"))
			b.WriteString("\n")
			b.WriteString(wrap.Render(msg.text))
			b.WriteString("\n")
		case "agent":
			b.WriteString(headSty.Render("agent"))
			b.WriteString("\n")
			b.WriteString(renderMarkdown(msg.text, max(m.vp.Width-1, 20)))
			b.WriteString("\n")
		case "sys":
			b.WriteString(dimSty.Render("· " + msg.text))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m Model) renderDiff() string {
	var b strings.Builder
	switch {
	case m.pending.Report.Skipped:
		b.WriteString(dimSty.Render("validation: skipped"))
		b.WriteString("\n\n")
	case m.pending.Report.Passed():
		b.WriteString(okSty.Render("validation: ✓ passed"))
		b.WriteString("\n\n")
	default:
		b.WriteString(badSty.Render("validation: ✗ failed"))
		b.WriteString("\n\n")
	}
	b.WriteString(highlightDiff(m.pending.Diff)) // chroma diff highlighting
	return b.String()
}

func (m Model) View() string {
	if !m.ready {
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
	case tabChat:
		b.WriteString(m.chatView())
	case tabLogs:
		b.WriteString(m.logsView())
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
	return title + "   " + strings.Join(tabs, " ") + "\n" + dimSty.Render(strings.Repeat("─", m.width))
}

func (m Model) footer() string {
	var keys string
	switch m.active {
	case tabAgents:
		keys = "tab: switch • p: probe • r: refresh • q: quit"
	case tabChat:
		switch m.cstate {
		case chatReviewing:
			keys = "y: accept & commit • n: reject & revert • ↑/↓: scroll • tab: switch"
		case chatRunning:
			keys = "working… • ↑/↓: scroll • tab: switch • ctrl+c: quit"
		default:
			keys = "ctrl+enter: send • enter: newline • ↑/↓ pgup/pgdn: scroll • tab: switch • esc: leave"
		}
	default:
		keys = "tab: switch • r: refresh • q: quit"
	}
	status := ""
	if m.status != "" {
		status = "   " + m.status
	}
	return footerSty.Render(strings.Repeat("─", m.width) + "\n" + keys + status)
}

func (m Model) chatView() string {
	mode := "agent: " + m.d.DefaultAgent
	if m.d.RoutingOn && m.d.Router != nil {
		mode = "AI routing on"
	}
	head := headSty.Render("Chat") + dimSty.Render("   ("+mode+")")
	var bottom string
	switch m.cstate {
	case chatRunning:
		bottom = titleSty.Render(spinnerFrames[m.frame%len(spinnerFrames)]) + dimSty.Render(" agent is working…")
	case chatReviewing:
		bottom = promptSty.Render("accept these changes?") + " [y]es / [n]o"
	default:
		bottom = m.ta.View()
	}
	return head + "\n\n" + m.vp.View() + "\n\n" + bottom
}

func (m Model) logsView() string {
	if len(m.logs) == 0 {
		return dimSty.Render("no events yet — actions in Chat and Agents tabs are logged here")
	}
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-20s %-8s %s", "TIME", "KIND", "MESSAGE")))
	b.WriteString("\n")
	// Show newest first, fit to screen
	start := 0
	avail := m.height - 5 // header + footer + padding
	if avail < 1 {
		avail = 1
	}
	if len(m.logs) > avail {
		start = len(m.logs) - avail
	}
	for i := start; i < len(m.logs); i++ {
		e := m.logs[i]
		kind := dimSty.Render(e.kind)
		switch e.kind {
		case "error":
			kind = badSty.Render(e.kind)
		case "turn":
			kind = okSty.Render(e.kind)
		case "route":
			kind = youSty.Render(e.kind)
		}
		b.WriteString(fmt.Sprintf("%-20s %-8s %s\n",
			e.time.Local().Format("01-02 15:04:05"), kind, truncate(e.text, m.width-32)))
	}
	return b.String()
}

func (m Model) agentsView() string {
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-12s %-14s %-22s %s", "AGENT", "INSTALLED", "PROBE", "CAPABILITIES")))
	b.WriteString("\n")
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
	b.WriteString("\n")
	b.WriteString(dimSty.Render("* default agent   ·   press p to live-probe whether each agent can actually run"))
	return b.String()
}

func (m Model) historyView() string {
	if len(m.runs) == 0 {
		return dimSty.Render("no run history yet — try the Chat tab, `orchestra run`, or `orchestra do`")
	}
	var b strings.Builder
	b.WriteString(headSty.Render(fmt.Sprintf("%-17s %-10s %-10s %-4s %s", "WHEN", "AGENT", "OUTCOME", "ATT", "TASK")))
	b.WriteString("\n")
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
	b.WriteString(headSty.Render(fmt.Sprintf("%-17s %-10s %-6s %-6s %-8s %s", "WHEN", "AGENT", "WON", "VALID", "TIME", "TASK")))
	b.WriteString("\n")
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

// --- helpers ---

func lastUserMsg(msgs []chatLine) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].role == "you" {
			return msgs[i].text
		}
	}
	return ""
}

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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
