package llm

import (
	"context"
	"strings"
)

// Anthropic talks to the Anthropic Messages API (POST {base}/v1/messages).
type Anthropic struct {
	base  string // e.g. https://api.anthropic.com
	key   string // sent as x-api-key
	model string
	hc    HTTPClient
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic" }

const anthropicVersion = "2023-06-01"

// anthropicRequest / anthropicResponse model the minimal messages wire shape.
type anthropicRequest struct {
	Model       string            `json:"model"`
	System      string            `json:"system,omitempty"`
	Messages    []anthropicTurn   `json:"messages"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature"`
}

type anthropicTurn struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"` // "text" | "tool_use" | ...
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete implements Provider.
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096 // Anthropic requires an explicit cap
	}
	turns := make([]anthropicTurn, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		turns = append(turns, anthropicTurn{Role: role, Content: m.Content})
	}
	payload := anthropicRequest{
		Model:       a.model,
		System:      req.System,
		Messages:    turns,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}

	headers := map[string]string{
		"x-api-key":         a.key,
		"anthropic-version": anthropicVersion,
	}

	var out anthropicResponse
	url := a.base + "/v1/messages"
	if err := postJSON(ctx, a.hc, "anthropic", url, headers, payload, &out); err != nil {
		return Response{}, err
	}

	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return Response{Text: b.String(), StopReason: out.StopReason}, nil
}
