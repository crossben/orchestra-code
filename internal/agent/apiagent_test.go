package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crossben/orchestra-code/internal/llm"
)

// fakeProvider is an in-memory llm.Provider: no network, deterministic replies.
type fakeProvider struct {
	name  string
	text  string
	err   error
	last  llm.Request
	calls int
}

func (f *fakeProvider) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return llm.Response{}, f.err
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Text: f.text}, nil
}

// apiRepo builds a clean git repo for Run tests (skips without git).
func apiRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "init") // committed baseline so status --porcelain is empty
	return dir
}

const diffReply = "```diff\n" +
	"diff --git a/README.md b/README.md\n" +
	"--- a/README.md\n" +
	"+++ b/README.md\n" +
	"@@ -1 +1,2 @@\n" +
	" # repo\n" +
	"+touched by model\n" +
	"```"

func newTestAPIAgent(t *testing.T, p llm.Provider) *APIAgent {
	t.Helper()
	a, err := NewAPI("fakeapi", "", "test-model", "http://unused", "FAKE_KEY_ENV", []Capability{CapImplement}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	a.prov = p
	t.Setenv("FAKE_KEY_ENV", "test-key")
	return a
}

func TestAPIAgentRunAppliesDiff(t *testing.T) {
	dir := apiRepo(t)
	fp := &fakeProvider{text: diffReply}
	a := newTestAPIAgent(t, fp)

	res, err := a.Run(context.Background(), Task{Prompt: "add a line", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code: %d", res.ExitCode)
	}
	got, rerr := os.ReadFile(filepath.Join(dir, "README.md"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(got), "touched by model") {
		t.Fatalf("patch not applied:\n%s", got)
	}
	if !strings.Contains(res.Output, "diff --git") {
		t.Fatal("Output should carry the raw model text")
	}
	// The user message must contain the snapshot and the task.
	if !strings.Contains(fp.last.Messages[0].Content, "=== TASK ===") ||
		!strings.Contains(fp.last.Messages[0].Content, "# repo") {
		t.Fatalf("user message missing snapshot/task:\n%.200s", fp.last.Messages[0].Content)
	}
	// The system message must carry the output contract.
	if !strings.Contains(fp.last.System, "REPLY FORMAT") {
		t.Fatalf("system contract missing:\n%s", fp.last.System)
	}
}

func TestAPIAgentRunProseReplyChangesNothing(t *testing.T) {
	dir := apiRepo(t)
	fp := &fakeProvider{text: "I could not find anything to change; which file do you mean?"}
	a := newTestAPIAgent(t, fp)

	res, err := a.Run(context.Background(), Task{Prompt: "do it", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("tree changed on prose reply:\n%s", out)
	}
	if !strings.Contains(res.Output, "which file do you mean") {
		t.Fatal("prose should surface via Output for question detection")
	}
}

func TestAPIAgentRunProviderErrorPropagates(t *testing.T) {
	dir := apiRepo(t)
	fp := &fakeProvider{err: &llm.Error{Provider: "openai", Kind: llm.ErrAuth, Status: 401}}
	a := newTestAPIAgent(t, fp)

	_, err := a.Run(context.Background(), Task{Prompt: "x", Dir: dir})
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrAuth {
		t.Fatalf("expected typed auth error, got %v", err)
	}
}

func TestAPIAgentQueryIsPlainCompletion(t *testing.T) {
	fp := &fakeProvider{text: "42"}
	a := newTestAPIAgent(t, fp)

	got, err := a.Query(context.Background(), Task{Prompt: "the answer?"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "42" {
		t.Fatalf("query: %q", got)
	}
	// Query mode must NOT inject the patch contract or a snapshot.
	if strings.Contains(fp.last.System, "REPLY FORMAT") {
		t.Fatalf("query system should be empty, got: %s", fp.last.System)
	}
	if strings.Contains(fp.last.Messages[0].Content, "=== TASK ===") {
		t.Fatal("query must not wrap the prompt in the task/snapshot envelope")
	}
	quiet, err := a.QueryQuiet(context.Background(), Task{Prompt: "q"})
	if err != nil || quiet != "42" {
		t.Fatalf("quiet query: %q %v", quiet, err)
	}
}

func TestAPIAgentRunQuietMatchesRun(t *testing.T) {
	dir := apiRepo(t)
	fp := &fakeProvider{text: diffReply}
	a := newTestAPIAgent(t, fp)

	res, err := a.RunQuiet(context.Background(), Task{Prompt: "add a line", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "diff --git") {
		t.Fatalf("unexpected quiet result: %+v", res)
	}
}

func TestAPIAgentHealthRequiresEnvVar(t *testing.T) {
	t.Setenv("FAKE_KEY_ENV", "") // present but empty → still unhealthy
	a, err := NewAPI("x", "openai", "m", "", "FAKE_KEY_ENV", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Health(); err == nil {
		t.Fatal("expected Health to fail with unset key")
	}
	t.Setenv("FAKE_KEY_ENV", "k")
	if err := a.Health(); err != nil {
		t.Fatalf("Health with key set: %v", err)
	}
}

func TestAPIAgentProbeMapsErrors(t *testing.T) {
	ok := newTestAPIAgent(t, &fakeProvider{text: "OK"})
	if pr := ok.Probe(context.Background(), time.Second); !pr.OK || pr.Detail != "responded" {
		t.Fatalf("probe ok: %+v", pr)
	}

	bad := newTestAPIAgent(t, &fakeProvider{err: &llm.Error{
		Provider: "openai", Kind: llm.ErrBilling, Status: 402,
	}})
	pr := bad.Probe(context.Background(), time.Second)
	if pr.OK {
		t.Fatal("expected failing probe")
	}
	if pr.Detail == "" || pr.Detail == "responded" {
		t.Fatalf("expected actionable detail, got %q", pr.Detail)
	}
}

func TestNewAPIDefaultsKeyEnvAndBudget(t *testing.T) {
	a, err := NewAPI("gpt", "openai", "gpt-4o", "", "", []Capability{CapPlan}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.keyEnv != "OPENAI_API_KEY" {
		t.Fatalf("keyEnv: %s", a.keyEnv)
	}
	if a.budget != DefaultContextBudget {
		t.Fatalf("budget: %d", a.budget)
	}
	if a.ProviderName() != "openai" || a.Model() != "gpt-4o" {
		t.Fatalf("accessors: %s %s", a.ProviderName(), a.Model())
	}
	if _, err := NewAPI("", "openai", "m", "", "", nil, 0); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if _, err := NewAPI("x", "nope", "m", "", "", nil, 0); err == nil {
		t.Fatal("unknown provider must be rejected")
	}
}
