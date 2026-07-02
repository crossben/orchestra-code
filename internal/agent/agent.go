// Package agent defines the Agent abstraction: a coding assistant Orchestra can
// dispatch a task to. Every supported agent (Claude, OpenCode, Codex, Gemini) is
// a CLI spawned in headless, auto-approve mode — so there is one concrete
// implementation, CLIAgent, driven by per-agent config. The interface exists so
// a future non-CLI agent (e.g. an API-backed one) can plug in without touching
// the scheduler, router, or shell.
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/runner"
)

// Capability describes what kind of work an agent is suited to. The router (M4)
// uses these to choose an agent; for now they are informational.
type Capability string

const (
	CapPlan      Capability = "plan"
	CapImplement Capability = "implement"
	CapReview    Capability = "review"
)

// Task is a unit of work handed to an agent.
type Task struct {
	Prompt  string
	Dir     string
	Timeout time.Duration
}

// Result reports how an agent run finished.
type Result struct {
	ExitCode int
	Duration time.Duration
	Output   string // combined stdout+stderr the agent produced (for question detection)
}

// Agent is anything Orchestra can dispatch a Task to.
type Agent interface {
	Name() string
	Run(ctx context.Context, task Task) (Result, error)
	Health() error // nil if the agent is installed and usable
	Capabilities() []Capability
}

// Querier is an agent that can answer a prompt with text (its stdout) instead of
// editing files — used by the planner for decomposition. CLIAgent implements it;
// callers type-assert and fall back gracefully if an agent doesn't.
type Querier interface {
	Query(ctx context.Context, task Task) (string, error)
}

// ProbeResult reports whether an agent can actually do work (not just that its
// binary exists): OK means it responded; otherwise Detail explains the failure
// (timeout, auth/billing error, etc.).
type ProbeResult struct {
	OK     bool
	Detail string
}

// Prober is an agent that can be health-probed with a trivial live task.
type Prober interface {
	Probe(ctx context.Context, timeout time.Duration) ProbeResult
}

// Has reports whether an agent has a given capability.
func Has(a Agent, c Capability) bool {
	for _, cap := range a.Capabilities() {
		if cap == c {
			return true
		}
	}
	return false
}

// CLIAgent runs a coding-agent CLI in headless mode. Args is the prefix that
// selects headless/auto-approve mode; the task prompt is appended as the final
// argument. Auto-approve is deliberate: Orchestra's diff review is the human
// gate, so the agent itself must not block on its own permission prompts.
type CLIAgent struct {
	name    string
	bin     string
	args    []string
	dirFlag string // if set, inject "<dirFlag> <abs dir>" so CLIs that ignore cwd still isolate
	caps    []Capability
}

// New builds a CLIAgent. dirFlag (may be empty) is the flag an agent needs to be
// told its working directory — some CLIs (e.g. opencode) ignore the process cwd.
func New(name, bin string, args []string, dirFlag string, caps []Capability) *CLIAgent {
	return &CLIAgent{name: name, bin: bin, args: args, dirFlag: dirFlag, caps: caps}
}

func (a *CLIAgent) Name() string               { return a.name }
func (a *CLIAgent) Capabilities() []Capability { return a.caps }

// Health reports whether the agent's binary is on PATH.
func (a *CLIAgent) Health() error {
	_, err := exec.LookPath(a.bin)
	return err
}

// Run dispatches the task to the CLI via the runner.
func (a *CLIAgent) Run(ctx context.Context, task Task) (Result, error) {
	out, r, err := runner.Run(ctx, a.spec(task))
	return Result{ExitCode: r.ExitCode, Duration: r.Duration, Output: out}, err
}

// Query runs the CLI and captures its stdout (for planning / decomposition).
func (a *CLIAgent) Query(ctx context.Context, task Task) (string, error) {
	out, _, err := runner.RunCapture(ctx, a.spec(task))
	return out, err
}

const probePrompt = "Reply with exactly the word OK and nothing else. Do not create or modify any files."

// Probe runs a trivial live task in a throwaway directory to check the agent can
// actually work — catching auth/billing errors and hangs that a binary-on-PATH
// check misses.
func (a *CLIAgent) Probe(ctx context.Context, timeout time.Duration) ProbeResult {
	dir, err := os.MkdirTemp("", "orchestra-probe-")
	if err != nil {
		return ProbeResult{OK: false, Detail: "cannot create temp dir: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	out, res, err := runner.RunProbe(ctx, a.spec(Task{Prompt: probePrompt, Dir: dir, Timeout: timeout}))
	switch {
	case err != nil:
		return ProbeResult{OK: false, Detail: firstMeaningfulLine(err.Error())}
	case res.ExitCode != 0:
		detail := firstMeaningfulLine(out)
		if detail == "" {
			detail = fmt.Sprintf("exited with code %d", res.ExitCode)
		}
		return ProbeResult{OK: false, Detail: detail}
	default:
		return ProbeResult{OK: true, Detail: "responded"}
	}
}

// firstMeaningfulLine returns a short, human-useful snippet from agent output:
// the last non-empty line (errors usually land last), trimmed of ANSI noise.
func firstMeaningfulLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(stripANSI(lines[i]))
		if t != "" {
			if len(t) > 120 {
				t = t[:117] + "…"
			}
			return t
		}
	}
	return ""
}

// stripANSI removes basic ANSI escape sequences so probe detail is readable.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (a *CLIAgent) spec(task Task) runner.Spec {
	dir := task.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	args := slices.Clone(a.args)
	if a.dirFlag != "" {
		args = append(args, a.dirFlag, dir) // tell cwd-ignoring CLIs where to work
	}
	args = append(args, task.Prompt)
	return runner.Spec{
		Bin:     a.bin,
		Args:    args,
		Dir:     dir,
		Timeout: task.Timeout,
	}
}
