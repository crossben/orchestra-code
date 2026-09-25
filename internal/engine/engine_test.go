package engine

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/validate"
)

// writeAgent is an in-process fake agent that "edits" the task directory by
// creating one file — enough to exercise the supervised loop's diff/review/
// restore paths without any subprocess or network.
type writeAgent struct {
	name    string
	path    string
	content string
}

func (w *writeAgent) Name() string                     { return w.name }
func (w *writeAgent) Health() error                    { return nil }
func (w *writeAgent) Capabilities() []agent.Capability { return []agent.Capability{agent.CapImplement} }
func (w *writeAgent) Run(_ context.Context, task agent.Task) (agent.Result, error) {
	target := filepath.Join(task.Dir, w.path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return agent.Result{}, err
	}
	if err := os.WriteFile(target, []byte(w.content), 0o644); err != nil {
		return agent.Result{}, err
	}
	return agent.Result{ExitCode: 0, Output: "wrote " + w.path}, nil
}

func optsFor(a agent.Agent, dir string) Options {
	return Options{
		Agent:      a,
		Prompt:     "make the file",
		Dir:        dir,
		MaxRetries: 0,
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should have been removed", path)
	}
}

func mustContain(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), want) {
		t.Errorf("%s = %q, missing %q", path, b, want)
	}
}

func TestExecuteNonGitRejectRestores(t *testing.T) {
	dir := t.TempDir() // deliberately NOT a git repository
	mustWrite(t, filepath.Join(dir, "pre.txt"), "before\n")

	in := bufio.NewReader(strings.NewReader("n\n")) // reject at the review prompt
	out, err := Execute(context.Background(), in, optsFor(&writeAgent{name: "fake", path: "created.txt", content: "hi"}, dir))
	if err != nil {
		t.Fatal(err)
	}
	if !out.HadChanges || out.Accepted {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	mustNotExist(t, filepath.Join(dir, "created.txt"))
	mustContain(t, filepath.Join(dir, "pre.txt"), "before")
}

func TestExecuteNonGitAcceptKeeps(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pre.txt"), "before\n")

	in := bufio.NewReader(strings.NewReader("y\n"))
	opts := optsFor(&writeAgent{name: "fake", path: "sub/created.txt", content: "kept"}, dir)
	opts.CommitOnAccept = true // shell-like: would commit in a repo

	out, err := Execute(context.Background(), in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatalf("expected acceptance: %+v", out)
	}
	mustContain(t, filepath.Join(dir, "sub", "created.txt"), "kept")
	mustContain(t, filepath.Join(dir, "pre.txt"), "before")
}

func TestExecuteNonGitNoChange(t *testing.T) {
	dir := t.TempDir()
	in := bufio.NewReader(strings.NewReader("y\n"))
	out, err := Execute(context.Background(), in, optsFor(noopAgent{}, dir))
	if err != nil {
		t.Fatal(err)
	}
	if out.HadChanges || out.Accepted {
		t.Fatalf("expected no changes: %+v", out)
	}
}

type noopAgent struct{}

func (noopAgent) Name() string                     { return "noop" }
func (noopAgent) Health() error                    { return nil }
func (noopAgent) Capabilities() []agent.Capability { return nil }
func (noopAgent) Run(context.Context, agent.Task) (agent.Result, error) {
	return agent.Result{ExitCode: 0, Output: ""}, nil
}

func TestExecuteGitRepoAcceptStillCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	mustWrite(t, filepath.Join(dir, "base.txt"), "base\n")
	run("add", "-A")
	run("commit", "-qm", "init")

	in := bufio.NewReader(strings.NewReader("y\n"))
	opts := optsFor(&writeAgent{name: "fake", path: "committed.txt", content: "in history"}, dir)
	opts.CommitOnAccept = true
	out, err := Execute(context.Background(), in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatalf("expected acceptance: %+v", out)
	}
	var count string
	cmd := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD")
	if c, err := cmd.Output(); err == nil {
		count = strings.TrimSpace(string(c))
	}
	if count != "2" {
		t.Fatalf("expected 2 commits after acceptance, got %q", count)
	}
	status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(status)) != "" {
		t.Errorf("tree should be clean after commit: %q", status)
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing streamed output.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Produce must report each attempt, the agent's exit and every validation
// stage, and stream the agent's output — the dashboard's live run panel.
func TestProduceEmitsEventsAndStreamsOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "flaky.sh")
	// First attempt writes "broken", later ones "fixed"; the counter lives
	// next to the script so it never shows up in the diff.
	mustWrite(t, script, `#!/bin/sh
c="$(dirname "$0")/count"; n=$(cat "$c" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$c"
if [ $n -ge 2 ]; then echo fixed > out.txt; else echo broken > out.txt; fi
echo "attempt $n"
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	var events []Event
	var out syncBuffer
	turn := Produce(context.Background(), Options{
		Agent:      agent.New("flaky", script, nil, "", []agent.Capability{agent.CapImplement}),
		Prompt:     "fix it",
		Dir:        dir,
		Stages:     []validate.Stage{{Name: "test", Command: "grep -q fixed out.txt"}},
		MaxRetries: 2,
		OnEvent:    func(e Event) { events = append(events, e) },
		Output:     &out,
	})
	if turn.Err != nil {
		t.Fatal(turn.Err)
	}
	if turn.Attempts != 2 || !turn.Report.Passed() || !turn.HadChanges {
		t.Fatalf("unexpected turn: attempts=%d passed=%v changed=%v", turn.Attempts, turn.Report.Passed(), turn.HadChanges)
	}

	var kinds []string
	names := map[EventKind]string{EventAttempt: "attempt", EventAgentDone: "agent", EventStageStart: "start", EventStageDone: "done"}
	for _, e := range events {
		k := names[e.Kind]
		if e.Kind == EventStageDone && !e.Passed {
			k += "✗"
		}
		kinds = append(kinds, k)
	}
	want := "attempt agent start done✗ attempt agent start done"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if events[4].Attempt != 2 || events[4].Max != 3 {
		t.Fatalf("second attempt event = %+v", events[4])
	}
	if s := out.String(); !strings.Contains(s, "attempt 1") || !strings.Contains(s, "attempt 2") {
		t.Fatalf("output not streamed: %q", s)
	}
}
