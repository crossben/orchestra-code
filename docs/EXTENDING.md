# Extending Orchestra — adding agents

Orchestra treats every agent as an interchangeable component behind one interface. There are three
ways to add one, from easiest to deepest.

---

## 1. Add any CLI agent via config (no code)

This is the primary extension point and covers almost every case. **Any command-line tool** that:

- takes the task prompt as its **last argument**, and
- edits files in its **working directory**,

is an Orchestra agent. Register it in `orchestra.yaml`:

```yaml
agents:
  - name: myagent
    bin: myagent-cli            # binary on PATH, or a path to a script
    args: ["--headless", "-y"]  # flags that put it in non-interactive/auto-approve mode
    dir_flag: "--dir"           # optional: for CLIs that ignore the process cwd (e.g. opencode)
    capabilities: [implement, review]   # plan | implement | review
```

Orchestra invokes it as:

```
myagent-cli --headless -y [--dir <working-dir>] "<task prompt>"
```

Notes:
- **Headless / auto-approve is required.** Orchestra's diff review *is* the human gate, so the agent
  must not block on its own permission prompts. (For Claude Code that's `-p --dangerously-skip-permissions`;
  for opencode/mimo, `run --dangerously-skip-permissions`.)
- **`dir_flag`** is only needed for agents that don't honor the process working directory. Orchestra
  always `cd`s into the target dir; if your agent respects that, omit `dir_flag`.
- **`capabilities`** feed the router and are informational otherwise.

### Try it

A complete, dependency-free example lives in [`examples/`](../examples):

```sh
# in a clean git repo, with examples/ copied in (or run from the orchestra repo)
cp examples/agents/notes-agent.sh .          # a trivial "agent" that edits NOTES.md
cat > orchestra.yaml <<'YAML'
default_agent: notes
validate: { auto: false }
agents:
  - name: notes
    bin: ./notes-agent.sh
    capabilities: [implement]
YAML
git add -A && git commit -m init
orchestra run "remember to add pagination"    # runs the agent → shows the diff → accept/reject
```

You'll see the full supervised loop against your custom agent.

---

## 2. Add an API-backed agent via config (no code)

If you'd rather call a hosted LLM directly than spawn a CLI, register an agent with
`type: api`. Orchestra sends the model a bounded snapshot of your repository plus the task,
the model replies with changes (a unified diff or whole-file blocks), Orchestra applies them
to the working tree, and everything flows through the same supervised loop:
validate → retry → diff review.

```yaml
agents:
  - name: gpt
    type: api
    provider: openai          # openai | anthropic
    model: gpt-4o             # required
    # api_base: https://api.openai.com/v1        # optional override (OpenRouter, Groq,
    #                                             # Ollama, vLLM… all speak the openai format)
    api_key_env: OPENAI_API_KEY                  # env var holding the key (provider default)
    context_budget: 98304                        # repo snapshot byte cap (default 96 KiB)
    capabilities: [plan, implement, review]
```

Notes:
- The API key is read from the environment at run time — never put keys in the YAML.
- `orchestra agents` shows these as `(api: <provider>/<model>)`, and `--probe` runs a real
  tiny completion so auth/billing problems surface with actionable detail.
- Because the model cannot explore the repo itself, the snapshot it receives matters: keep
  the tree lean, and raise `context_budget` if tasks touch many files.
- Self-correction works like any agent: on validation failure the failure text is fed back
  and the model re-edits its own prior changes in place.
- Works everywhere CLI agents do: `run`, shell, dashboard chat, planning, routing, and even
  `do --parallel` worktrees (patches apply inside each isolated worktree, or folder copy
  outside a git repository).

### Tokens and cost

API agents record the token usage the provider reports (OpenAI-compatible
`usage.prompt_tokens`/`completion_tokens`, Anthropic `usage.input_tokens`/`output_tokens`),
summed over every attempt of a run including self-correction retries. Gateways that omit
usage simply record nothing. To also see cost, set the model's prices — Orchestra ships no
price table, because prices change:

```yaml
agents:
  - name: sonnet
    type: api
    provider: anthropic
    model: claude-sonnet-4-5
    price_input_per_mtok: 3      # USD per 1M input tokens
    price_output_per_mtok: 15    # USD per 1M output tokens
```

Cost is computed only when both prices are set (> 0); otherwise tokens are still recorded
and cost shows as `—`. Usage appears in the end-of-run line
(`▸ tokens 12.3k in / 1.1k out · $0.04`), `orchestra history`, the `benchmark` leaderboard,
and the dashboard's History, Benchmarks and Agents (per-agent totals) tabs. CLI agents
don't report usage yet, so their runs show `—`.

### Bring your own key (`env:`)

Orchestra never holds or proxies keys. CLI agents keep their own login; API agents read a key from
the environment. The optional `env:` block on any agent lets you pick *which* key, without putting it
in `orchestra.yaml`:

```yaml
agents:
  - name: opencode-groq          # a second opencode profile on another provider
    bin: opencode
    args: ["run"]
    dir_flag: "--dir"
    env:
      GROQ_API_KEY: "${MY_GROQ_KEY}"      # passed into the opencode process

  - name: sonnet-work
    type: api
    provider: anthropic
    model: claude-sonnet-4-5
    env:
      ANTHROPIC_API_KEY: "${WORK_ANTHROPIC_KEY}"   # api agents use the api_key_env entry as their key
```

- Values reference variables as `$VAR` or `${VAR}` and are expanded each time the agent runs.
- If a referenced variable is unset, the agent shows as **missing key** (not "not installed") in
  `orchestra agents`, the dashboard's Agents tab and `--probe`, with the variable's name. Values are
  never printed.
- Literal values are allowed but `orchestra agents` warns when a `*KEY*`/`*TOKEN*`/`*SECRET*` entry
  holds one: `orchestra.yaml` is often committed.
- If the provider rejects a key, the probe names the variable it came from.

---

## 3. Implement the `Agent` interface (for a built-in / non-CLI agent)

For agents that aren't a subprocess (e.g. a direct API/gRPC client), implement the interface in
[`internal/agent/agent.go`](../internal/agent/agent.go):

```go
type Agent interface {
    Name() string
    Run(ctx context.Context, task Task) (Result, error) // do the work, edit files in task.Dir
    Health() error                                      // nil if usable
    Capabilities() []Capability
}
```

Optional interfaces unlock more features when implemented:

| Interface      | Enables                                             |
|----------------|----------------------------------------------------|
| `Querier`      | planning / decomposition (answer a prompt as text) |
| `QuietQuerier` | routing/classification without terminal output     |
| `QuietRunner`  | the dashboard's in-pane chat (captured, no streaming) |
| `Prober`       | `orchestra agents --probe` live health checks      |

`CLIAgent` in the same file is the reference implementation of all of these — read it as a template.

To wire a code-level agent in, register it where the registry is built
([`config.BuildRegistry`](../internal/config/config.go)). This is a small, well-contained change and a
great first contribution — see [CONTRIBUTING.md](../CONTRIBUTING.md).

> Runtime third-party binary plugins aren't needed: the config path above already lets anyone plug in
> any tool without touching Orchestra's code. Implementing the interface is for agents that live
> *inside* the process.
