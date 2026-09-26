package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crossben/orchestra-code/internal/llm"
)

// fakeProvider is an offline llm.Provider returning a canned reply.
type fakeProvider struct {
	text string
	err  error
	got  llm.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	f.got = req
	if _, ok := ctx.Deadline(); !ok {
		return llm.Response{}, errors.New("classifier must set a deadline")
	}
	return llm.Response{Text: f.text}, f.err
}

func TestAPIClassifier(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		err     error
		want    Classification
		wantErr bool
	}{
		{name: "valid json", text: `{"intent":"implement","agent":"opencode","reason":"code change"}`,
			want: Classification{Intent: IntentImplement, Agent: "opencode", Reason: "code change"}},
		{name: "fenced json", text: "```json\n{\"intent\": \"Question\", \"agent\": \" \", \"reason\": \"asks\"}\n```",
			want: Classification{Intent: IntentQuestion, Reason: "asks"}},
		{name: "garbage", text: "I think you want to implement it", wantErr: true},
		{name: "provider error", err: errors.New("401 unauthorized"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{text: tc.text, err: tc.err}
			c := NewAPIClassifier(p, []string{"claude", "opencode"}, time.Second)
			got, err := c.Classify(context.Background(), "add pagination", "/tmp")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if p.got.Temperature != 0 || p.got.MaxTokens != apiClassifierMaxTokens {
				t.Fatalf("unexpected request params: %+v", p.got)
			}
			if !strings.Contains(p.got.System, "claude, opencode") {
				t.Fatalf("system prompt lacks agent choices: %q", p.got.System)
			}
			if len(p.got.Messages) != 1 || p.got.Messages[0].Role != "user" || p.got.Messages[0].Content != "add pagination" {
				t.Fatalf("unexpected messages: %+v", p.got.Messages)
			}
		})
	}
}

func TestAPIClassifierDefaultTimeout(t *testing.T) {
	c := NewAPIClassifier(&fakeProvider{}, nil, 0)
	if c.timeout != DefaultClassifierTimeout {
		t.Fatalf("timeout = %v, want %v", c.timeout, DefaultClassifierTimeout)
	}
}
