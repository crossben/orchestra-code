package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- factory ---

func TestNewRejectsUnknownProvider(t *testing.T) {
	if _, err := New("gemini-http", "", "", "m", nil); err == nil {
		t.Fatal("expected error for unknown provider name")
	}
}

func TestNewRequiresModel(t *testing.T) {
	if _, err := New("openai", "", "", "  ", nil); err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestNewDefaults(t *testing.T) {
	o, err := New("openai", "", "k", "gpt-4o", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.(*OpenAI).base != "https://api.openai.com/v1" {
		t.Fatalf("unexpected openai base: %s", o.(*OpenAI).base)
	}
	a, err := New("anthropic", "", "k", "claude-3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.(*Anthropic).base != "https://api.anthropic.com" {
		t.Fatalf("unexpected anthropic base: %s", a.(*Anthropic).base)
	}
	if DefaultKeyEnv("anthropic") != "ANTHROPIC_API_KEY" || DefaultKeyEnv("openai") != "OPENAI_API_KEY" {
		t.Fatal("unexpected default key env vars")
	}
}

// --- openai provider against a fake server ---

func TestOpenAICompleteRequestShapeAndParsing(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello world"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	p, err := New("openai", srv.URL, "sk-test", "gpt-4o", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), Request{
		System:      "be brief",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		MaxTokens:   100,
		Temperature: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("wrong path: %s", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("wrong auth header: %q", gotAuth)
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("system message not first: %v", first)
	}
	if gotBody["model"] != "gpt-4o" {
		t.Fatalf("model not sent: %v", gotBody["model"])
	}
	if resp.Text != "hello world" || resp.StopReason != "stop" {
		t.Fatalf("bad response: %+v", resp)
	}
	if p.Name() != "openai" {
		t.Fatalf("name: %s", p.Name())
	}
}

func TestOpenAIOmitsBearerWhenNoKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	p, _ := New("openai", srv.URL, "", "m", nil)
	if _, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatalf("expected no Authorization header, got %q", auth)
	}
}

// --- anthropic provider against a fake server ---

func TestAnthropicCompleteHeadersShapeAndParsing(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"answer"},{"type":"other","text":"ignored"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	p, err := New("anthropic", srv.URL, "ak-test", "claude-3-5-sonnet", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), Request{
		System:   "sys",
		Messages: []Message{{Role: "user", Content: "q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("wrong path: %s", gotPath)
	}
	if gotKey != "ak-test" || gotVersion != "2023-06-01" {
		t.Fatalf("wrong headers: key=%q version=%q", gotKey, gotVersion)
	}
	if gotBody["system"] != "sys" {
		t.Fatalf("system field: %v", gotBody["system"])
	}
	mt, _ := gotBody["max_tokens"].(float64)
	if mt <= 0 {
		t.Fatal("anthropic requires explicit max_tokens; none sent")
	}
	if resp.Text != "answer" { // non-text blocks ignored
		t.Fatalf("text concat wrong: %q", resp.Text)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop reason: %q", resp.StopReason)
	}
}

// --- error mapping (shared postJSON path) ---

func TestErrorMappingByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{401, ErrAuth}, {403, ErrAuth}, {402, ErrBilling},
		{429, ErrRateLimit}, {400, ErrRequest}, {404, ErrRequest},
		{500, ErrServer}, {503, ErrServer},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"message":"boom"}}`, tc.status)
		}))
		p, _ := New("openai", srv.URL, "k", "m", nil)
		_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}})
		srv.Close()
		var le *Error
		if !errors.As(err, &le) {
			t.Fatalf("status %d: not an *llm.Error: %v", tc.status, err)
		}
		if le.Kind != tc.want {
			t.Fatalf("status %d: kind=%v want=%v", tc.status, le.Kind, tc.want)
		}
		if le.Provider != "openai" {
			t.Fatalf("provider attribution missing: %+v", le)
		}
		if le.UserDetail() == "" {
			t.Fatalf("status %d: empty UserDetail", tc.status)
		}
	}
}

func TestTimeoutMapsToNetworkKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	p, _ := New("openai", srv.URL, "k", "m", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := p.Complete(ctx, Request{Messages: []Message{{Role: "user", Content: "x"}}})
	var le *Error
	if !errors.As(err, &le) || le.Kind != ErrNetwork {
		t.Fatalf("expected network-kind timeout error, got %v", err)
	}
}

func TestEmptyChoicesIsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	p, _ := New("openai", srv.URL, "k", "m", nil)
	_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}})
	var le *Error
	if !errors.As(err, &le) || le.Kind != ErrServer {
		t.Fatalf("expected server-kind error for empty choices, got %v", err)
	}
	if want := "no choices"; !strings.Contains(le.Error(), want) {
		t.Fatalf("unhelpful message %q; want it to contain %q", le.Error(), want)
	}
}
