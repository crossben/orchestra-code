package llm

import "context"

// OpenAI talks to the OpenAI-compatible chat completions API
// (POST {base}/chat/completions). The same wire format is spoken by many
// gateways (OpenRouter, Groq, Together, Ollama, vLLM, LM Studio), which is why
// it is the default provider.
type OpenAI struct {
	base  string // e.g. https://api.openai.com/v1
	key   string // bearer token; may be empty for local gateways
	model string
	hc    HTTPClient
}

// Name implements Provider.
func (o *OpenAI) Name() string { return "openai" }

// openaiRequest / openaiResponse model the minimal chat-completions wire shape.
type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature"`
}

type openaiMessage struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

type openaiResponse struct {
	Choices []struct {
		Message      openaiMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete implements Provider.
func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	msgs := make([]openaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openaiMessage{Role: m.Role, Content: m.Content})
	}
	payload := openaiRequest{Model: o.model, Messages: msgs, MaxTokens: req.MaxTokens, Temperature: req.Temperature}

	headers := map[string]string{}
	if o.key != "" {
		headers["Authorization"] = "Bearer " + o.key
	}

	var out openaiResponse
	url := o.base + "/chat/completions"
	if err := postJSON(ctx, o.hc, "openai", url, headers, payload, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, &Error{Provider: "openai", Kind: ErrServer, Body: "response contained no choices"}
	}
	return Response{
		Text:       out.Choices[0].Message.Content,
		StopReason: out.Choices[0].FinishReason,
	}, nil
}
