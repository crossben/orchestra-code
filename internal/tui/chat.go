package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/ui"
)

const (
	sideMinWidth = 110 // below this the run panel stacks instead of sitting beside the chat
	inputHeight  = 5   // textarea (3) + border
)

func (m Model) wide() bool { return m.width >= sideMinWidth }

func (m Model) sideWidth() int { return min(max(m.width*36/100, 40), 64) }

func (m Model) mainWidth() int {
	if m.wide() {
		return m.width - m.sideWidth() - 1
	}
	return m.width
}

func (m Model) stackedPanelHeight() int { return min(14, m.bodyHeight()/2) }

// layout sizes the transcript, input and any open reviewer to the window.
func (m *Model) layout() {
	bodyH := m.bodyHeight()
	mw := m.mainWidth()
	vpH := bodyH - inputHeight
	if m.cstate == chatRunning && !m.wide() {
		vpH = bodyH - m.stackedPanelHeight()
	}
	m.vp.Width = mw
	m.vp.Height = max(vpH, 3)
	m.ta.SetWidth(max(mw-4, 10))
	if m.cstate == chatReviewing {
		m.rv.setSize(m.width, bodyH)
	}
	if m.browsing {
		m.browse.setSize(m.width, bodyH)
	}
}

// setChatContent refreshes the transcript viewport, keeping it pinned to the
// newest message.
func (m *Model) setChatContent() {
	m.vp.SetContent(m.renderTranscript())
	m.vp.GotoBottom()
}

func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.cstate {
	case chatReviewing:
		switch msg.String() {
		case "y", "Y":
			return m.accept()
		case "n", "N":
			return m.reject()
		}
		var cmd tea.Cmd
		m.rv, cmd, _ = m.rv.update(msg)
		return m, cmd
	case chatRunning:
		switch msg.String() {
		case "esc":
			if m.run != nil && !m.run.cancelled {
				m.run.cancelled = true
				m.run.cancel()
				m.setStatus("cancelling…")
			}
			return m, nil
		case "pgup", "pgdown", "up", "down":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	// idle
	switch msg.String() {
	case "enter":
		return m.submitChat()
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case "esc":
		return m, nil
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
	m.ta.Blur()
	m.messages = append(m.messages, chatLine{role: "you", text: text})
	m.cstate = chatRunning
	m.frame = 0
	m.status = ""
	cmd := m.startRun(text)
	m.logEvent("info", "task: "+firstLine(text))
	m.layout()
	m.setChatContent()
	return m, cmd
}

// onTurn handles the worker's final message.
func (m Model) onTurn(msg turnMsg) (tea.Model, tea.Cmd) {
	run, t := m.run, msg.turn
	agentName := msg.agent
	if agentName == "" {
		agentName = run.agent
	}
	m.cstate = chatIdle

	switch {
	case run.cancelled:
		run.result = "cancelled"
		text := "↺ cancelled"
		if msg.started {
			if err := engine.Revert(m.d.Dir, t.Baseline); err != nil {
				text = "↺ cancelled — restoring the working tree failed: " + err.Error()
			} else {
				text = "↺ cancelled — any changes were reverted"
			}
			m.record(memory.Run{Agent: agentName, Prompt: run.task, Outcome: "cancelled", Attempts: t.Attempts})
		}
		m.messages = append(m.messages, chatLine{role: "sys", text: text})
		m.logEvent("turn", "run cancelled")
	case t.Err != nil:
		run.result = "failed"
		m.messages = append(m.messages, chatLine{role: "sys", text: "✗ " + t.Err.Error()})
		m.logEvent("error", t.Err.Error())
		if msg.started {
			m.record(memory.Run{Agent: agentName, Prompt: run.task, Outcome: "failed", Attempts: t.Attempts})
		}
	case msg.answered:
		run.result = "answered"
		run.agent = ""
		m.messages = append(m.messages, chatLine{role: "agent", text: cleanText(t.AgentText), agent: "orchestra"})
		m.logEvent("turn", "question answered")
		ui.Notify("Orchestra", "Answered — "+firstLine(cleanText(t.AgentText)))
	case !t.HadChanges:
		run.result = "no changes"
		resp := cleanText(t.AgentText)
		if resp == "" {
			resp = "(the agent made no file changes)"
		}
		m.messages = append(m.messages, chatLine{role: "agent", text: resp, agent: agentName})
		m.record(memory.Run{Agent: agentName, Prompt: run.task, Outcome: "no-change", Attempts: t.Attempts, Passed: t.Report.Passed()})
		m.logEvent("turn", fmt.Sprintf("%s replied without changes", agentName))
		ui.Notify("Orchestra", "Agent finished — "+firstLine(resp))
	default:
		run.result = "review"
		m.cstate = chatReviewing
		m.pending, m.pendAg, m.pendTask = t, agentName, run.task
		rep := t.Report
		m.rv = newReviewer(t.Diff, reviewMeta{
			Agent: agentName, Task: run.task, Attempts: t.Attempts,
			Max: m.d.MaxRetries + 1, Report: &rep,
		}, m.width, m.bodyHeight())
		m.messages = append(m.messages, chatLine{role: "sys", text: fmt.Sprintf("● %s changed %d file%s — review required",
			agentName, len(m.rv.files), plural(len(m.rv.files)))})
		m.logEvent("turn", agentName+" produced changes — review required")
		ui.Notify("Orchestra", "Agent produced changes — review required")
	}
	if m.cstate == chatIdle && m.active == tabChat {
		m.ta.Focus()
	}
	m.layout()
	m.setChatContent()
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) accept() (tea.Model, tea.Cmd) {
	committed, err := engine.CommitAccepted(m.d.Dir, "orchestra: "+firstLine(m.pendTask))
	switch {
	case err != nil:
		m.messages = append(m.messages, chatLine{role: "sys", text: "✗ commit failed: " + err.Error()})
	case committed:
		m.messages = append(m.messages, chatLine{role: "sys", text: "✓ accepted & committed"})
	default:
		m.messages = append(m.messages, chatLine{role: "sys", text: "✓ accepted — kept as plain files (not a git repo)"})
	}
	return m.finishReview("accepted"), nil
}

func (m Model) reject() (tea.Model, tea.Cmd) {
	if err := engine.Revert(m.d.Dir, m.pending.Baseline); err != nil {
		m.messages = append(m.messages, chatLine{role: "sys", text: "✗ restore failed: " + err.Error()})
	} else {
		m.messages = append(m.messages, chatLine{role: "sys", text: "↺ rejected & reverted"})
	}
	return m.finishReview("rejected"), nil
}

func (m Model) finishReview(outcome string) Model {
	t := m.pending
	m.record(memory.Run{
		Agent: m.pendAg, Prompt: m.pendTask, Outcome: outcome,
		Attempts: t.Attempts, Passed: t.Report.Passed(), Diff: t.Diff,
	})
	m.logEvent("turn", outcome)
	m.cstate = chatIdle
	m.pending = engine.Turn{}
	if m.active == tabChat {
		m.ta.Focus()
	}
	m.layout()
	m.setChatContent()
	return m
}

// --- rendering ---

func (m Model) chatView() string {
	if m.cstate == chatReviewing {
		return m.rv.view()
	}
	bodyH := m.bodyHeight()
	mw := m.mainWidth()

	var bottom string
	switch {
	case m.cstate == chatRunning && !m.wide():
		bottom = m.runPanel(mw, m.stackedPanelHeight())
	case m.cstate == chatRunning:
		bottom = panelBox("", warnSty.Render(spin(m.frame))+dimSty.Render(" the agent is working — follow along on the right · esc cancels"),
			mw, inputHeight, faint)
	default:
		bottom = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
			Padding(0, 1).Width(max(mw-2, 12)).Render(m.ta.View())
	}
	main := m.vp.View() + "\n" + bottom
	if !m.wide() {
		return main
	}
	var side string
	if m.run != nil {
		side = m.runPanel(m.sideWidth(), bodyH)
	} else {
		side = m.sessionCard(m.sideWidth(), bodyH)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, padLines(main, bodyH, mw), " ", side)
}

func (m Model) renderTranscript() string {
	w := max(m.vp.Width, 20)
	if len(m.messages) == 0 {
		return lipgloss.Place(w, max(m.vp.Height, 3), lipgloss.Center, lipgloss.Center, m.welcome())
	}
	var blocks []string
	for _, msg := range m.messages {
		switch msg.role {
		case "you":
			blocks = append(blocks, turnBlock(youSty.Render("you"), lipgloss.NewStyle().Width(w-3).Render(msg.text), accent2, w))
		case "agent":
			label := msg.agent
			if label == "" {
				label = "agent"
			}
			blocks = append(blocks, turnBlock(titleSty.Render(label), renderMarkdown(msg.text, w-3), accent, w))
		case "sys":
			blocks = append(blocks, sysLine(msg.text, w))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// turnBlock renders a message with a coloured bar down its left edge.
func turnBlock(label, body string, bar lipgloss.TerminalColor, w int) string {
	return lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(bar).PaddingLeft(1).Width(w - 1).Render(label + "\n" + body)
}

// sysLine colours a system note by its leading glyph.
func sysLine(text string, w int) string {
	st := dimSty
	switch {
	case strings.HasPrefix(text, "✓"):
		st = okSty
	case strings.HasPrefix(text, "✗"):
		st = badSty
	case strings.HasPrefix(text, "↺"), strings.HasPrefix(text, "●"):
		st = warnSty
	}
	// Padding (not a literal indent) keeps wrapped lines aligned under the first.
	return lipgloss.NewStyle().Width(w).PaddingLeft(2).Render(st.Render(text))
}

func (m Model) welcome() string {
	lines := []string{
		titleSty.Render("What should we build?"),
		"",
		dimSty.Render("Describe a change and an agent makes it. You watch it work,"),
		dimSty.Render("the checks run, and nothing is kept until you accept the diff."),
		"",
		keySty.Render("try  ") + "add a /health endpoint with a test",
	}
	if m.d.RoutingOn && m.d.Router != nil {
		lines = append(lines, keySty.Render("     ")+"why is the build slow?"+dimSty.Render("  (questions get answers)"))
	}
	return strings.Join(lines, "\n")
}

// sessionCard fills the side panel before the first run.
func (m Model) sessionCard(w, h int) string {
	row := func(k, v string) string { return fit(dimSty.Render(k), 10) + v }
	folder := m.repoName
	switch {
	case m.branch != "":
		folder += "  " + youSty.Render("⎇ "+m.branch)
	case !m.inRepo:
		folder += dimSty.Render("  plain folder")
	}
	routing := "fixed: " + youSty.Render(m.d.DefaultAgent)
	if m.d.RoutingOn && m.d.Router != nil {
		routing = okSty.Render("AI") + " picks the agent"
	}
	total := 0
	if m.d.Reg != nil {
		total = len(m.d.Reg.All())
	}
	checks := warnSty.Render("none — changes are unverified")
	var names []string
	for _, st := range m.d.Stages {
		if strings.TrimSpace(st.Command) != "" {
			names = append(names, st.Name)
		}
	}
	if len(names) > 0 {
		checks = strings.Join(names, dimSty.Render(" → "))
	}
	lines := []string{
		row("folder", folder),
		row("routing", routing),
		row("agents", fmt.Sprintf("%d of %d installed", m.installed, total)),
		row("checks", checks),
		row("retries", fmt.Sprintf("%d", m.d.MaxRetries)+dimSty.Render(fmt.Sprintf("  ·  timeout %s", m.d.Timeout))),
		"",
		headSty.Render("How it works"),
		dimSty.Render("1 ") + "you describe a change",
		dimSty.Render("2 ") + "an agent edits the files",
		dimSty.Render("3 ") + "checks run; failures retry",
		dimSty.Render("4 ") + "you review the diff: " + okSty.Render("y") + dimSty.Render(" / ") + badSty.Render("n"),
	}
	return panelBox("Session", strings.Join(lines, "\n"), w, h, faint)
}
