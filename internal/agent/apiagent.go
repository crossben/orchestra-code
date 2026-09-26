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
	keyEnv string            // env var holding the API key
	env    map[string]string // optional env: block; may supply keyEnv (e.g. "${WORK_KEY}")
	prov   llm.Provider
	caps   []Capability
	budget int

	priceIn, priceOut float64 // USD per million tokens (0 = not priced)
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

// SetPricing sets the USD prices per million input/output tokens used to
// cost each run. Both must be > 0 for a cost to be computed; otherwise runs
// still report tokens but no cost.
func (a *APIAgent) SetPricing(inPerMTok, outPerMTok float64) {
	a.priceIn, a.priceOut = inPerMTok, outPerMTok
}

// Pricing returns the configured USD prices per million input/output tokens.
func (a *APIAgent) Pricing() (inPerMTok, outPerMTok float64) { return a.priceIn, a.priceOut }

// Name implements Agent.
func (a *APIAgent) Name() string { return a.name }

// Capabilities implements Agent.
func (a *APIAgent) Capabilities() []Capability { return a.caps }

// Model returns the model id this agent talks to.
func (a *APIAgent) Model() string { return a.model }

// ProviderName returns the llm provider backing this agent.
func (a *APIAgent) ProviderName() string { return a.prov.Name() }

// SetEnv sets the agent's env: block. Only the entry named by api_key_env is
// used: it lets one agent read its key from another variable, e.g.
// {"ANTHROPIC_API_KEY": "${WORK_ANTHROPIC_KEY}"}.
func (a *APIAgent) SetEnv(env map[string]string) { a.env = copyEnv(env) }

// apiKey resolves the key: the env: block's entry for keyEnv when present,
// otherwise the process environment. A missing key is an *EnvError.
func (a *APIAgent) apiKey() (string, error) {
	if raw, ok := a.env[a.keyEnv]; ok {
		pairs, missing := expandEnv(map[string]string{a.keyEnv: raw})
		if err := firstMissing(a.name, missing); err != nil {
			return "", err
		}
		key := strings.TrimPrefix(pairs[0], a.keyEnv+"=")
		if strings.TrimSpace(key) == "" {
			return "", &EnvError{Agent: a.name, Var: a.keyEnv}
		}
		return key, nil
	}
	key := os.Getenv(a.keyEnv)
	if strings.TrimSpace(key) == "" {
		return "", &EnvError{Agent: a.name, Var: a.keyEnv}
	}
	return key, nil
}

// Health reports whether the agent is usable without network I/O: its API key
// must resolve (mirrors CLIAgent's cheap binary-on-PATH check).
func (a *APIAgent) Health() error {
	_, err := a.apiKey()
	return err
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
	resp, err := a.complete(ctx, outputContract, snap+"\n\n=== TASK ===\n"+task.Prompt, task.Timeout)
	if err != nil {
		return Result{}, err
	}
	text, usage := resp.Text, a.usage(resp.Usage)
	p, _ := patch.Extract(text)
	if !p.Empty() {
		if err := patch.Apply(ctx, task.Dir, p); err != nil {
			// The tokens were spent even though the patch failed.
			return Result{Duration: time.Since(start), Usage: usage}, fmt.Errorf("apply model changes: %w", err)
		}
		progress(task.Output, "← applied %d diff(s) and %d file write(s) in %s", len(p.Diffs), len(p.Files), time.Since(start).Round(time.Second/10))
	} else {
		progress(task.Output, "← reply with no changes in %s", time.Since(start).Round(time.Second/10))
	}
	// Output carries the model's raw text so the engine's no-change/question
	// detection (package engine) sees exactly what the model said.
	return Result{ExitCode: 0, Duration: time.Since(start), Output: text, Usage: usage}, nil
}

// usage converts a provider's token counts into a (possibly priced) Usage.
// A reply without counts stays unknown rather than reading as free.
func (a *APIAgent) usage(u llm.Usage) Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Usage{}
	}
	cost, priced := Cost(u.InputTokens, u.OutputTokens, a.priceIn, a.priceOut)
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: cost, Known: true, Priced: priced}
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
	resp, err := a.complete(ctx, "", task.Prompt, task.Timeout)
	return resp.Text, err
}

// QueryQuiet implements QuietQuerier; identical to Query (never streams).
func (a *APIAgent) QueryQuiet(ctx context.Context, task Task) (string, error) {
	return a.Query(ctx, task)
}

const probeContract = "Reply with exactly the word OK and nothing else. Do not create or modify any files."

// Probe implements Prober: a trivial live completion that catches auth,
// billing, and connectivity problems a Health check cannot see.
func (a *APIAgent) Probe(ctx context.Context, timeout time.Duration) ProbeResult {
	resp, err := a.complete(ctx, probeContract, probeContract, timeout)
	text := resp.Text
	switch {
	case err != nil:
		detail := firstMeaningfulLine(err.Error())
		var le *llm.Error
		switch {
		case errors.As(err, &le) && le.Kind == llm.ErrAuth:
			detail = fmt.Sprintf("%s rejected the key in %s — check it and that it can use this model", le.Provider, a.keyEnv)
		case errors.As(err, &le) && le.UserDetail() != "":
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
func (a *APIAgent) complete(ctx context.Context, system, user string, timeout time.Duration) (llm.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	key, err := a.apiKey()
	if err != nil {
		return llm.Response{}, err
	}
	return a.prov.Complete(ctx, llm.Request{
		APIKey:      key,
		System:      system,
		Messages:    []llm.Message{{Role: "user", Content: user}},
		Temperature: 0,
	})
}
