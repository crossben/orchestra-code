package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/validate"
)

// fakeParAgents registers alpha (default, also the planner) and beta. Both
// run the same script: a planning prompt prints planJSON, any other prompt
// runs work (a shell snippet that sees the prompt in "$*").
func fakeParAgents(t *testing.T, planJSON, work string) *agent.Registry {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n*'planning assistant'*)\ncat <<'PLAN'\n" + planJSON + "\nPLAN\n;;\n*)\n" + work + "\n;;\nesac\n"
	reg := agent.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(bin, name+".sh")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		reg.Add(agent.New(name, path, nil, "", []agent.Capability{agent.CapImplement, agent.CapPlan}))
	}
	return reg
}

// Two independent steps, then a third that needs both.
const threeStepPlan = `[{"title":"write a","agent":"alpha","depends_on":[]},
{"title":"write b","agent":"beta","depends_on":[]},
{"title":"write c","agent":"alpha","depends_on":[1,2]}]`

const writeFiles = `echo "working on $*" | tail -c 60
case "$*" in
*'write a'*) echo a > a.txt ;;
*'write b'*) mkdir -p pkg && echo b > pkg/b.txt ;;
*'write c'*) cat a.txt pkg/b.txt > c.txt ;;
esac`

func startPar(t *testing.T, m Model, request string) Model {
	t.Helper()
	m.ta.SetValue("/parallel " + request)
	nm, _ := m.submitChat()
	m = nm.(Model)
	if m.par == nil {
		t.Fatalf("/parallel should start a parallel run; messages=%+v", m.messages)
	}
	t.Cleanup(func() {
		if m.par != nil && m.par.iso != nil {
			m.par.iso.Cleanup()
		}
	})
	return m
}

// drivePar feeds worker messages into the model until the run stops running
// (review, finished, or quit). It returns the last command.
func drivePar(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	var cmd tea.Cmd
	for m.par != nil && m.cstate == chatRunning {
		select {
		case msg, ok := <-m.par.ch:
			if !ok {
				t.Fatal("parallel worker closed its channel without a final message")
			}
			var nm tea.Model
			nm, cmd = m.Update(msg)
			m = nm.(Model)
		case <-deadline:
			t.Fatal("parallel run did not settle")
		}
	}
	return m, cmd
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func plainDir(t *testing.T) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Plan → wave 1 (two tasks) → review both → wave 2 (depends on both) →
// review → done, in a plain folder and in a git repository.
func TestParallelWavesReviewMerge(t *testing.T) {
	for _, mode := range []string{"plain", "git"} {
		t.Run(mode, func(t *testing.T) {
			dir := plainDir(t)
			if mode == "git" {
				dir = gitRepo(t)
			}
			reg := fakeParAgents(t, threeStepPlan, writeFiles)
			m := testModelIn(dir, reg, []validate.Stage{{Name: "test", Command: "true"}}, 140, 40)
			m = startPar(t, m, "build a, b and c")

			m, _ = drivePar(t, m)
			if m.cstate != chatReviewing {
				t.Fatalf("wave 1 should end in review, state=%d messages=%+v", m.cstate, m.messages)
			}
			if len(m.par.tasks) != 2 || m.par.wave != 1 || m.par.waves != 2 {
				t.Fatalf("wave 1: tasks=%d wave=%d/%d", len(m.par.tasks), m.par.wave, m.par.waves)
			}
			v := m.View()
			for _, want := range []string{"REVIEW", "wave 1/2 · task 1/2", "a.txt", "y accept"} {
				if !strings.Contains(v, want) {
					t.Fatalf("first review missing %q\n%s", want, v)
				}
			}
			m = press(m, "y")
			if !strings.Contains(m.View(), "wave 1/2 · task 2/2") || !strings.Contains(m.View(), "pkg/b.txt") {
				t.Fatalf("second review should be task 2/2 with pkg/b.txt:\n%s", m.View())
			}
			m = press(m, "y")
			if m.cstate != chatRunning || m.par.wave != 2 {
				t.Fatalf("accepting the last task should start wave 2, state=%d wave=%d", m.cstate, m.par.wave)
			}
			m, _ = drivePar(t, m)
			if m.cstate != chatReviewing || !strings.Contains(m.View(), "wave 2/2 · task 1/1") {
				t.Fatalf("wave 2 should be reviewed:\n%s", m.View())
			}
			m = press(m, "y")
			if m.cstate != chatIdle || m.par.phase != parFinished {
				t.Fatalf("run should be finished, state=%d phase=%d", m.cstate, m.par.phase)
			}
			if got := readFile(t, filepath.Join(dir, "c.txt")); got != "a\nb\n" {
				t.Fatalf("c.txt = %q (wave 2 must see wave 1's merged work)", got)
			}
			if readFile(t, filepath.Join(dir, "base.txt")) != "base\n" {
				t.Fatal("base.txt changed")
			}
			last := m.messages[len(m.messages)-1].text
			if !strings.Contains(last, "all 3 steps merged") {
				t.Fatalf("summary = %q", last)
			}
			if len(m.runs) != 3 {
				t.Fatalf("each task should be recorded, got %d runs", len(m.runs))
			}
			for _, r := range m.runs {
				if r.Outcome != "accepted" || r.Diff == "" {
					t.Fatalf("run not recorded as accepted with a diff: %+v", r)
				}
			}
			if mode == "git" {
				if clean, _ := gitutil.IsClean(dir); !clean {
					t.Fatal("repository should be clean after merges")
				}
				out, _ := exec.Command("git", "-C", dir, "branch", "--list", "orchestra/*").Output()
				if strings.TrimSpace(string(out)) != "" {
					t.Fatalf("task branches left behind: %s", out)
				}
			}
			if v := m.View(); !strings.Contains(v, "merged") {
				t.Fatalf("finished panel should show merged tasks:\n%s", v)
			}
		})
	}
}

// Two tasks edit the same file: the first merges, the second is reported as
// a conflict, counts as rejected, and lands nothing. A dependent step is
// skipped.
func TestParallelConflict(t *testing.T) {
	dir := plainDir(t)
	plan := `[{"title":"edit one","agent":"alpha"},{"title":"edit two","agent":"beta"},
{"title":"after two","agent":"alpha","depends_on":[2]}]`
	work := `case "$*" in
*'edit one'*) echo one > base.txt ;;
*'edit two'*) echo two > base.txt; echo t > two-only.txt ;;
*) echo after > after.txt ;;
esac`
	m := testModelIn(dir, fakeParAgents(t, plan, work), nil, 120, 30)
	m = startPar(t, m, "edit")
	m, _ = drivePar(t, m)
	m = press(m, "y", "y")
	if m.cstate != chatIdle {
		t.Fatalf("the dependent step should be skipped and the run finish, state=%d", m.cstate)
	}
	if got := readFile(t, filepath.Join(dir, "base.txt")); got != "one\n" {
		t.Fatalf("base.txt = %q", got)
	}
	for _, f := range []string{"two-only.txt", "after.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist", f)
		}
	}
	all := ""
	for _, l := range m.messages {
		all += l.text + "\n"
	}
	for _, want := range []string{"conflict", "skipped", "1/3 steps merged"} {
		if !strings.Contains(all, want) {
			t.Fatalf("transcript missing %q:\n%s", want, all)
		}
	}
	outcomes := map[string]int{}
	for _, r := range m.runs {
		outcomes[r.Outcome]++
	}
	if outcomes["accepted"] != 1 || outcomes["rejected"] != 1 {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

// A failed task is reported with its error and skipped; the others are still
// reviewed.
func TestParallelFailedTaskSkipped(t *testing.T) {
	dir := plainDir(t)
	plan := `[{"title":"good","agent":"alpha"},{"title":"bad","agent":"beta"}]`
	work := `case "$*" in *'good'*) echo g > g.txt ;; *) echo boom; exit 3 ;; esac`
	m := testModelIn(dir, fakeParAgents(t, plan, work), nil, 120, 30)
	m = startPar(t, m, "x")
	m, _ = drivePar(t, m)
	if m.cstate != chatReviewing || !strings.Contains(m.View(), "g.txt") {
		t.Fatalf("the good task should be under review:\n%s", m.View())
	}
	found := false
	for _, l := range m.messages {
		if strings.Contains(l.text, "step 2") && strings.Contains(l.text, "failed") && strings.Contains(l.text, "code 3") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed task not reported: %+v", m.messages)
	}
	m = press(m, "n")
	if m.cstate != chatIdle {
		t.Fatal("run should finish")
	}
	if _, err := os.Stat(filepath.Join(dir, "g.txt")); !os.IsNotExist(err) {
		t.Fatal("rejected task must leave nothing")
	}
}

// ctrl+c mid-wave cancels every task, removes every isolated tree, undoes
// stray writes into the base folder, and only then quits.
func TestParallelCtrlCCleansUp(t *testing.T) {
	dir := plainDir(t)
	plan := `[{"title":"slow one","agent":"alpha"},{"title":"slow two","agent":"beta"}]`
	work := `echo partial > half.txt; echo stray > '` + filepath.Join(dir, "stray.txt") + `'; sleep 20`
	reg := fakeParAgents(t, plan, work)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	m := testModelIn(dir, reg, nil, 120, 30)
	m = startPar(t, m, "slow")
	// Run until both agents are working.
	deadline := time.After(20 * time.Second)
	for {
		running := 0
		for _, tk := range m.par.tasks {
			if tk.status == tsRunning {
				running++
			}
		}
		if running == 2 {
			if _, err := os.Stat(filepath.Join(dir, "stray.txt")); err == nil {
				break
			}
		}
		select {
		case msg := <-m.par.ch:
			nm, _ := m.Update(msg)
			m = nm.(Model)
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("agents never started")
		}
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = nm.(Model)
	if cmd != nil || !m.quitting {
		t.Fatal("first ctrl+c should cancel, not quit yet")
	}
	start := time.Now()
	m, cmd = drivePar(t, m)
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel did not stop the agents promptly")
	}
	if cmd == nil {
		t.Fatal("should quit once cleaned up")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected a quit command")
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "orchestra-") {
			t.Fatalf("temp tree left behind: %s", e.Name())
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "base.txt" || readFile(t, filepath.Join(dir, "base.txt")) != "base\n" {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("base folder should be untouched, has %v", names)
	}
	for _, r := range m.runs {
		if r.Outcome != "cancelled" {
			t.Fatalf("cancelled tasks should be recorded as cancelled: %+v", r)
		}
	}
}

// parModel is a model showing a running wave of three tasks (no worker).
func parModel(w, h int) Model {
	m := testModelIn(".", nil, nil, w, h)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	m.par = &parRun{
		ctx: ctx, cancel: cancel, ch: make(chan tea.Msg, 8), request: "r", phase: parRunning,
		start: now, wave: 1, waves: 2,
		tasks: []*parTask{
			{idx: 0, id: "1", title: "add the login handler with validation", agent: "alpha", status: tsRunning, attempt: 1, max: 3, start: now, lines: []string{"compiling handler"}},
			{idx: 1, id: "2", title: "write docs", agent: "beta", status: tsQueued},
			{idx: 2, id: "3", title: "wire the router", agent: "alpha", status: tsRetrying, attempt: 2, max: 3, start: now, lines: []string{"first line", "LASTLINE of output"}},
		},
	}
	m.cstate = chatRunning
	m.layout()
	return m
}

func TestParallelPanelFitsAtWidths(t *testing.T) {
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {140, 40}, {200, 50}} {
		m := parModel(size[0], size[1])
		v := m.View()
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > size[0] {
				t.Fatalf("%dx%d line %d is %d wide:\n%s", size[0], size[1], i, w, l)
			}
		}
		plain := ansi.Strip(v)
		wants := []string{"wave 1/2", "running", "queued", "retrying", "LASTLINE"}
		if size[1] < 20 {
			wants = []string{"wave 1/2", "running"} // only the selected rows fit
		}
		for _, want := range wants {
			if !strings.Contains(plain, want) {
				t.Fatalf("%dx%d: panel missing %q\n%s", size[0], size[1], want, plain)
			}
		}
		if size[0] >= 200 && !strings.Contains(plain, "2/3") {
			t.Fatalf("%dx%d: wide rows should show the attempt\n%s", size[0], size[1], plain)
		}
	}
}

func TestParallelEventRoutingByTaskID(t *testing.T) {
	m := parModel(140, 40)
	for _, msg := range []tea.Msg{
		parEventMsg{id: "2", e: engine.Event{Kind: engine.EventAttempt, Attempt: 1, Max: 3}},
		parOutputMsg{id: "2", lines: []string{"docs output"}},
		parEventMsg{id: "2", e: engine.Event{Kind: engine.EventAgentDone, Attempt: 1, Max: 3}},
		parEventMsg{id: "2", e: engine.Event{Kind: engine.EventStageStart, Stage: "test"}},
	} {
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	t1, t2 := m.par.tasks[0], m.par.tasks[1]
	if t2.status != tsValidating || t2.attempt != 1 || t2.start.IsZero() || t2.last() != "docs output" {
		t.Fatalf("task 2 not updated: %+v", t2)
	}
	if t1.status != tsRunning || t1.last() != "compiling handler" {
		t.Fatalf("task 1 should be untouched: %+v", t1)
	}
	nm, _ := m.Update(parEventMsg{id: "1", e: engine.Event{Kind: engine.EventAttempt, Attempt: 2, Max: 3}})
	if s := nm.(Model).par.tasks[0].status; s != tsRetrying {
		t.Fatalf("attempt 2 should mark retrying, got %v", s)
	}
}

func TestParallelSelectExpandCollapse(t *testing.T) {
	m := parModel(140, 40)
	m = press(m, "down", "j")
	if m.par.sel != 2 {
		t.Fatalf("sel = %d, want 2", m.par.sel)
	}
	m = press(m, "k")
	if m.par.sel != 1 {
		t.Fatalf("k should move up, sel = %d", m.par.sel)
	}
	m = press(m, "k", "enter")
	if !m.par.expanded {
		t.Fatal("enter should expand the selected task")
	}
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "compiling handler") || !strings.Contains(v, "esc back") {
		t.Fatalf("expanded view should show the task's output:\n%s", v)
	}
	m = press(m, "esc")
	if m.par.expanded || m.par.cancelled {
		t.Fatal("esc should collapse without cancelling")
	}
	m = press(m, "tab")
	if !m.par.expanded || m.active != tabChat {
		t.Fatal("tab should expand while a wave runs")
	}
	m = press(m, "esc", "esc")
	if !m.par.cancelled || m.par.ctx.Err() == nil {
		t.Fatal("esc on the task list should cancel the run")
	}
}

func TestParallelUsage(t *testing.T) {
	m := testModel()
	m.ta.SetValue("/par   ")
	nm, _ := m.submitChat()
	m = nm.(Model)
	if m.par != nil || m.cstate != chatIdle || !strings.Contains(m.messages[len(m.messages)-1].text, "/parallel") {
		t.Fatalf("empty /par should print usage, got %+v", m.messages)
	}
}

func TestParallelRecordsTaskUsage(t *testing.T) {
	m := testModel()
	task := &parTask{agent: "alpha", prompt: "p", turn: engine.Turn{Attempts: 1,
		Usage: agent.Usage{InputTokens: 3000, OutputTokens: 300, CostUSD: 0.012, Known: true, Priced: true}}}
	m.recordTask(task, "accepted", "diff")
	if len(m.runs) != 1 || m.runs[0].TokensIn != 3000 || m.runs[0].TokensOut != 300 || m.runs[0].CostUSD != 0.012 {
		t.Fatalf("parallel task not recorded with usage: %+v", m.runs)
	}
	if st := m.stats["alpha"]; st.TokensIn != 3000 {
		t.Fatalf("agent totals not updated: %+v", st)
	}
}
