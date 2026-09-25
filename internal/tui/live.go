package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
)

// Stage states in the live timeline.
const (
	stPending = iota
	stRunning
	stPassed
	stFailed
)

type stageState struct {
	name  string
	state int
}

// liveRun is one chat turn in flight (and, once finished, the last run shown
// in the side panel). Only Update mutates it; the worker goroutine talks to
// the model exclusively through ch.
type liveRun struct {
	ch     chan tea.Msg
	ctx    context.Context
	cancel context.CancelFunc
	task   string
	start  time.Time
	end    time.Time

	routing   bool // waiting for the router
	agent     string
	reason    string
	attempt   int
	max       int
	agentDone bool
	exitCode  int
	agentDur  time.Duration
	stages    []stageState
	past      []string // one summary line per finished attempt
	lines     []string // agent output tail
	cancelled bool
	result    string // set when finished: "review", "answered", "no changes", "failed", "cancelled"
}

const maxRunLines = 400

func (r *liveRun) addLines(ls ...string) {
	r.lines = append(r.lines, ls...)
	if len(r.lines) > maxRunLines {
		r.lines = r.lines[len(r.lines)-maxRunLines:]
	}
}

// --- worker → model messages ---

type routedMsg struct{ agent, reason string }
type eventMsg struct{ e engine.Event }
type outputMsg struct{ lines []string }
type turnMsg struct {
	turn     engine.Turn
	agent    string
	answered bool // the router answered a question; nothing ran
	started  bool // Produce ran, so the tree may have been touched
}

// waitRun delivers the next message from the worker (nil once it is done).
func waitRun(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// startRun launches the worker for text and returns the commands that drive it.
func (m *Model) startRun(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.d.Ctx)
	run := &liveRun{
		ch:      make(chan tea.Msg, 256),
		ctx:     ctx,
		cancel:  cancel,
		task:    text,
		start:   time.Now(),
		routing: m.d.RoutingOn && m.d.Router != nil,
		agent:   m.d.DefaultAgent,
	}
	m.run = run
	d := m.d
	start := func() tea.Msg {
		go runTurn(ctx, d, text, run.ch)
		return nil
	}
	return tea.Batch(tickCmd(), start, waitRun(run.ch))
}

// runTurn is the worker: route, then Produce with live events and output.
// The final turnMsg is always delivered, then the channel is closed.
func runTurn(ctx context.Context, d Deps, text string, ch chan tea.Msg) {
	defer close(ch)
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}

	// Inside a git repo the tree must start clean (reject uses git restore).
	// Plain directories are always allowed: the engine snapshots them.
	if gitutil.IsRepo(d.Dir) {
		if clean, _ := gitutil.IsClean(d.Dir); !clean {
			ch <- turnMsg{turn: engine.Turn{Err: errDirty}}
			return
		}
	}

	agentName := d.DefaultAgent
	if d.RoutingOn && d.Router != nil {
		dec := d.Router.Route(ctx, text, d.Dir) // quiet: safe in the TUI
		if dec.IsQuestion() {
			ans, err := d.Router.Answer(ctx, text, d.Dir, d.Timeout)
			ch <- turnMsg{turn: engine.Turn{AgentText: ans, Err: err}, answered: true}
			return
		}
		agentName = dec.Agent
		send(routedMsg{agent: dec.Agent, reason: dec.Reason})
	}

	ag, ok := d.Reg.Get(agentName)
	if !ok {
		ch <- turnMsg{turn: engine.Turn{Err: fmt.Errorf("agent %q not available", agentName)}, agent: agentName}
		return
	}
	lw := &lineWriter{ch: ch}
	t := engine.Produce(ctx, engine.Options{
		Agent: ag, Prompt: text, Dir: d.Dir,
		Stages: d.Stages, MaxRetries: d.MaxRetries,
		Timeout: d.Timeout, Principles: d.Principles,
		OnEvent: func(e engine.Event) { send(eventMsg{e}) },
		Output:  lw,
	})
	lw.flush()
	ch <- turnMsg{turn: t, agent: agentName, started: true}
}

// lineWriter turns streamed agent output into cleaned lines for the panel.
// It never blocks the agent: if the UI is behind, lines are dropped (the full
// output still arrives with the final turn).
type lineWriter struct {
	mu      sync.Mutex
	ch      chan tea.Msg
	partial string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial += string(p)
	i := strings.LastIndexByte(w.partial, '\n')
	if i < 0 {
		return len(p), nil
	}
	complete := w.partial[:i]
	w.partial = w.partial[i+1:]
	w.emit(strings.Split(complete, "\n"))
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.partial != "" {
		w.emit([]string{w.partial})
		w.partial = ""
	}
}

func (w *lineWriter) emit(raw []string) {
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		lines = append(lines, cleanLine(l))
	}
	select {
	case w.ch <- outputMsg{lines: lines}:
	default:
	}
}

// cleanLine strips terminal escapes and keeps only what a carriage-return
// progress line finally shows.
func cleanLine(s string) string {
	s = ansi.Strip(s)
	if i := strings.LastIndexByte(strings.TrimRight(s, "\r"), '\r'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimRight(s, "\r")
	return strings.ReplaceAll(s, "\t", "    ")
}

// cleanText applies cleanLine to every line of an agent's captured output, so
// colour codes from CLIs like opencode never reach the transcript as "[0m".
func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = cleanLine(l)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// onRunMsg applies one worker message to the model.
func (m Model) onRunMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	run := m.run
	if run == nil {
		return m, nil
	}
	rearm := waitRun(run.ch)
	switch msg := msg.(type) {
	case routedMsg:
		run.routing = false
		run.agent, run.reason = msg.agent, msg.reason
		note := "routed to " + msg.agent
		if msg.reason != "" {
			note += " — " + msg.reason
		}
		m.logEvent("route", note)
		m.messages = append(m.messages, chatLine{role: "sys", text: "↳ " + note})
		m.setChatContent()
	case eventMsg:
		m.applyEvent(msg.e)
	case outputMsg:
		run.addLines(msg.lines...)
	case turnMsg:
		run.routing = false
		run.end = time.Now()
		return m.onTurn(msg)
	}
	return m, rearm
}

func (m *Model) applyEvent(e engine.Event) {
	run := m.run
	run.routing = false
	switch e.Kind {
	case engine.EventAttempt:
		if run.attempt > 0 {
			run.past = append(run.past, run.attemptSummary())
		}
		run.attempt, run.max = e.Attempt, e.Max
		run.agentDone = false
		run.stages = run.stages[:0]
		for _, st := range m.d.Stages {
			if strings.TrimSpace(st.Command) != "" {
				run.stages = append(run.stages, stageState{name: st.Name})
			}
		}
		if e.Attempt > 1 {
			run.addLines(fmt.Sprintf("── retry %d/%d: self-correcting ──", e.Attempt-1, e.Max-1))
			m.logEvent("turn", fmt.Sprintf("retry %d/%d", e.Attempt-1, e.Max-1))
		}
	case engine.EventAgentDone:
		run.agentDone = true
		run.exitCode, run.agentDur = e.ExitCode, e.Duration
	case engine.EventStageStart, engine.EventStageDone:
		state := stRunning
		if e.Kind == engine.EventStageDone {
			state = stFailed
			if e.Passed {
				state = stPassed
			}
		}
		for i := range run.stages {
			if run.stages[i].name == e.Stage {
				run.stages[i].state = state
				return
			}
		}
		run.stages = append(run.stages, stageState{name: e.Stage, state: state})
	}
}

// attemptSummary condenses the attempt that just ended into one line.
func (r *liveRun) attemptSummary() string {
	label := fmt.Sprintf("attempt %d/%d", r.attempt, r.max)
	for _, st := range r.stages {
		if st.state == stFailed {
			return badSty.Render("✗") + " " + label + dimSty.Render(" · "+st.name+" failed")
		}
	}
	return warnSty.Render("!") + " " + label
}

// runPanel renders the live timeline + output tail in a w×h box.
func (m Model) runPanel(w, h int) string {
	run := m.run
	inner := max(w-4, 10)
	var head []string

	// Title line: agent · elapsed, or the final result.
	elapsed := time.Since(run.start)
	if !run.end.IsZero() {
		elapsed = run.end.Sub(run.start)
	}
	status := ""
	switch run.result {
	case "":
		status = warnSty.Render(spinnerFrames[m.frame%len(spinnerFrames)] + " running")
	case "review":
		status = okSty.Render("✓ ready for review")
	case "answered":
		status = okSty.Render("✓ answered")
	case "no changes":
		status = dimSty.Render("○ no file changes")
	case "cancelled":
		status = warnSty.Render("↺ cancelled")
	default:
		status = badSty.Render("✗ " + run.result)
	}
	head = append(head, spread(status, dimSty.Render(fmtElapsed(elapsed)), inner))

	// Timeline.
	switch {
	case run.routing:
		head = append(head, warnSty.Render(spin(m.frame))+" routing…")
	case run.reason != "":
		head = append(head, okSty.Render("✓")+" routed → "+youSty.Render(run.agent)+dimSty.Render(" · "+run.reason))
	case run.agent != "" && run.result != "answered":
		head = append(head, dimSty.Render("•")+" agent "+youSty.Render(run.agent))
	}
	head = append(head, run.past...)
	if run.attempt > 0 {
		label := "attempt " + fmt.Sprint(run.attempt)
		if run.max > 1 {
			label = fmt.Sprintf("attempt %d/%d", run.attempt, run.max)
		}
		switch {
		case !run.agentDone && run.result == "":
			head = append(head, warnSty.Render(spin(m.frame))+" "+label+dimSty.Render(" · agent working"))
		case !run.agentDone:
			head = append(head, dimSty.Render("•")+" "+label)
		default:
			mark := okSty.Render("✓")
			if run.exitCode != 0 {
				mark = warnSty.Render("!")
			}
			head = append(head, mark+" "+label+dimSty.Render(fmt.Sprintf(" · exit %d · %s", run.exitCode, run.agentDur.Round(time.Second/10))))
		}
		if len(run.stages) > 0 {
			var parts []string
			for _, st := range run.stages {
				var g string
				switch st.state {
				case stPending:
					g = dimSty.Render("○")
				case stRunning:
					g = warnSty.Render(spin(m.frame))
				case stPassed:
					g = okSty.Render("✓")
				default:
					g = badSty.Render("✗")
				}
				parts = append(parts, g+" "+st.name)
			}
			head = append(head, "  "+strings.Join(parts, "  "))
		} else if run.agentDone {
			head = append(head, "  "+warnSty.Render("no checks configured — unverified"))
		}
	}

	// Output tail fills the rest.
	outH := h - 2 - len(head) - 2 // border, head, gap + label
	var body []string
	body = append(body, head...)
	if outH >= 1 {
		body = append(body, "", faintSty.Render("output "+strings.Repeat("─", max(inner-7, 0))))
		tail := run.lines
		if len(tail) > outH {
			tail = tail[len(tail)-outH:]
		}
		if len(tail) == 0 {
			tail = []string{dimSty.Render("waiting for output…")}
		}
		for _, l := range tail {
			body = append(body, dimSty.Render(ansiTruncate(l, inner)))
		}
	}
	return panelBox("Run", strings.Join(body, "\n"), w, h, accent)
}

func spin(frame int) string { return spinnerFrames[frame%len(spinnerFrames)] }

// spread puts left and right on one line of width w.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}
