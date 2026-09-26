# Token & cost tracking (API agents first)

Date: 2026-09-26 · Status: implemented

## Goal

Show how many tokens a run consumed and, when the user has told us the price,
what it cost — per run, per benchmark entry and per agent. API agents report
usage exactly; CLI agents stay "unknown" for now (no scraping of CLI output).

## Design

**llm.** `llm.Response` gains `Usage{InputTokens, OutputTokens int}`.
OpenAI-compatible replies are read from `usage.prompt_tokens` /
`usage.completion_tokens`; Anthropic from `usage.input_tokens` /
`usage.output_tokens`. A reply without a `usage` object yields zeros — never an
error (many local gateways omit it).

**agent.** `agent.Result` gains `Usage agent.Usage`:

```go
type Usage struct {
    InputTokens, OutputTokens int
    CostUSD                   float64
    Known                     bool // tokens were reported
    Priced                    bool // CostUSD was computed from configured prices
}
```

`Usage.Add` sums two usages (Known/Priced are OR-ed). `agent.Cost(in, out,
priceIn, priceOut)` computes USD from per-million-token prices and reports
whether both prices were set (> 0). `APIAgent.SetPricing` stores the prices;
`APIAgent.Run` fills `Result.Usage` from the provider reply (also on an
apply-failure result, since the tokens were spent). CLI agents leave it zero.

**Pricing is config-only** — no built-in price table (prices go stale):

```yaml
agents:
  - name: sonnet-api
    type: api
    provider: anthropic
    model: claude-sonnet-4-5
    price_input_per_mtok: 3      # USD per 1M input tokens
    price_output_per_mtok: 15    # USD per 1M output tokens
```

Cost is computed only when both prices are set; otherwise tokens are still
recorded and cost renders as "—".

**engine.** `Outcome.Usage` and `Turn.Usage` hold the sum across every attempt
(first try + self-correction retries), including an attempt whose agent call
errored. `EventAgentDone` carries that attempt's `Usage`, so the dashboard's
live panel can keep a running total. `run`/`do`/shell print one summary line
after the loop, only when usage is known:
`▸ tokens 12.3k in / 1.1k out · $0.04`.

**memory.** `runs` and `benchmarks` gain `tokens_in INTEGER NOT NULL DEFAULT 0`,
`tokens_out INTEGER NOT NULL DEFAULT 0`, `cost_usd REAL NOT NULL DEFAULT 0`,
added with the existing idempotent `hasColumn` + `ALTER TABLE ADD COLUMN`
pattern, so old databases upgrade in place. `Run`, `BenchRun`, `BenchRow` and
`AgentStats` carry the values; `StatsByAgent` sums them per agent. In storage,
zero tokens means "unknown" and zero cost means "not priced".

**display.** Compact cells: tokens as `12.3k/1.1k`, cost as `$0.04` (four
decimals under a cent), "—" when unknown.

- `orchestra history`: TOKENS and COST columns.
- `orchestra benchmark` leaderboard: TOKENS and COST columns.
- TUI History, Benchmarks: TOKENS (10) and COST (7) columns.
- TUI Agents: per-agent TOKENS and COST totals.
- TUI live run panel: running total under the timeline once known.

## Non-goals

Parsing CLI agent output for usage; a built-in price table; cache-token
pricing; tracking router/planner query usage (not part of a run).

## Tests

httptest fixtures for both providers with and without `usage`; cost math;
engine summing across a retry with a fake agent; memory migration from an
old-schema DB (twice, idempotent) and round trip; headless TUI renders of the
new columns.
