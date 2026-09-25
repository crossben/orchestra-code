package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/validate"
)

func testModelIn(dir string, reg *agent.Registry, stages []validate.Stage, w, h int) Model {
	if reg == nil {
		reg = agent.NewRegistry()
		reg.Add(agent.New("alpha", "true", nil, "", []agent.Capability{agent.CapImplement}))
		reg.Add(agent.New("beta", "true", nil, "", []agent.Capability{agent.CapReview}))
	}
	cfg := &config.Config{DefaultAgent: "alpha"}
	m := New(Deps{Ctx: context.Background(), Cfg: cfg, Reg: reg, Dir: dir, DefaultAgent: "alpha",
		Stages: stages, MaxRetries: 2, Timeout: 30 * time.Second})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return nm.(Model)
}

func testModel() Model { return testModelIn(".", nil, nil, 100, 30) }

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		nm, _ := m.Update(keyMsg(k))
		m = nm.(Model)
	}
	return m
}

func TestOpensOnChat(t *testing.T) {
	m := testModel()
	if m.active != tabChat {
		t.Fatalf("dashboard should open on Chat, got tab %d", m.active)
	}
	v := m.View()
	for _, want := range []string{"ORCHESTRA", "1 Chat", "2 Changes", "What should we build?", "READY", "enter send"} {
		if !strings.Contains(v, want) {
			t.Fatalf("chat view missing %q\n---\n%s", want, v)
		}
	}
}

// Every frame is exactly the window height, and no line is wider than it.
func TestViewFitsWindow(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m := testModelIn(".", nil, nil, size[0], size[1])
		for tb := tab(0); tb < numTabs; tb++ {
			m = m.switchTab(tb)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size[1] {
				t.Fatalf("%dx%d tab %s: %d lines", size[0], size[1], tabNames[tb], len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > size[0] {
					t.Fatalf("%dx%d tab %s line %d is %d wide:\n%s", size[0], size[1], tabNames[tb], i, w, l)
				}
			}
		}
	}
}

func TestViewRendersAgentsTab(t *testing.T) {
	m := testModel().switchTab(tabAgents)
	view := m.View()
	for _, want := range []string{"Agents", "History", "Benchmarks", "AGENT", "alpha ★", "beta", "p probe", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agents view missing %q\n---\n%s", want, view)
		}
	}
}

func TestTabSwitching(t *testing.T) {
	m := press(testModel().switchTab(tabAgents), "3")
	if m.active != tabHistory || !strings.Contains(m.View(), "No run history yet") {
		t.Fatalf("expected History empty state, tab %d:\n%s", m.active, m.View())
	}
	m = press(m, "tab")
	if m.active != tabAgents {
		t.Fatalf("tab should move to Agents, got %d", m.active)
	}
	m = press(m, "5")
	if !strings.Contains(m.View(), "No benchmarks yet") {
		t.Fatalf("benchmarks empty state missing:\n%s", m.View())
	}
	m = press(m, "1")
	if m.active != tabChat {
		t.Fatalf("1 should select Chat, got %d", m.active)
	}
}

func TestQuitKeyOutsideChat(t *testing.T) {
	m := testModel().switchTab(tabAgents)
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q should quit outside chat")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q should return tea.QuitMsg, got %T", cmd())
	}
}

// In Chat, q and digits are text, not shortcuts.
func TestChatTypingIsText(t *testing.T) {
	m := press(testModel(), "q", "1", "x")
	if m.ta.Value() != "q1x" || m.active != tabChat {
		t.Fatalf("typing should stay in the input: value=%q tab=%d", m.ta.Value(), m.active)
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := nm.(Model).ta.Value(); got != "q1" {
		t.Fatalf("backspace failed, got %q", got)
	}
}

func TestWindowResizeApplied(t *testing.T) {
	nm, _ := testModel().Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := nm.(Model)
	if m.width != 120 || m.height != 40 || !m.wide() {
		t.Fatalf("resize not applied: %dx%d wide=%v", m.width, m.height, m.wide())
	}
}

func TestChatEmptySubmitNoop(t *testing.T) {
	_, cmd := testModel().Update(keyMsg("enter"))
	if cmd != nil {
		t.Fatal("submitting empty input should not launch a command")
	}
}

func TestChatTabNavigatesWhileTyping(t *testing.T) {
	m := press(testModel(), "hello", "tab")
	if m.active == tabChat {
		t.Fatal("tab should navigate out of chat while typing")
	}
	if m.ta.Value() != "hello" {
		t.Fatalf("input should be preserved on nav, got %q", m.ta.Value())
	}
}

func TestChatSubmitEntersRunning(t *testing.T) {
	m := testModel()
	m.ta.SetValue("do a thing")
	nm, cmd := m.submitChat()
	m = nm.(Model)
	defer m.run.cancel()
	if m.cstate != chatRunning || cmd == nil || m.run == nil {
		t.Fatalf("expected running state with a command, got state=%d", m.cstate)
	}
	if len(m.messages) != 1 || m.messages[0].role != "you" {
		t.Fatalf("user message not recorded: %+v", m.messages)
	}
	if v := m.View(); !strings.Contains(v, "RUNNING") || !strings.Contains(v, "esc cancel") {
		t.Fatalf("running view should show mode and cancel hint:\n%s", v)
	}
}

func TestOnTurnStates(t *testing.T) {
	running := func() Model {
		m := testModel()
		m.cstate = chatRunning
		m.run = &liveRun{task: "x", start: time.Now(), agent: "alpha"}
		return m
	}
	// no changes → idle + agent message, recorded as no-change
	nm, _ := running().onTurn(turnMsg{turn: engine.Turn{AgentText: "here is an explanation"}, agent: "alpha", started: true})
	m := nm.(Model)
	if m.cstate != chatIdle || m.messages[len(m.messages)-1].role != "agent" || m.runs[0].Outcome != "no-change" {
		t.Fatalf("no-change turn: state=%d runs=%+v", m.cstate, m.runs)
	}
	// changes → reviewing with the routed agent's name
	nm, _ = running().onTurn(turnMsg{turn: engine.Turn{HadChanges: true, Attempts: 1,
		Diff: "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-x\n+y\n"}, agent: "beta", started: true})
	m = nm.(Model)
	if m.cstate != chatReviewing || m.pendAg != "beta" || len(m.rv.files) != 1 {
		t.Fatalf("changed turn should review beta's diff, got state=%d agent=%q", m.cstate, m.pendAg)
	}
	// error before running → idle + sys message, nothing recorded
	nm, _ = running().onTurn(turnMsg{turn: engine.Turn{Err: errDirty}})
	m = nm.(Model)
	if m.cstate != chatIdle || m.messages[len(m.messages)-1].role != "sys" || len(m.runs) != 0 {
		t.Fatal("error turn should go idle with a sys message and no history")
	}
}

func TestParseDiff(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package x
+// ---- not a header
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
diff --git a/m.go b/m.go
--- a/m.go
+++ b/m.go
@@ -1,2 +1,2 @@
 keep
-old
+new
`
	files := parseDiff(diff)
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d: %+v", len(files), files)
	}
	want := []struct {
		path   string
		status byte
		a, d   int
		binary bool
	}{{"new.go", 'A', 2, 0, false}, {"old.txt", 'D', 0, 1, false}, {"img.png", 'M', 0, 0, true}, {"m.go", 'M', 1, 1, false}}
	for i, w := range want {
		f := files[i]
		if f.Path != w.path || f.Status != w.status || f.Added != w.a || f.Removed != w.d || f.Binary != w.binary {
			t.Fatalf("file %d = %+v, want %+v", i, f, w)
		}
	}
	if a, d := diffTotals(files); a != 3 || d != 2 {
		t.Fatalf("totals = +%d -%d", a, d)
	}
}

func TestReviewerNavigation(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-1\n+2\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1,2 @@\n x\n+y\n"
	rep := validate.Report{Stages: []validate.StageResult{{Name: "build", Passed: true}, {Name: "test", Passed: false, Output: "FAIL TestX\n"}}}
	r := newReviewer(diff, reviewMeta{Agent: "alpha", Attempts: 3, Max: 3, Report: &rep}, 120, 30)
	v := r.view()
	for _, want := range []string{"REVIEW", "alpha", "attempt 3/3", "build ✓", "test ✗", "2 files", "files", "a.go", "b.go", "FAIL TestX"} {
		if !strings.Contains(v, want) {
			t.Fatalf("review view missing %q\n%s", want, v)
		}
	}
	if len(strings.Split(v, "\n")) != 30 {
		t.Fatalf("review view should fill its height, got %d lines", len(strings.Split(v, "\n")))
	}
	r, _, _ = r.update(keyMsg("]"))
	if r.sel != 1 || !strings.Contains(r.view(), "2/2  b.go") {
		t.Fatalf("] should select the second file:\n%s", r.view())
	}
	r, _, _ = r.update(keyMsg("a"))
	if !r.all || r.showList() {
		t.Fatal("a should switch to the all-files view")
	}
	if _, _, handled := r.update(keyMsg("y")); handled {
		t.Fatal("y must be left to the caller (accept)")
	}
}

func TestFitAlignsStyledCells(t *testing.T) {
	for _, s := range []string{okSty.Render("✓"), badSty.Render("✗ " + strings.Repeat("x", 40)), "plain"} {
		if w := lipgloss.Width(fit(s, 12)); w != 12 {
			t.Fatalf("fit(%q) width = %d, want 12", s, w)
		}
	}
}

func TestLiveRunPanel(t *testing.T) {
	m := testModelIn(".", nil, []validate.Stage{{Name: "build", Command: "x"}, {Name: "test", Command: "y"}}, 140, 40)
	m.cstate = chatRunning
	m.run = &liveRun{start: time.Now(), agent: "alpha", reason: "implementation"}
	for _, e := range []engine.Event{
		{Kind: engine.EventAttempt, Attempt: 1, Max: 3},
		{Kind: engine.EventAgentDone, Attempt: 1, Max: 3, ExitCode: 0, Duration: time.Second},
		{Kind: engine.EventStageStart, Stage: "build"},
		{Kind: engine.EventStageDone, Stage: "build", Passed: false},
		{Kind: engine.EventAttempt, Attempt: 2, Max: 3},
		{Kind: engine.EventStageStart, Stage: "build"},
	} {
		m.applyEvent(e)
	}
	m.run.addLines("compiling…", "wrote main.go")
	v := m.View()
	for _, want := range []string{"Run", "routed → alpha", "attempt 2/3", "agent working", "build", "test", "retry 1/2", "wrote main.go"} {
		if !strings.Contains(v, want) {
			t.Fatalf("run panel missing %q\n%s", want, v)
		}
	}
}

func TestLineWriterAndCleanLine(t *testing.T) {
	ch := make(chan tea.Msg, 8)
	w := &lineWriter{ch: ch}
	w.Write([]byte("\x1b[32mhello\x1b[0m\npart"))
	w.Write([]byte("ial\r50%\r100%\nlast"))
	w.flush()
	close(ch)
	var got []string
	for msg := range ch {
		got = append(got, msg.(outputMsg).lines...)
	}
	if strings.Join(got, "|") != "hello|100%|last" {
		t.Fatalf("lines = %q", got)
	}
}

func TestHistoryEnterOpensReview(t *testing.T) {
	m := testModel()
	m.runs = []memory.Run{
		{Agent: "alpha", Prompt: "plain run", Outcome: "no-change"},
		{Agent: "beta", Prompt: "with diff", Outcome: "accepted", Diff: "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n"},
	}
	m.reload()
	m = press(m.switchTab(tabHistory), "enter")
	if m.browsing || !strings.Contains(m.status, "no diff") {
		t.Fatal("a run without a diff should not open")
	}
	m = press(m, "down", "enter")
	if !m.browsing || !strings.Contains(m.View(), "ACCEPTED") || !strings.Contains(m.View(), "esc back") {
		t.Fatalf("enter should open the run's diff read-only:\n%s", m.View())
	}
	m = press(m, "esc")
	if m.browsing {
		t.Fatal("esc should close the review")
	}
	if m = m.switchTab(tabChanges); !strings.Contains(m.View(), "with diff") || strings.Contains(m.View(), "plain run") {
		t.Fatalf("Changes should list only runs with a diff:\n%s", m.View())
	}
}

// drive feeds worker messages into the model until the run finishes.
func drive(t *testing.T, m Model) Model {
	t.Helper()
	go runTurn(m.run.ctx, m.d, m.run.task, m.run.ch)
	deadline := time.After(20 * time.Second)
	for m.cstate == chatRunning {
		select {
		case msg, ok := <-m.run.ch:
			if !ok {
				t.Fatal("worker closed without a final turn")
			}
			nm, _ := m.Update(msg)
			m = nm.(Model)
		case <-deadline:
			t.Fatal("run did not finish")
		}
	}
	return m
}

func fakeAgent(t *testing.T, script string) *agent.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := agent.NewRegistry()
	reg.Add(agent.New("alpha", path, nil, "", []agent.Capability{agent.CapImplement}))
	return reg
}

// End to end through the real worker: a fake agent edits a plain folder, the
// check passes, the diff is reviewed and accepted, and the run lands in
// Changes with its diff.
func TestChatRunReviewAccept(t *testing.T) {
	dir := t.TempDir()
	reg := fakeAgent(t, "echo 'working on it'\necho hello > greeting.txt\n")
	m := testModelIn(dir, reg, []validate.Stage{{Name: "test", Command: "grep -q hello greeting.txt"}}, 140, 40)
	m.ta.SetValue("write a greeting")
	nm, _ := m.submitChat()
	m = drive(t, nm.(Model))

	if m.cstate != chatReviewing {
		t.Fatalf("expected review, got state %d; messages=%+v", m.cstate, m.messages)
	}
	if !strings.Contains(strings.Join(m.run.lines, "\n"), "working on it") {
		t.Fatalf("agent output was not streamed: %q", m.run.lines)
	}
	v := m.View()
	for _, want := range []string{"REVIEW", "greeting.txt", "test ✓", "y accept"} {
		if !strings.Contains(v, want) {
			t.Fatalf("review missing %q\n%s", want, v)
		}
	}
	m = press(m, "y")
	if m.cstate != chatIdle {
		t.Fatal("accept should return to idle")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "greeting.txt")); err != nil || !strings.Contains(string(b), "hello") {
		t.Fatalf("accepted file missing: %v", err)
	}
	if len(m.runs) != 1 || m.runs[0].Outcome != "accepted" || m.runs[0].Agent != "alpha" || m.runs[0].Diff == "" {
		t.Fatalf("accepted run not recorded with its diff: %+v", m.runs)
	}
	if v := m.switchTab(tabChanges).View(); !strings.Contains(v, "write a greeting") || !strings.Contains(v, "2 Changes 1") {
		t.Fatalf("Changes should list the accepted run:\n%s", v)
	}
}

func TestChatRunReject(t *testing.T) {
	dir := t.TempDir()
	reg := fakeAgent(t, "echo x > made.txt\n")
	m := testModelIn(dir, reg, nil, 100, 30)
	m.ta.SetValue("make a file")
	nm, _ := m.submitChat()
	m = press(drive(t, nm.(Model)), "n")
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); !os.IsNotExist(err) {
		t.Fatal("reject should remove the created file")
	}
	if m.runs[0].Outcome != "rejected" {
		t.Fatalf("reject not recorded: %+v", m.runs)
	}
}

// esc while running cancels the agent and reverts what it already wrote.
func TestChatRunCancelReverts(t *testing.T) {
	dir := t.TempDir()
	reg := fakeAgent(t, "echo partial > half.txt\nsleep 20\n")
	m := testModelIn(dir, reg, nil, 100, 30)
	m.ta.SetValue("slow task")
	nm, _ := m.submitChat()
	m = nm.(Model)
	go runTurn(m.run.ctx, m.d, m.run.task, m.run.ch)

	// Wait until the agent has written its file, then cancel.
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, "half.txt")); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("agent never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	m = press(m, "esc")
	start := time.Now()
	for m.cstate == chatRunning {
		nm, _ := m.Update(<-m.run.ch)
		m = nm.(Model)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel did not stop the agent promptly")
	}
	if _, err := os.Stat(filepath.Join(dir, "half.txt")); !os.IsNotExist(err) {
		t.Fatal("cancel should revert the partial change")
	}
	if m.run.result != "cancelled" || !strings.Contains(m.messages[len(m.messages)-1].text, "cancelled") {
		t.Fatalf("cancel not reported: result=%q", m.run.result)
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := renderMarkdown("# Title\n\nSome **bold** text and `code`.\n\n- item one\n- item two", 60)
	if out == "" {
		t.Fatal("markdown render returned empty")
	}
	if strings.Contains(out, "**bold**") {
		t.Fatalf("markdown not rendered (raw ** present):\n%s", out)
	}
}
