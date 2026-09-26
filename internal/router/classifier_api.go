package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/llm"
)

// DefaultClassifierTimeout bounds one API classification call. It is separate
// from the task timeout: classifying should take seconds, not minutes.
const DefaultClassifierTimeout = 30 * time.Second

// apiClassifierMaxTokens caps the reply — the verdict is a tiny JSON object.
const apiClassifierMaxTokens = 256

// APIClassifier classifies messages by calling a hosted LLM directly over HTTP
// (no agent CLI is spawned). A small, cheap model is enough; the answerer for
// plain questions stays the router agent.
type APIClassifier struct {
	prov    llm.Provider
	choices []string
	timeout time.Duration
}

// NewAPIClassifier wraps a provider. choices is the list of agent names the
// classifier may suggest; timeout <= 0 uses DefaultClassifierTimeout.
func NewAPIClassifier(p llm.Provider, choices []string, timeout time.Duration) *APIClassifier {
	if timeout <= 0 {
		timeout = DefaultClassifierTimeout
	}
	return &APIClassifier{prov: p, choices: choices, timeout: timeout}
}

// Classify sends the output contract as the system prompt and the message as
// the user turn, then leniently parses the JSON verdict.
func (c *APIClassifier) Classify(ctx context.Context, message, _ string) (Classification, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.prov.Complete(ctx, llm.Request{
		System:      fmt.Sprintf(classifyContract, strings.Join(c.choices, ", ")),
		Messages:    []llm.Message{{Role: "user", Content: message}},
		MaxTokens:   apiClassifierMaxTokens,
		Temperature: 0,
	})
	if err != nil {
		return Classification{}, fmt.Errorf("%s classifier: %w", c.prov.Name(), err)
	}
	return parseClassification(resp.Text)
}
