// Package agent defines the Agent abstraction: a coding assistant Orchestra can
// dispatch a task to. Every supported agent (Claude, OpenCode, Codex, Gemini) is
// a CLI spawned in headless, auto-approve mode — so there is one concrete
// implementation, CLIAgent, driven by per-agent config. The interface exists so
// a future non-CLI agent (e.g. an API-backed one) can plug in without touching
// the scheduler, router, or shell.
package agent

import (
	"context"
	"os/exec"
	"slices"
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
}

// Agent is anything Orchestra can dispatch a Task to.
type Agent interface {
	Name() string
	Run(ctx context.Context, task Task) (Result, error)
	Health() error // nil if the agent is installed and usable
	Capabilities() []Capability
}

// CLIAgent runs a coding-agent CLI in headless mode. Args is the prefix that
// selects headless/auto-approve mode; the task prompt is appended as the final
// argument. Auto-approve is deliberate: Orchestra's diff review is the human
// gate, so the agent itself must not block on its own permission prompts.
type CLIAgent struct {
	name string
	bin  string
	args []string
	caps []Capability
}

// New builds a CLIAgent.
func New(name, bin string, args []string, caps []Capability) *CLIAgent {
	return &CLIAgent{name: name, bin: bin, args: args, caps: caps}
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
	args := append(slices.Clone(a.args), task.Prompt)
	r, err := runner.Run(ctx, runner.Spec{
		Bin:     a.bin,
		Args:    args,
		Dir:     task.Dir,
		Timeout: task.Timeout,
	})
	return Result{ExitCode: r.ExitCode, Duration: r.Duration}, err
}
