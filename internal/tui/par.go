package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/parallel"
	"github.com/crossben/orchestra-code/internal/planner"
	"github.com/crossben/orchestra-code/internal/scheduler"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/worktree"
)

// Parallel runs (/parallel <request>): plan the request, run each dependency
// wave concurrently in isolated trees, then review every task that changed
// something with the regular reviewer and merge it on accept. The shared wave
// logic lives in internal/parallel; this file is the dashboard's view of it.
//
// Concurrency model: the plan and each wave run in a worker goroutine that
// only talks to the model through parRun.ch (tagged with the task id). The
// isolator and guard are handed to the worker for the duration of a wave and
// used by Update only between waves.

// parPhase is where a parallel run is.
type parPhase int

const (
	parPlanning parPhase = iota
	parRunning
	parReviewing
	parFinished
)

// taskStatus is one task's state, live and after review.
type taskStatus int

const (
	tsQueued taskStatus = iota
	tsRunning
	tsValidating
	tsRetrying
	tsDone // finished; waiting for review
	tsFailed
	tsMerged
	tsRejected
	tsConflict
	tsNoChanges
	tsCancelled
)

var taskStatusNames = [...]string{"queued", "running", "validating", "retrying", "done", "failed",
	"merged", "rejected", "conflict", "no changes", "cancelled"}

func (s taskStatus) String() string { return taskStatusNames[s] }

// parTask is one step of the current wave.
type parTask struct {
	idx          int // step index in the plan
	id           string
	title        string
	prompt       string
	agent        string
	ag           agent.Agent
	status       taskStatus
	attempt, max int
	start, end   time.Time
	lines        []string
	turn         engine.Turn
	tree         worktree.Tree
	created      bool
	err          error
}

func (t *parTask) addLines(ls ...string) {
	t.lines = append(t.lines, ls...)
	if len(t.lines) > maxRunLines {
		t.lines = t.lines[len(t.lines)-maxRunLines:]
	}
}

// last is the last non-blank output line.
func (t *parTask) last() string {
	for i := len(t.lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(t.lines[i]) != "" {
			return t.lines[i]
		}
	}
	return ""
}

func (t *parTask) elapsed() time.Duration {
	switch {
	case t.start.IsZero():
		return 0
	case t.end.IsZero():
		return time.Since(t.start)
	}
	return t.end.Sub(t.start)
}

// parRun is a parallel run in flight (and, once finished, the last one shown
// in the side panel). Only Update mutates it.
type parRun struct {
	ctx     context.Context
	cancel  context.CancelFunc
	ch      chan tea.Msg // current phase's worker channel
	request string
	start   time.Time
	phase   parPhase

	plan      planner.Plan
	graph     *parallel.Graph
	iso       worktree.Isolator
	guard     *parallel.BaseGuard
	planAgent agent.Agent

	wave, waves int
	tasks       []*parTask // the current wave
	sel         int
	expanded    bool
	out         viewport.Model // expanded task's output

	queue     []int // tasks (indexes into tasks) awaiting review, in order
	reviewing int   // task under review (index into tasks)
	cancelled bool
}

// --- worker → model messages ---

type parPlannedMsg struct {
	plan  planner.Plan
	graph *parallel.Graph
	iso   worktree.Isolator
	agent agent.Agent
	err   error
}
type parEventMsg struct {
	id string
	e  engine.Event
}
type parOutputMsg struct {
	id    string
	lines []string
}
type parTaskDoneMsg struct {
	id      string
	turn    engine.Turn
	tree    worktree.Tree
	created bool
	err     error
}
type parWaveDoneMsg struct{ stray bool }

// parPrefix recognises "/parallel <request>" and "/par <request>".
func parPrefix(text string) (request string, ok bool) {
	for _, p := range []string{"/parallel", "/par"} {
		if text == p {
			return "", true
		}
		if strings.HasPrefix(text, p+" ") || strings.HasPrefix(text, p+"\n") {
			return strings.TrimSpace(text[len(p):]), true
		}
	}
	return "", false
}

// startPar begins planning request; the worker is launched immediately.
func (m *Model) startPar(request string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.d.Ctx)
	p := &parRun{ctx: ctx, cancel: cancel, ch: make(chan tea.Msg, 4), request: request,
		start: time.Now(), phase: parPlanning, out: viewport.New(40, 5)}
	m.par = p
	m.run = nil
	go runParPlan(ctx, m.d, request, p.ch)
	return tea.Batch(tickCmd(), waitRun(p.ch))
}

// runParPlan is the planning worker. It always delivers one parPlannedMsg,
// then closes ch.
func runParPlan(ctx context.Context, d Deps, request string, ch chan tea.Msg) {
	defer close(ch)
	msg := parPlannedMsg{}
	defer func() { ch <- msg }()

	if gitutil.IsRepo(d.Dir) {
		if clean, _ := gitutil.IsClean(d.Dir); !clean {
			msg.err = errDirty
			return
		}
	}
	ag, ok := d.Reg.Get(d.DefaultAgent)
	if !ok {
		msg.err = fmt.Errorf("agent %q not available", d.DefaultAgent)
		return
	}
	if err := ag.Health(); err != nil {
		msg.err = fmt.Errorf("agent %q is not available: %w", d.DefaultAgent, err)
		return
	}
	pl, err := planner.New(ag, d.Timeout)
	if err != nil {
		msg.err = err
		return
	}
	plan, err := pl.MakeParallel(ctx, request, d.Dir, parallel.HealthyAgentNames(d.Reg))
	if err != nil {
		msg.err = err
		return
	}
	g, err := parallel.NewGraph(plan)
	if err != nil {
		msg.err = err
		return
	}
	iso, err := worktree.New(d.Dir)
	if err != nil {
		msg.err = err
		return
	}
	msg = parPlannedMsg{plan: plan, graph: g, iso: iso, agent: ag}
}

// waveTask is what the wave worker needs to know about one task.
type waveTask struct {
	id     string
	ag     agent.Agent
	prompt string
}

// runParWave is the wave worker: arm the guard, create each task's tree
// (sequentially — git worktree creation is not concurrency-safe), run the
// tasks with bounded concurrency, check the guard, and report. Every task
// gets exactly one parTaskDoneMsg; parWaveDoneMsg is last.
func runParWave(ctx context.Context, d Deps, iso worktree.Isolator, guard *parallel.BaseGuard, tasks []waveTask, ch chan tea.Msg) {
	defer close(ch)
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	armErr := guard.Arm()

	trees := make([]worktree.Tree, len(tasks))
	errs := make([]error, len(tasks))
	for k, t := range tasks {
		switch {
		case armErr != nil:
			errs[k] = armErr
		case ctx.Err() != nil:
			errs[k] = ctx.Err()
		default:
			trees[k], errs[k] = iso.Add(t.id, "HEAD")
		}
	}

	reported := make([]bool, len(tasks))
	scheduler.Bounded(ctx, parallel.DefaultJobs, len(tasks), func(k int) error {
		t := tasks[k]
		reported[k] = true
		if errs[k] != nil {
			ch <- parTaskDoneMsg{id: t.id, err: errs[k]}
			return errs[k]
		}
		lw := &lineWriter{ch: ch, tag: func(lines []string) tea.Msg { return parOutputMsg{id: t.id, lines: lines} }}
		turn := parallel.RunStep(ctx, engine.Options{
			Agent: t.ag, Prompt: t.prompt, Dir: trees[k].Dir,
			Stages: d.Stages, MaxRetries: d.MaxRetries,
			Timeout: d.Timeout, Principles: d.Principles,
			OnEvent: func(e engine.Event) { send(parEventMsg{id: t.id, e: e}) },
			Output:  lw,
		})
		lw.flush()
		ch <- parTaskDoneMsg{id: t.id, turn: turn, tree: trees[k], created: true}
		return nil
	})
	// Tasks the scheduler never started (cancelled while queued).
	for k, t := range tasks {
		if !reported[k] {
			err := errs[k]
			if err == nil {
				err = context.Canceled
			}
			ch <- parTaskDoneMsg{id: t.id, tree: trees[k], created: errs[k] == nil, err: err}
		}
	}
	// Anything an agent wrote into the base instead of its tree is discarded
	// here — including after a cancel, so ctrl+c never strands stray edits.
	ch <- parWaveDoneMsg{stray: guard.Check()}
}

// onParMsg applies one worker message to the model.
func (m Model) onParMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.par
	if p == nil {
		return m, nil
	}
	rearm := waitRun(p.ch)
	switch msg := msg.(type) {
	case parPlannedMsg:
		return m.onPlanned(msg)
	case parEventMsg:
		if t := p.task(msg.id); t != nil {
			t.apply(msg.e, m.d.MaxRetries+1)
		}
	case parOutputMsg:
		if t := p.task(msg.id); t != nil {
			t.addLines(msg.lines...)
			if p.expanded && p.tasks[p.sel] == t {
				m.refreshParOutput()
			}
		}
	case parTaskDoneMsg:
		if t := p.task(msg.id); t != nil {
			t.finish(msg, p.cancelled)
			m.logEvent("turn", fmt.Sprintf("step %s %s", t.id, t.status))
		}
	case parWaveDoneMsg:
		return m.onWaveDone(msg)
	}
	return m, rearm
}

func (p *parRun) task(id string) *parTask {
	for _, t := range p.tasks {
		if t.id == id {
			return t
		}
	}
	return nil
}

// apply updates a task from one engine event.
func (t *parTask) apply(e engine.Event, defMax int) {
	switch e.Kind {
	case engine.EventAttempt:
		if t.start.IsZero() {
			t.start = time.Now()
		}
		t.attempt, t.max = e.Attempt, e.Max
		t.status = tsRunning
		if e.Attempt > 1 {
			t.status = tsRetrying
			t.addLines(fmt.Sprintf("── retry %d/%d: self-correcting ──", e.Attempt-1, e.Max-1))
		}
	case engine.EventStageStart:
		t.status = tsValidating
	}
	if t.max == 0 {
		t.max = defMax
	}
}

// finish records a task's final result (before review).
func (t *parTask) finish(msg parTaskDoneMsg, cancelled bool) {
	t.end = time.Now()
	t.turn, t.tree, t.created = msg.turn, msg.tree, msg.created
	t.err = msg.err
	if t.err == nil {
		t.err = msg.turn.Err
	}
	switch {
	case cancelled || errors.Is(t.err, context.Canceled):
		t.status = tsCancelled
	case t.err != nil:
		t.status = tsFailed
	case t.turn.ExitCode != 0:
		t.status = tsFailed
		t.err = fmt.Errorf("agent exited abnormally (code %d)", t.turn.ExitCode)
	default:
		t.status = tsDone
	}
}

func (m Model) onPlanned(msg parPlannedMsg) (tea.Model, tea.Cmd) {
	p := m.par
	p.iso, p.graph, p.plan, p.planAgent = msg.iso, msg.graph, msg.plan, msg.agent
	switch {
	case p.cancelled:
		return m.finishPar("↺ parallel run cancelled — nothing was changed")
	case msg.err != nil:
		m.logEvent("error", "planning failed: "+msg.err.Error())
		return m.finishPar("✗ planning failed: " + msg.err.Error())
	}
	p.guard = parallel.NewBaseGuard(m.d.Dir)
	m.messages = append(m.messages, chatLine{role: "agent", agent: "plan · " + msg.agent.Name(), text: planText(msg.plan)})
	m.logEvent("info", fmt.Sprintf("planned %d steps", len(msg.plan.Steps)))
	return m.startWave()
}

// planText renders a plan as a markdown list for the transcript.
func planText(pl planner.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d step%s:\n\n", len(pl.Steps), plural(len(pl.Steps)))
	for i, s := range pl.Steps {
		fmt.Fprintf(&b, "%d. **%s**", i+1, s.Title)
		if s.Agent != "" {
			fmt.Fprintf(&b, " — %s", s.Agent)
		}
		if len(s.DependsOn) > 0 {
			deps := make([]string, len(s.DependsOn))
			for j, d := range s.DependsOn {
				deps[j] = fmt.Sprint(d)
			}
			fmt.Fprintf(&b, " _(after %s)_", strings.Join(deps, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// startWave launches the next ready wave, or finishes when none is left.
func (m Model) startWave() (tea.Model, tea.Cmd) {
	p := m.par
	ready := p.graph.Ready()
	if len(ready) == 0 {
		return m.finishPar("")
	}
	p.wave++
	p.waves = p.wave - 1 + p.graph.WavesLeft()
	p.tasks = p.tasks[:0]
	p.sel, p.expanded, p.queue = 0, false, nil
	wt := make([]waveTask, len(ready))
	for k, i := range ready {
		step := p.plan.Steps[i]
		ag := parallel.StepAgent(m.d.Reg, step, p.planAgent, p.planAgent.Name())
		t := &parTask{idx: i, id: parallel.StepID(i), title: step.Title, prompt: parallel.StepTask(step),
			agent: ag.Name(), ag: ag, max: m.d.MaxRetries + 1}
		p.tasks = append(p.tasks, t)
		wt[k] = waveTask{id: t.id, ag: ag, prompt: t.prompt}
	}
	p.phase = parRunning
	p.ch = make(chan tea.Msg, 256)
	m.cstate = chatRunning
	m.ta.Blur()
	m.logEvent("turn", fmt.Sprintf("wave %d/%d: %d task(s)", p.wave, p.waves, len(ready)))
	go runParWave(p.ctx, m.d, p.iso, p.guard, wt, p.ch)
	m.layout()
	m.setChatContent()
	return m, tea.Batch(tickCmd(), waitRun(p.ch))
}

// onWaveDone triages the finished wave and opens the first review.
func (m Model) onWaveDone(msg parWaveDoneMsg) (tea.Model, tea.Cmd) {
	p := m.par
	p.expanded = false
	if msg.stray {
		m.messages = append(m.messages, chatLine{role: "sys",
			text: "↺ an agent wrote outside its isolated tree — stray changes in the base were discarded"})
		m.logEvent("error", "stray writes in the base discarded")
	}
	if p.cancelled {
		for _, t := range p.tasks {
			if t.status != tsCancelled && t.status != tsFailed {
				t.status = tsCancelled
			}
			if !t.start.IsZero() {
				m.record(usageRun(memory.Run{Agent: t.agent, Prompt: t.prompt, Outcome: "cancelled", Attempts: t.attempt}, t.turn.Usage))
			}
		}
		return m.finishPar("↺ parallel run cancelled — isolated trees removed, any stray changes reverted")
	}
	for k, t := range p.tasks {
		switch {
		case t.status == tsFailed:
			m.messages = append(m.messages, chatLine{role: "sys",
				text: fmt.Sprintf("✗ step %s (%s) failed: %v — skipped", t.id, t.title, t.err)})
			p.graph.MarkDead(t.idx)
			m.dropTree(t)
			m.recordTask(t, "failed", "")
		case !t.turn.HadChanges:
			t.status = tsNoChanges
			m.messages = append(m.messages, chatLine{role: "sys",
				text: fmt.Sprintf("○ step %s (%s) made no changes", t.id, t.title)})
			p.graph.MarkDone(t.idx)
			m.dropTree(t)
			m.recordTask(t, "no-change", "")
		default:
			p.queue = append(p.queue, k)
		}
	}
	if len(p.queue) > 0 {
		ui.Notify("Orchestra", fmt.Sprintf("Wave %d done — %d task(s) to review", p.wave, len(p.queue)))
	}
	return m.nextReview()
}

// nextReview opens the next queued task in the reviewer, or moves on.
func (m Model) nextReview() (tea.Model, tea.Cmd) {
	p := m.par
	for len(p.queue) > 0 {
		k := p.queue[0]
		p.queue = p.queue[1:]
		t := p.tasks[k]
		diff, err := p.iso.Diff(t.tree)
		if err != nil || diff == "" {
			if err == nil {
				err = errors.New("no diff in the isolated tree")
			}
			t.status, t.err = tsFailed, err
			m.messages = append(m.messages, chatLine{role: "sys",
				text: fmt.Sprintf("✗ step %s (%s): %v — skipped", t.id, t.title, err)})
			p.graph.MarkDead(t.idx)
			m.dropTree(t)
			m.recordTask(t, "failed", "")
			continue
		}
		t.turn.Diff = diff
		p.reviewing = k
		p.phase = parReviewing
		rep := t.turn.Report
		m.rv = newReviewer(diff, reviewMeta{
			Agent: t.agent, Task: t.prompt, Attempts: t.turn.Attempts, Max: m.d.MaxRetries + 1, Report: &rep,
			Context: fmt.Sprintf("wave %d/%d · task %d/%d", p.wave, p.waves, k+1, len(p.tasks)),
		}, m.width, m.bodyHeight())
		m.cstate = chatReviewing
		m.layout()
		m.setChatContent()
		return m, nil
	}
	return m.startWave()
}

// parAccept merges the task under review into the base.
func (m Model) parAccept() (tea.Model, tea.Cmd) {
	p := m.par
	t := p.tasks[p.reviewing]
	conflict, err := p.iso.Merge(t.tree, fmt.Sprintf("Merge step %d: %s", t.idx+1, t.title))
	switch {
	case err != nil:
		t.status = tsFailed
		p.graph.MarkDead(t.idx)
		m.messages = append(m.messages, chatLine{role: "sys",
			text: fmt.Sprintf("✗ step %s (%s): merge failed: %v", t.id, t.title, err)})
		m.recordTask(t, "failed", t.turn.Diff)
	case conflict:
		t.status = tsConflict
		p.graph.MarkDead(t.idx)
		m.messages = append(m.messages, chatLine{role: "sys",
			text: fmt.Sprintf("✗ step %s (%s): merge conflict with work already merged — left unmerged; dependent steps will be skipped", t.id, t.title)})
		m.recordTask(t, "rejected", t.turn.Diff)
	default:
		t.status = tsMerged
		p.graph.MarkDone(t.idx)
		m.messages = append(m.messages, chatLine{role: "sys",
			text: fmt.Sprintf("✓ step %s (%s) merged", t.id, t.title)})
		m.recordTask(t, "accepted", t.turn.Diff)
	}
	m.dropTree(t)
	return m.nextReview()
}

// parReject discards the task under review.
func (m Model) parReject() (tea.Model, tea.Cmd) {
	p := m.par
	t := p.tasks[p.reviewing]
	t.status = tsRejected
	p.graph.MarkDead(t.idx)
	m.messages = append(m.messages, chatLine{role: "sys",
		text: fmt.Sprintf("↺ step %s (%s) rejected — discarded", t.id, t.title)})
	m.recordTask(t, "rejected", t.turn.Diff)
	m.dropTree(t)
	return m.nextReview()
}

func (m *Model) dropTree(t *parTask) {
	if t.created && m.par.iso != nil {
		_ = m.par.iso.Remove(t.tree)
		t.created = false
	}
}

func (m *Model) recordTask(t *parTask, outcome, diff string) {
	m.record(usageRun(memory.Run{Agent: t.agent, Prompt: t.prompt, Outcome: outcome,
		Attempts: t.turn.Attempts, Passed: t.turn.Report.Passed(), Diff: diff}, t.turn.Usage))
}

// finishPar ends the run: note blocked steps, clean up every isolated tree,
// summarise. note overrides the summary (cancel / planning failure).
func (m Model) finishPar(note string) (tea.Model, tea.Cmd) {
	p := m.par
	if p.iso != nil {
		p.iso.Cleanup()
	}
	if note == "" && p.graph != nil {
		if blocked := p.graph.Blocked(); len(blocked) > 0 {
			var ids []string
			for _, i := range blocked {
				ids = append(ids, fmt.Sprintf("%d (%s)", i+1, p.plan.Steps[i].Title))
			}
			m.messages = append(m.messages, chatLine{role: "sys",
				text: "○ skipped — a prerequisite was not merged: step " + strings.Join(ids, ", step ")})
		}
		merged, total := p.graph.Merged(), p.graph.Len()
		if merged == total {
			note = fmt.Sprintf("✓ parallel run complete — all %d steps merged", total)
		} else {
			note = fmt.Sprintf("↺ parallel run done — %d/%d steps merged (others rejected, failed, or blocked)", merged, total)
		}
	}
	m.messages = append(m.messages, chatLine{role: "sys", text: note})
	m.logEvent("turn", note)
	p.phase = parFinished
	p.expanded = false
	p.cancel()
	m.cstate = chatIdle
	if m.active == tabChat {
		m.ta.Focus()
	}
	m.layout()
	m.setChatContent()
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

// cancelPar asks every task (or the planner) to stop; the worker reports back.
func (m *Model) cancelPar(status string) {
	if m.par.cancelled {
		return
	}
	m.par.cancelled = true
	m.par.cancel()
	m.setStatus(status)
}

// quitPar is ctrl+c while a parallel run waits for review: drop every
// isolated tree (merged work stays — it was accepted) and quit.
func (m Model) quitPar() (tea.Model, tea.Cmd) {
	if m.par.iso != nil {
		m.par.iso.Cleanup()
	}
	m.par.cancel()
	return m, tea.Quit
}

// updateParRunning handles keys while planning or while a wave runs.
func (m Model) updateParRunning(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.par
	if p.expanded {
		switch msg.String() {
		case "esc", "enter", "tab":
			p.expanded = false
			return m, nil
		case "up", "down", "pgup", "pgdown", "j", "k", "g", "G", "home", "end":
			switch msg.String() {
			case "g", "home":
				p.out.GotoTop()
			case "G", "end":
				p.out.GotoBottom()
			default:
				var cmd tea.Cmd
				p.out, cmd = p.out.Update(msg)
				return m, cmd
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.cancelPar("cancelling the parallel run…")
	case "up", "k":
		if p.sel > 0 {
			p.sel--
		}
	case "down", "j":
		if p.sel < len(p.tasks)-1 {
			p.sel++
		}
	case "enter", "tab":
		if len(p.tasks) > 0 {
			p.expanded = true
			m.layout()
			m.refreshParOutput()
			p.out.GotoBottom()
		}
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// refreshParOutput loads the selected task's output into the expanded
// viewport, following the tail when it was already at the bottom.
func (m *Model) refreshParOutput() {
	p := m.par
	if p.sel >= len(p.tasks) {
		return
	}
	follow := p.out.AtBottom() || p.out.TotalLineCount() == 0
	t := p.tasks[p.sel]
	lines := make([]string, 0, len(t.lines))
	for _, l := range t.lines {
		lines = append(lines, ansiTruncate(l, max(p.out.Width, 1)))
	}
	if len(lines) == 0 {
		lines = []string{dimSty.Render("waiting for output…")}
	}
	p.out.SetContent(strings.Join(lines, "\n"))
	if follow {
		p.out.GotoBottom()
	}
}

// --- rendering ---

func taskGlyph(s taskStatus, frame int) string {
	switch s {
	case tsQueued:
		return dimSty.Render("○")
	case tsRunning, tsValidating, tsRetrying:
		return warnSty.Render(spin(frame))
	case tsDone, tsMerged:
		return okSty.Render("✓")
	case tsNoChanges:
		return dimSty.Render("○")
	case tsRejected, tsCancelled:
		return warnSty.Render("↺")
	}
	return badSty.Render("✗")
}

func taskStatusStyle(s taskStatus) lipgloss.Style {
	switch s {
	case tsRunning, tsValidating, tsRetrying, tsRejected, tsCancelled:
		return warnSty
	case tsDone, tsMerged:
		return okSty
	case tsFailed, tsConflict:
		return badSty
	}
	return dimSty
}

// taskRow renders one task in two lines of width w: the summary, then the
// last line of its output. On narrow rows the agent and attempt are dropped
// before the title is squeezed.
func (m Model) taskRow(t *parTask, selected bool, w int) string {
	marker := "  "
	if selected {
		marker = selSty.Render("▸ ")
	}
	prefix := marker + taskGlyph(t.status, m.frame) + " " + dimSty.Render(t.id) + " "
	st := taskStatusStyle(t.status).Render(t.status.String())
	var el string
	if !t.start.IsZero() {
		el = fmtElapsed(t.elapsed())
	}
	full := []string{t.agent}
	if t.attempt > 0 {
		full = append(full, fmt.Sprintf("%d/%d", t.attempt, t.max))
	}
	if el != "" {
		full = append(full, el)
	}
	right := st + dimSty.Render(" · "+strings.Join(full, " · "))
	titleW := w - lipgloss.Width(prefix) - lipgloss.Width(right) - 1
	if titleW < 12 {
		right = st
		if el != "" {
			right += dimSty.Render(" · " + el)
		}
		titleW = w - lipgloss.Width(prefix) - lipgloss.Width(right) - 1
	}
	if titleW < 4 {
		right = st
		titleW = w - lipgloss.Width(prefix) - lipgloss.Width(right) - 1
	}
	title := ansiTruncate(t.title, max(titleW, 1))
	if selected {
		title = selSty.Render(title)
	}
	line1 := spread(prefix+title, right, w)

	sub := t.last()
	switch {
	case t.err != nil && (t.status == tsFailed || t.status == tsConflict):
		sub = badSty.Render(ansiTruncate(t.err.Error(), max(w-4, 1)))
	case sub == "" && t.status == tsQueued:
		sub = dimSty.Render("waiting for a slot…")
	case sub == "":
		sub = dimSty.Render("…")
	default:
		sub = dimSty.Render(ansiTruncate(sub, max(w-4, 1)))
	}
	return ansiTruncate(line1, w) + "\n" + "    " + sub
}

// parPanel renders the task list for the current wave in a w×h box.
func (m Model) parPanel(w, h int) string {
	p := m.par
	inner := max(w-4, 10)
	var head string
	elapsed := fmtElapsed(time.Since(p.start))
	switch p.phase {
	case parPlanning:
		agentName := m.d.DefaultAgent
		head = spread(warnSty.Render(spin(m.frame)+" planning")+dimSty.Render(" with ")+youSty.Render(agentName),
			dimSty.Render(elapsed), inner)
	case parRunning:
		head = spread(warnSty.Render(spin(m.frame)+fmt.Sprintf(" wave %d/%d", p.wave, p.waves))+
			dimSty.Render(fmt.Sprintf(" · %d task%s", len(p.tasks), plural(len(p.tasks)))), dimSty.Render(elapsed), inner)
	case parReviewing:
		head = okSty.Render(fmt.Sprintf("● wave %d/%d", p.wave, p.waves)) + dimSty.Render(" · reviewing")
	default:
		merged, total := 0, 0
		if p.graph != nil {
			merged, total = p.graph.Merged(), p.graph.Len()
		}
		head = dimSty.Render(fmt.Sprintf("finished · %d/%d steps merged", merged, total))
	}
	body := []string{head}
	if p.phase == parPlanning {
		body = append(body, "", dimSty.Render("breaking the request into steps…"))
		return panelBox("Parallel", strings.Join(body, "\n"), w, h, accent)
	}
	room := (h - 2 - 1 - 1) / 2 // border, title, head; two lines per task
	start, end := listWindow(len(p.tasks), p.sel, max(room, 1))
	for i := start; i < end; i++ {
		body = append(body, m.taskRow(p.tasks[i], i == p.sel && p.phase == parRunning, inner))
	}
	return panelBox("Parallel", strings.Join(body, "\n"), w, h, accent)
}

// parExpandedView is the selected task's full output, filling the body.
func (m Model) parExpandedView() string {
	p := m.par
	t := p.tasks[p.sel]
	w := m.width
	st := taskStatusStyle(t.status).Render(t.status.String())
	meta := []string{t.agent}
	if t.attempt > 0 {
		meta = append(meta, fmt.Sprintf("attempt %d/%d", t.attempt, t.max))
	}
	if !t.start.IsZero() {
		meta = append(meta, fmtElapsed(t.elapsed()))
	}
	head := taskGlyph(t.status, m.frame) + " " + headSty.Render("step "+t.id) + " " + ansiTruncate(t.title, max(w/2, 10))
	line := spread(head, st+dimSty.Render(" · "+strings.Join(meta, " · ")), w)
	pane := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(p.out.Width + 2).Height(p.out.Height).Render(p.out.View())
	return ansiTruncate(line, w) + "\n" + pane
}

// parStackedHeight is the task list's height below the chat on narrow
// terminals: every task if it fits, at most two thirds of the body.
func (m Model) parStackedHeight() int {
	want := 4 + 2*max(len(m.par.tasks), 1) // border, title, head + two lines per task
	return max(min(want, m.bodyHeight()*2/3), 4)
}
