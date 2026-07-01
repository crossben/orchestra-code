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
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Agent  string `json:"agent,omitempty"` // optional per-step agent (M4 router will use this)
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

// Make asks the agent to decompose the request and returns the parsed plan.
func (p *Planner) Make(ctx context.Context, request, dir string) (Plan, error) {
	out, err := p.agent.Query(ctx, agent.Task{
		Prompt:  fmt.Sprintf(promptTemplate, request),
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
