// Package llm abstracts HTTP LLM APIs behind one small Provider interface so
// Orchestra can dispatch tasks to a hosted model without shelling out to a
// CLI. It is deliberately dependency-free (stdlib net/http only) and
// provider-agnostic: each concrete provider is one file, and adding another is
// a matter of implementing Provider and registering it in New.
//
// Errors are typed (*Error with an ErrorKind) so callers — notably the health
// probe — can turn auth/billing/rate-limit failures into actionable messages
// instead of raw HTTP noise.
package llm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Message is one chat turn.
type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// Request is a single completion request.
type Request struct {
	System      string    // system preamble (output contract, principles)
	Messages    []Message // conversation; typically one user message
	MaxTokens   int       // hard cap on the reply; provider default when 0
	Temperature float64   // 0 = deterministic
}

// Response is a completed completion.
type Response struct {
	Text       string // assistant text
	StopReason string // provider-reported stop reason (informational)
}

// Provider abstracts one HTTP LLM API.
type Provider interface {
	Name() string // "openai" | "anthropic"
	Complete(ctx context.Context, req Request) (Response, error)
}

// DefaultBase returns the default API base URL for a provider name.
func DefaultBase(name string) string {
	switch name {
	case "anthropic":
		return "https://api.anthropic.com"
	default:
		return "https://api.openai.com/v1"
	}
}

// DefaultKeyEnv returns the conventional environment variable holding the API
// key for a provider name.
func DefaultKeyEnv(name string) string {
	switch name {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}

// Known reports whether name is a registered provider.
func Known(name string) bool {
	switch name {
	case "openai", "anthropic":
		return true
	}
	return false
}

// New builds the Provider named by name (e.g. "openai", "anthropic"). apiBase
// may be empty (the provider default is used). hc may be nil (a plain client
// with sane timeouts derived from request contexts is used). model must be
// non-empty.
func New(name, apiBase, apiKey, model string, hc HTTPClient) (Provider, error) {
	if !Known(name) {
		return nil, fmt.Errorf("unknown llm provider %q (known: openai, anthropic)", name)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required for llm provider %q", name)
	}
	if apiBase == "" {
		apiBase = DefaultBase(name)
	}
	if _, err := url.Parse(apiBase); err != nil {
		return nil, fmt.Errorf("invalid api_base %q for provider %q: %w", apiBase, name, err)
	}
	client := hc
	if client == nil {
		client = defaultClient()
	}
	switch name {
	case "anthropic":
		return &Anthropic{base: strings.TrimRight(apiBase, "/"), key: apiKey, model: model, hc: client}, nil
	default:
		return &OpenAI{base: strings.TrimRight(apiBase, "/"), key: apiKey, model: model, hc: client}, nil
	}
}
