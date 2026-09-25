package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/llm"
	"github.com/crossben/orchestra-code/internal/patch"
)

// APIAgent drives a hosted LLM over HTTP instead of spawning a CLI subprocess.
// Because a raw model cannot explore the repository the way a coding CLI can,
// each Run ships it a bounded snapshot of the repo and requires the reply to be
// machine-applicable changes (a unified diff or per-file blocks), which
// Orchestra then applies to the task's directory. From there the supervised
// engine treats it exactly like any other agent: validate, retry, review.
//
// Quiet variants equal their loud counterparts — an HTTP call never writes to
// the terminal — so the TUI can use this agent unmodified.
type APIAgent struct {
	name   string
	model  string
	keyEnv string // env var holding the API key
	prov   llm.Provider
	caps   []Capability
	budget int
}

// Compile-time proof that APIAgent satisfies the whole contract surface, so
// routing, planning, probing, and the TUI all light up via type assertion.
var (
	_ Agent        = (*APIAgent)(nil)
	_ Querier      = (*APIAgent)(nil)
	_ Prober       = (*APIAgent)(nil)
	_ QuietRunner  = (*APIAgent)(nil)
	_ QuietQuerier = (*APIAgent)(nil)
)

// NewAPI builds an APIAgent. provider is "openai" or "anthropic"; apiBase may
// be empty (provider default); keyEnv names the environment variable holding
// the API key (provider default when empty); budget caps the repo snapshot in
// bytes (<=0 → DefaultContextBudget).
func NewAPI(name, provider, model, apiBase, keyEnv string, caps []Capability, budget int) (*APIAgent, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("api agent name is required")
	}
	if strings.TrimSpace(provider) == "" {
		provider = "openai" // sensible default; explicit in config preferred
	}
	if keyEnv == "" {
		keyEnv = llm.DefaultKeyEnv(provider)
	}
	prov, err := llm.New(provider, apiBase, "", model, nil)
	if err != nil {
		return nil, fmt.Errorf("api agent %s: %w", name, err)
	}
	copied := make([]Capability, len(caps))
	copy(copied, caps)
	if budget <= 0 {
		budget = DefaultContextBudget
	}
	return &APIAgent{name: name, model: model, keyEnv: keyEnv, prov: prov, caps: copied, budget: budget}, nil
}

// Name implements Agent.
func (a *APIAgent) Name() string { return a.name }

// Capabilities implements Agent.
func (a *APIAgent) Capabilities() []Capability { return a.caps }

// Model returns the model id this agent talks to.
func (a *APIAgent) Model() string { return a.model }

// ProviderName returns the llm provider backing this agent.
func (a *APIAgent) ProviderName() string { return a.prov.Name() }

// Health reports whether the agent is usable without network I/O: the API key
// env var must be set (mirrors CLIAgent's cheap binary-on-PATH check).
func (a *APIAgent) Health() error {
	if v := os.Getenv(a.keyEnv); strings.TrimSpace(v) == "" {
		return fmt.Errorf("environment variable %s is not set (required by api agent %q)", a.keyEnv, a.name)
	}
	return nil
}

// outputContract is the strict reply format demanded of the model.
const outputContract = `You are a senior coding agent editing files inside the repository provided below. ` +
	"You cannot ask questions: make reasonable assumptions and act.\n\n" +
	"REPLY FORMAT (strict):\n" +
	"- To change files, reply with ONLY your changes in one of these two forms:\n" +
	"  1. A single fenced unified diff:\n" +
	"     ```diff\n" +
	"     <one complete unified diff>\n" +
	"     ```\n" +
	"  2. One fenced block per file, where the FIRST line of the block is the\n" +
	"     repo-relative file path and the rest is the ENTIRE new file content:\n" +
	"     ```\n" +
	"     path/to/file.go\n" +
	"     <full new content>\n" +
	"     ```\n" +
	"     To delete a file, make the body exactly <DELETE> on its own line.\n" +
	"- No prose before or after. No explanations. No other fences.\n" +
	"- If nothing needs changing, reply with one short plain-text sentence saying why.\n"

// Run implements Agent: snapshot → completion → extract → apply.
func (a *APIAgent) Run(ctx context.Context, task Task) (Result, error) {
	start := time.Now()
	snap, err := buildSnapshot(task.Dir, a.budget)
	if err != nil {
		return Result{}, fmt.Errorf("build repo snapshot: %w", err)
	}
	// There is no token stream, so progress is one line out and one line back.
	progress(task.Output, "→ asking %s (%s) · %d KiB of repo context", a.model, a.prov.Name(), len(snap)>>10)
	text, err := a.complete(ctx, outputContract, snap+"\n\n=== TASK ===\n"+task.Prompt, task.Timeout)
	if err != nil {
		return Result{}, err
	}
	p, _ := patch.Extract(text)
	if !p.Empty() {
		if err := patch.Apply(ctx, task.Dir, p); err != nil {
			return Result{Duration: time.Since(start)}, fmt.Errorf("apply model changes: %w", err)
		}
		progress(task.Output, "← applied %d diff(s) and %d file write(s) in %s", len(p.Diffs), len(p.Files), time.Since(start).Round(time.Second/10))
	} else {
		progress(task.Output, "← reply with no changes in %s", time.Since(start).Round(time.Second/10))
	}
	// Output carries the model's raw text so the engine's no-change/question
	// detection (package engine) sees exactly what the model said.
	return Result{ExitCode: 0, Duration: time.Since(start), Output: text}, nil
}

// progress writes one line to w when it is set.
func progress(w io.Writer, format string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", a...)
	}
}

// RunQuiet implements QuietRunner. API calls never stream, so this is Run.
func (a *APIAgent) RunQuiet(ctx context.Context, task Task) (Result, error) {
	return a.Run(ctx, task)
}

// Query implements Querier: a plain completion with no patch contract and no
// repo snapshot — used by the router (classification/answers) and planner.
func (a *APIAgent) Query(ctx context.Context, task Task) (string, error) {
	return a.complete(ctx, "", task.Prompt, task.Timeout)
}

// QueryQuiet implements QuietQuerier; identical to Query (never streams).
func (a *APIAgent) QueryQuiet(ctx context.Context, task Task) (string, error) {
	return a.Query(ctx, task)
}

const probeContract = "Reply with exactly the word OK and nothing else. Do not create or modify any files."

// Probe implements Prober: a trivial live completion that catches auth,
// billing, and connectivity problems a Health check cannot see.
func (a *APIAgent) Probe(ctx context.Context, timeout time.Duration) ProbeResult {
	text, err := a.complete(ctx, probeContract, probeContract, timeout)
	switch {
	case err != nil:
		detail := firstMeaningfulLine(err.Error())
		var le *llm.Error
		if errors.As(err, &le) && le.UserDetail() != "" {
			detail = le.UserDetail()
		}
		return ProbeResult{OK: false, Detail: detail}
	case strings.TrimSpace(text) == "":
		return ProbeResult{OK: false, Detail: "empty response"}
	default:
		return ProbeResult{OK: true, Detail: "responded"}
	}
}

// complete issues one provider call. system may be empty (plain query mode).
func (a *APIAgent) complete(ctx context.Context, system, user string, timeout time.Duration) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	resp, err := a.prov.Complete(ctx, llm.Request{
		System:      system,
		Messages:    []llm.Message{{Role: "user", Content: user}},
		Temperature: 0,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
