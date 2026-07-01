// Package planner decomposes a high-level request ("build authentication") into
// an ordered list of concrete sub-tasks. It only plans — it does not write code.
// The workflow runner then executes each step through the supervised engine.
//
// Planning uses a plan-capable agent in "query" mode (its stdout is captured and
// parsed) rather than letting it edit files. Because agent output is free-form,
// the JSON is extracted leniently (first '[' to last ']').
package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/agent"
)

// Step is one planned sub-task.
type Step struct {
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Agent     string `json:"agent,omitempty"`      // optional per-step agent (M4 router)
	DependsOn []int  `json:"depends_on,omitempty"` // 1-based indices of prerequisite steps (M5 parallel)
}

// Plan is an ordered list of steps.
type Plan struct {
	Request string
	Steps   []Step
}

// Planner turns requests into plans using a querying agent.
type Planner struct {
	agent   agent.Querier
	name    string
	timeout time.Duration
}

// New builds a Planner from an agent. The agent must support text queries
// (CLIAgent does) — otherwise planning isn't possible with it.
func New(a agent.Agent, timeout time.Duration) (*Planner, error) {
	q, ok := a.(agent.Querier)
	if !ok {
		return nil, fmt.Errorf("agent %q cannot be used for planning (no query support)", a.Name())
	}
	return &Planner{agent: q, name: a.Name(), timeout: timeout}, nil
}

// AgentName returns the name of the planning agent.
func (p *Planner) AgentName() string { return p.name }

const promptTemplate = `You are a planning assistant for a software project. Break the task below into a short, ordered list of concrete sub-tasks that another coding agent will implement one at a time.

Rules:
- Do NOT write code or modify any files. Output a plan only.
- 3 to 7 steps. Each step should be independently implementable and testable.
- Output ONLY a JSON array, no prose, no markdown fences. Each element:
  {"title": "<short imperative>", "detail": "<one or two sentences of guidance>"}

Task: %s`

const parallelTemplate = `You are a planning assistant for a software project. Break the task below into a short, ordered list of concrete sub-tasks, and identify which steps can run in PARALLEL vs. which depend on earlier ones.

Rules:
- Do NOT write code or modify any files. Output a plan only.
- 2 to 7 steps. Each step should be independently implementable and testable.
- For each step add "depends_on": a list of the 1-based step numbers that must complete BEFORE it (empty if it can start immediately / run in parallel with others).
- For each step, assign "agent": the best-suited agent for that step from this list: [%s]. Respect any agent the task explicitly requests for a part of the work. Use "" if no strong preference.
- Output ONLY a JSON array, no prose, no markdown fences. Each element:
  {"title": "<short imperative>", "detail": "<one or two sentences>", "agent": "<agent name or empty>", "depends_on": [<step numbers>]}

Task: %s`

// Make asks the agent to decompose the request and returns the parsed plan.
func (p *Planner) Make(ctx context.Context, request, dir string) (Plan, error) {
	return p.make(ctx, promptTemplate, request, dir)
}

// MakeParallel decomposes the request and asks the agent to declare per-step
// dependencies (and an optional per-step agent from agentChoices), so the
// workflow runner can parallelize independent steps across the best agents.
func (p *Planner) MakeParallel(ctx context.Context, request, dir string, agentChoices []string) (Plan, error) {
	out, err := p.agent.Query(ctx, agent.Task{
		Prompt:  fmt.Sprintf(parallelTemplate, strings.Join(agentChoices, ", "), request),
		Dir:     dir,
		Timeout: p.timeout,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("planning agent failed: %w", err)
	}
	steps, err := parseSteps(out)
	if err != nil {
		return Plan{}, err
	}
	pl := Plan{Request: request, Steps: steps}
	sanitizeDeps(pl.Steps)
	return pl, nil
}

func (p *Planner) make(ctx context.Context, tmpl, request, dir string) (Plan, error) {
	out, err := p.agent.Query(ctx, agent.Task{
		Prompt:  fmt.Sprintf(tmpl, request),
		Dir:     dir,
		Timeout: p.timeout,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("planning agent failed: %w", err)
	}
	steps, err := parseSteps(out)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Request: request, Steps: steps}, nil
}

// sanitizeDeps drops out-of-range or self/forward dependencies so a malformed
// plan can't create a cycle or reference a nonexistent step.
func sanitizeDeps(steps []Step) {
	for i := range steps {
		var clean []int
		for _, d := range steps[i].DependsOn {
			if d >= 1 && d <= len(steps) && d-1 < i { // must reference an earlier step
				clean = append(clean, d)
			}
		}
		steps[i].DependsOn = clean
	}
}

// parseSteps leniently extracts a JSON array of steps from free-form agent text.
func parseSteps(out string) ([]Step, error) {
	start := strings.IndexByte(out, '[')
	end := strings.LastIndexByte(out, ']')
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("planner: no JSON array found in agent output:\n%s", truncate(out, 500))
	}
	var steps []Step
	if err := json.Unmarshal([]byte(out[start:end+1]), &steps); err != nil {
		return nil, fmt.Errorf("planner: could not parse plan JSON: %w\n%s", err, truncate(out, 500))
	}
	// Keep only steps with a title.
	cleaned := steps[:0]
	for _, s := range steps {
		s.Title = strings.TrimSpace(s.Title)
		if s.Title != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return nil, errors.New("planner: plan contained no usable steps")
	}
	return cleaned, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
