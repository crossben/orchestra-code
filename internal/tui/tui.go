// Package tui is Orchestra's dashboard — a Bubble Tea full-screen app over the
// supervised engine. Chat sends tasks (AI-routed or to the default agent) and
// shows each run live: routing, every attempt, the agent's output, and each
// validation stage as it happens. Changes are then reviewed file by file and
// accepted or rejected in place. Changes and History reopen any past run's
// diff (diffs are persisted in the memory store); Agents, Benchmarks and Logs
// round it out.
//
// Layout: tui.go (model, messages, key routing), chat.go (chat + transcript),
// live.go (running a turn and the live run panel), review.go (diff review),
// views.go (list tabs), chrome.go (header + status bar), theme.go, table.go.
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
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
	"github.com/crossben/orchestra-code/internal/validate"
)

type tab int

const (
	tabChat tab = iota
	tabChanges
	tabHistory
	tabAgents
	tabBench
	tabLogs
	numTabs
)

var tabNames = []string{"Chat", "Changes", "History", "Agents", "Benchmarks", "Logs"}

type chatState int

const (
	chatIdle chatState = iota
	chatRunning
	chatReviewing
)

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
	role  string // "you" | "agent" | "sys"
	text  string
	agent string // who answered (agent lines)
}

type logEntry struct {
	time time.Time
	kind string // "info" | "error" | "turn" | "route" | "probe"
	text string
}

// runStat caches a history run's diff size for list views.
type runStat struct{ files, added, removed int }

// Model is the dashboard state.
type Model struct {
	d             Deps
	active        tab
	width, height int
	ready         bool

	repoName string
	branch   string
	inRepo   bool

	runs     []memory.Run // newest first (from memory, or this session without one)
	runStats []runStat    // parallel to runs
	benches  []memory.BenchRow
	stats    map[string]memory.AgentStats

	probing bool
	probed  map[string]agent.ProbeResult

	// chat
	ta       textarea.Model
	vp       viewport.Model // transcript
	cstate   chatState
	messages []chatLine
	run      *liveRun // current or last run (nil before the first)
	frame    int
	rv       reviewer    // pending review (cstate == chatReviewing)
	pending  engine.Turn // the turn under review
	pendAg   string      // agent that produced it
	pendTask string

	// changes / history
	changeSel, histSel int
	browsing           bool
	browse             reviewer

	logs []logEntry

	quitting  bool // ctrl+c during a run: quit once it has been reverted
	installed int  // agents found on PATH (cached for the session card)

	status   string
	statusAt time.Time
}

// New builds the dashboard model.
func New(d Deps) Model {
	ta := textarea.New()
	ta.Placeholder = "Describe a change, or ask a question…"
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.MaxHeight = 8
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = dimSty
	ta.BlurredStyle.Placeholder = dimSty
	// enter sends (handled in updateChat); ctrl+j inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	ta.Focus()

	m := Model{
		d:        d,
		width:    80,
		height:   24,
		probed:   map[string]agent.ProbeResult{},
		ta:       ta,
		vp:       viewport.New(80, 12),
		repoName: filepath.Base(d.Dir),
		inRepo:   gitutil.IsRepo(d.Dir),
	}
	if m.inRepo {
		m.branch = gitutil.Branch(d.Dir)
	}
	if d.Reg != nil {
		for _, a := range d.Reg.All() {
			if a.Health() == nil {
				m.installed++
			}
		}
	}
	m.reload()
	m.setChatContent()
	return m
}

// reload refreshes history, benchmarks and agent stats from the memory store.
func (m *Model) reload() {
	if m.d.Mem != nil {
		if r, err := m.d.Mem.Recent(m.d.Dir, 200); err == nil {
			m.runs = r
		}
		if b, err := m.d.Mem.RecentBenchmarks(m.d.Dir, 100); err == nil {
			m.benches = b
		}
		if s, err := m.d.Mem.StatsByAgent(m.d.Dir); err == nil {
			m.stats = s
		}
	}
	m.runStats = make([]runStat, len(m.runs))
	for i, r := range m.runs {
		if r.Diff != "" {
			files := parseDiff(r.Diff)
			a, d := diffTotals(files)
			m.runStats[i] = runStat{files: len(files), added: a, removed: d}
		}
	}
	m.clampSelections()
}

// record stores a finished run in history (memory store, or this session).
func (m *Model) record(r memory.Run) {
	r.Dir = m.d.Dir
	if m.d.Mem != nil {
		if err := m.d.Mem.Record(r, time.Now()); err != nil {
			m.logEvent("error", "could not record run: "+err.Error())
		}
	} else {
		r.Time = time.Now()
		m.runs = append([]memory.Run{r}, m.runs...)
		if m.stats == nil {
			m.stats = map[string]memory.AgentStats{}
		}
		st := m.stats[r.Agent]
		st.Runs++
		if r.Outcome == "accepted" {
			st.Accepted++
		}
		st.LastUsed = r.Time
		m.stats[r.Agent] = st
	}
	m.reload()
}

func (m Model) Init() tea.Cmd { return textarea.Blink }

// --- messages ---

type probeDoneMsg struct{ results map[string]agent.ProbeResult }
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

var errDirty = fmt.Errorf("working tree has uncommitted changes — commit or stash first")

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

func (m *Model) setStatus(s string) {
	m.status = s
	m.statusAt = time.Now()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		m.setChatContent()
		return m, nil
	case probeDoneMsg:
		m.probed = msg.results
		m.probing = false
		m.logEvent("probe", fmt.Sprintf("probed %d agents", len(msg.results)))
		m.setStatus("probe complete")
		return m, nil
	case tickMsg:
		if m.cstate == chatRunning {
			m.frame++
			return m, tickCmd()
		}
		return m, nil
	case routedMsg, eventMsg, outputMsg, turnMsg:
		return m.onRunMsg(msg)
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	// Non-key messages (e.g. cursor blink) go to the input.
	if m.active == tabChat && m.cstate == chatIdle {
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Mid-run, cancel first so the agent's partial edits are reverted,
		// then quit when the run reports back. A second ctrl+c forces it.
		if m.cstate == chatRunning && !m.quitting {
			m.quitting = true
			m.run.cancelled = true
			m.run.cancel()
			m.setStatus("cancelling and reverting before quitting… (ctrl+c again to force)")
			return m, nil
		}
		return m, tea.Quit
	case "tab":
		return m.switchTab((m.active + 1) % numTabs), nil
	case "shift+tab":
		return m.switchTab((m.active + numTabs - 1) % numTabs), nil
	}
	if m.active == tabChat {
		return m.updateChat(msg)
	}
	if m.browsing {
		if msg.String() == "esc" || msg.String() == "q" {
			m.browsing = false
			return m, nil
		}
		var cmd tea.Cmd
		m.browse, cmd, _ = m.browse.update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "right", "l":
		return m.switchTab((m.active + 1) % numTabs), nil
	case "left", "h":
		return m.switchTab((m.active + numTabs - 1) % numTabs), nil
	case "1", "2", "3", "4", "5", "6":
		return m.switchTab(tab(msg.String()[0] - '1')), nil
	case "r":
		m.reload()
		m.setStatus("refreshed")
	case "p":
		if m.active == tabAgents && !m.probing {
			m.probing = true
			m.setStatus("probing agents…")
			return m, m.probeCmd()
		}
	case "down", "j":
		m.moveSel(1)
	case "up", "k":
		m.moveSel(-1)
	case "pgdown":
		m.moveSel(10)
	case "pgup":
		m.moveSel(-10)
	case "enter":
		m.openSelected()
	}
	return m, nil
}

func (m Model) switchTab(t tab) Model {
	if t == m.active {
		return m
	}
	if m.active == tabChat {
		m.ta.Blur()
	}
	m.active = t
	m.browsing = false
	if t == tabChat && m.cstate == chatIdle {
		m.ta.Focus()
	}
	m.layout()
	m.setChatContent()
	return m
}

// changeIdx lists the indexes of runs that produced a diff.
func (m Model) changeIdx() []int {
	var out []int
	for i, r := range m.runs {
		if r.Diff != "" {
			out = append(out, i)
		}
	}
	return out
}

func (m *Model) clampSelections() {
	clamp := func(v, n int) int {
		if v >= n {
			v = n - 1
		}
		if v < 0 {
			v = 0
		}
		return v
	}
	m.changeSel = clamp(m.changeSel, len(m.changeIdx()))
	m.histSel = clamp(m.histSel, len(m.runs))
}

func (m *Model) moveSel(delta int) {
	switch m.active {
	case tabChanges:
		m.changeSel += delta
	case tabHistory:
		m.histSel += delta
	}
	m.clampSelections()
}

// openSelected opens the selected run's diff in the read-only reviewer.
func (m *Model) openSelected() {
	idx := -1
	switch m.active {
	case tabChanges:
		if ci := m.changeIdx(); m.changeSel < len(ci) {
			idx = ci[m.changeSel]
		}
	case tabHistory:
		if m.histSel < len(m.runs) {
			idx = m.histSel
		}
	}
	if idx < 0 {
		return
	}
	r := m.runs[idx]
	if r.Diff == "" {
		m.setStatus("no diff recorded for this run")
		return
	}
	m.browse = newReviewer(r.Diff, reviewMeta{
		Agent: r.Agent, Task: r.Prompt, Attempts: r.Attempts,
		Outcome: r.Outcome, When: r.Time, ReadOnly: true,
	}, m.width, m.bodyHeight())
	m.browsing = true
}

func (m *Model) logEvent(kind, text string) {
	m.logs = append(m.logs, logEntry{time: time.Now(), kind: kind, text: text})
	if len(m.logs) > 300 {
		m.logs = m.logs[len(m.logs)-300:]
	}
}

// bodyHeight is the space between the header (2 lines) and status bar (1).
func (m Model) bodyHeight() int { return max(m.height-3, 5) }

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	var body string
	switch {
	case m.browsing:
		body = m.browse.view()
	case m.active == tabChat:
		body = m.chatView()
	case m.active == tabChanges:
		body = m.changesView()
	case m.active == tabHistory:
		body = m.historyView()
	case m.active == tabAgents:
		body = m.agentsView()
	case m.active == tabBench:
		body = m.benchView()
	case m.active == tabLogs:
		body = m.logsView()
	}
	return m.header() + "\n" + padLines(body, m.bodyHeight(), m.width) + "\n" + m.statusBar()
}
