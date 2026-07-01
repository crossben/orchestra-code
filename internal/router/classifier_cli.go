package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/agent"
)

// CLIClassifier classifies messages by asking an agent (in query mode) to return
// a small JSON verdict. It is the default M4 classifier — no API key, works with
// whatever agent CLIs are installed.
type CLIClassifier struct {
	agent      agent.Querier
	name       string
	choices    []string // available agent names offered to the classifier
	timeout    time.Duration
}

// NewCLIClassifier wraps a query-capable agent. choices is the list of agent
// names the classifier may suggest.
func NewCLIClassifier(a agent.Agent, choices []string, timeout time.Duration) (*CLIClassifier, error) {
	q, ok := a.(agent.Querier)
	if !ok {
		return nil, fmt.Errorf("agent %q cannot classify (no query support)", a.Name())
	}
	return &CLIClassifier{agent: q, name: a.Name(), choices: choices, timeout: timeout}, nil
}

const classifyTemplate = `You are the router for a coding assistant. Classify the user's message and respond with ONLY a JSON object — no prose, no markdown fences:
{"intent": "question|plan|implement|review", "agent": "<one of: %s — or empty>", "reason": "<short justification>"}

Definitions:
- "question": the user wants information or an explanation, NOT a change to the code.
- "plan": the user wants a large task broken into steps.
- "implement": the user wants code written or modified.
- "review": the user wants existing code reviewed or critiqued.
Set "agent" only if one is clearly most suitable; otherwise leave it empty.

User message: %s`

// Classify queries the agent and parses its JSON verdict.
func (c *CLIClassifier) Classify(ctx context.Context, message, dir string) (Classification, error) {
	prompt := fmt.Sprintf(classifyTemplate, strings.Join(c.choices, ", "), message)
	out, err := c.agent.Query(ctx, agent.Task{Prompt: prompt, Dir: dir, Timeout: c.timeout})
	if err != nil {
		return Classification{}, err
	}
	return parseClassification(out)
}

// parseClassification leniently extracts a JSON object from free-form output.
func parseClassification(out string) (Classification, error) {
	start := strings.IndexByte(out, '{')
	end := strings.LastIndexByte(out, '}')
	if start < 0 || end < 0 || end <= start {
		return Classification{}, fmt.Errorf("no JSON object in classifier output: %s", truncate(out, 300))
	}
	var c Classification
	if err := json.Unmarshal([]byte(out[start:end+1]), &c); err != nil {
		return Classification{}, fmt.Errorf("could not parse classifier JSON: %w", err)
	}
	c.Intent = Intent(strings.ToLower(strings.TrimSpace(string(c.Intent))))
	c.Agent = strings.TrimSpace(c.Agent)
	return c, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
