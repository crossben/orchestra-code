package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crossben/orchestra-code/internal/llm"
)

func TestExpandEnvResolvesAndReportsMissing(t *testing.T) {
	t.Setenv("BYOK_SET", "sk-set")
	t.Setenv("BYOK_EMPTY", "")
	pairs, missing := expandEnv(map[string]string{
		"B_KEY":   "${BYOK_SET}",
		"A_PLAIN": "literal",
		"C_GONE":  "$BYOK_UNSET_XYZ",
		"D_EMPTY": "${BYOK_EMPTY}",
	})
	want := []string{"A_PLAIN=literal", "B_KEY=sk-set", "C_GONE=", "D_EMPTY="}
	if !slices.Equal(pairs, want) {
		t.Fatalf("pairs = %v, want %v (sorted, expanded)", pairs, want)
	}
	if len(missing) != 2 || missing[0].Var != "BYOK_UNSET_XYZ" || missing[0].For != "C_GONE" ||
		missing[1].Var != "BYOK_EMPTY" || missing[1].For != "D_EMPTY" {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestCLIAgentEnvReachesProcess(t *testing.T) {
	t.Setenv("BYOK_SRC", "sk-from-parent")
	a := New("echoer", "sh", []string{"-c", `printf '%s' "$GROQ_API_KEY"`, "sh"}, "", nil)
	a.SetEnv(map[string]string{"GROQ_API_KEY": "${BYOK_SRC}"})
	out, err := a.Query(context.Background(), Task{Prompt: "ignored", Dir: t.TempDir(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "sk-from-parent" {
		t.Fatalf("child saw %q", out)
	}
}

func TestCLIAgentHealthReportsMissingEnv(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true on PATH")
	}
	a := New("opencode-groq", "true", nil, "", nil)
	a.SetEnv(map[string]string{"GROQ_API_KEY": "${BYOK_NOT_SET_ABC}"})
	err := a.Health()
	var ee *EnvError
	if !errors.As(err, &ee) || ee.Var != "BYOK_NOT_SET_ABC" {
		t.Fatalf("want EnvError for BYOK_NOT_SET_ABC, got %v", err)
	}
	if !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Fatalf("message should name the env entry: %v", err)
	}
	t.Setenv("BYOK_NOT_SET_ABC", "x")
	if err := a.Health(); err != nil {
		t.Fatalf("healthy once set: %v", err)
	}
}

func TestCLIAgentMissingBinaryIsNotEnvError(t *testing.T) {
	a := New("ghost", "definitely-not-a-binary-xyz", nil, "", nil)
	var ee *EnvError
	if err := a.Health(); err == nil || errors.As(err, &ee) {
		t.Fatalf("want a plain not-installed error, got %v", err)
	}
}

func TestAPIAgentHealthMissingKeyIsEnvError(t *testing.T) {
	t.Setenv("BYOK_API_KEY", "")
	a, err := NewAPI("x", "anthropic", "m", "", "BYOK_API_KEY", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	herr := a.Health()
	var ee *EnvError
	if !errors.As(herr, &ee) || ee.Var != "BYOK_API_KEY" {
		t.Fatalf("want EnvError, got %v", herr)
	}
}

func TestAPIAgentKeyFromEnvBlock(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("WORK_ANTHROPIC_KEY", "sk-work")
	a, err := NewAPI("x", "anthropic", "m", "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.SetEnv(map[string]string{"ANTHROPIC_API_KEY": "${WORK_ANTHROPIC_KEY}"})
	if err := a.Health(); err != nil {
		t.Fatalf("env block should supply the key: %v", err)
	}
	fp := &fakeProvider{text: "OK"}
	a.prov = fp
	if _, err := a.Query(context.Background(), Task{Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if fp.last.APIKey != "sk-work" {
		t.Fatalf("request key = %q", fp.last.APIKey)
	}
}

// Regression: API agents built by NewAPI must actually send the key.
func TestAPIAgentSendsKeyOverHTTP(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("BYOK_OPENAI", "sk-real")
	a, err := NewAPI("x", "openai", "m", srv.URL, "BYOK_OPENAI", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pr := a.Probe(context.Background(), 5*time.Second); !pr.OK {
		t.Fatalf("probe: %+v", pr)
	}
	if got != "Bearer sk-real" {
		t.Fatalf("Authorization header = %q", got)
	}
}

func TestAPIAgentProbeAuthNamesKeyVar(t *testing.T) {
	a := newTestAPIAgent(t, &fakeProvider{err: &llm.Error{Provider: "anthropic", Kind: llm.ErrAuth, Status: 401}})
	pr := a.Probe(context.Background(), time.Second)
	if pr.OK || !strings.Contains(pr.Detail, "FAKE_KEY_ENV") {
		t.Fatalf("auth failure should name the key variable, got %+v", pr)
	}
}

func TestHealthLabel(t *testing.T) {
	if HealthLabel(nil) != "available" || HealthLabel(&EnvError{Var: "K"}) != "missing key" ||
		HealthLabel(errors.New("exec: not found")) != "not installed" {
		t.Fatal("unexpected labels")
	}
}
