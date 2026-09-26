# Router v2 — direct API classifier + history-aware agent resolution

Date: 2026-09-26

## Intent

1. **Cheaper, faster classification.** Today every message is classified by spawning the router
   agent's CLI in query mode (`claude -p …`), which costs seconds and a full agent session. A small
   hosted model (e.g. Haiku) called directly over HTTP classifies in well under a second.
2. **Use what Orchestra already knows.** The memory store records which agent's work was accepted in
   each directory, and which agent won benchmarks there. When the AI has no strong opinion, that track
   record is a better tie-breaker than a static route table.

Neither change touches the supervised loop: routing only chooses *which* agent runs. Dispatch,
validation, retry and diff review are unchanged, and an `@agent` override still bypasses the router.

## Part A — APIClassifier

`internal/router/classifier_api.go` adds `APIClassifier`, implementing `Classifier` by calling an
`llm.Provider` directly:

- System prompt = the same output contract as the CLI classifier (JSON `intent/agent/reason`, the
  four intent definitions, the allowed agent names). User message = the raw message.
- `MaxTokens: 256`, `Temperature: 0`, its own timeout (default 30s, independent of the task timeout).
- Output parsed by the existing lenient `parseClassification` (tolerates fences and prose).
- The router package imports `internal/llm` but never `internal/config`.

### Config

```yaml
router:
  agent: claude                 # still the answerer for plain questions
  classifier:                   # optional — omit to keep the CLI classifier
    provider: anthropic         # openai (default) | anthropic
    model: claude-haiku-4-5
    api_base: ""                # optional endpoint override
    api_key_env: ""             # optional; provider default (ANTHROPIC_API_KEY / OPENAI_API_KEY)
```

Merged field-by-field over the defaults like the other router fields (defaults leave it empty).

`BuildRouter` uses the APIClassifier only when `classifier.model` is set **and** the key env var is
non-empty **and** the provider can be constructed. Otherwise it silently falls back to the CLI
classifier — a missing key never fails startup. The answerer stays `router.agent`, so a cheap
classifier can pair with a strong answerer.

## Part B — history-aware resolution

Resolution order for a task (plan / implement / review):

1. the AI's explicit agent suggestion, if healthy;
2. the best agent by track record in this directory, if healthy and with enough samples;
3. the static route for the intent;
4. the default agent;
5. the first healthy registered agent.

The same order applies when classification fails (degraded to implement with no suggestion).

The router does not import memory. It defines

```go
type History interface {
    BestAgent(dir string, candidates []string) (name, reason string, ok bool)
}
```

set with `(*Router).WithHistory(h)`. A nil History keeps the previous behaviour. The router passes
only the currently healthy agent names as candidates.

### Scoring (`(*memory.Store).AgentScores` / `BestAgent`)

For each candidate agent, over rows whose `dir` equals the absolute project directory:

- **successes** = accepted runs + benchmark wins
- **samples** = all runs + benchmark wins + validation-failed benchmarks
  (`valid = 0 AND skipped = 0`). A valid-but-lost benchmark is neutral and not counted.
- **score** = (successes + 1) / (samples + 2) — Laplace-smoothed acceptance rate.

Pick rules:

- only candidates passed in are considered; others are ignored;
- a candidate needs **samples ≥ 3**;
- its score must beat 0.5 (more successes than failures) — history never *pushes* a poor agent;
- the highest score wins; if the top score is shared, there is **no pick** (fall through).

The reason explains the pick, e.g. `claude: 8/10 accepted in this dir` (successes/samples). The
router appends it to the classifier's reason: `<classifier reason> · history: claude: 8/10 …`.

## Wiring

- Shell (`orchestra` root) and dashboard: `BuildRouter(reg)`, then `WithHistory(mem)` when the
  memory store opened. If it didn't, history is skipped silently.
- `orchestra do` does not use the router (the planner assigns agents per step), so it is unchanged.

## Error handling

- API classifier errors (network, auth, timeout, unparsable output) surface as a classification
  error, which the router already degrades to "implement on the resolved agent".
- History query errors are swallowed (`ok = false`) — history is advisory.

## Testing

- `APIClassifier` with a fake `llm.Provider`: valid JSON, fenced JSON, garbage, provider error;
  asserts the request (system prompt contains choices, temperature 0, max tokens).
- Config: classifier merge; BuildRouter falls back to CLI when the key env is empty; uses API when set.
- Router: table test of the resolution order with a fake History and fake agents (`true` / missing
  binaries for health).
- Memory: temp-dir sqlite — min-sample threshold, ties, candidate filtering, benchmark weighting,
  dir scoping.
