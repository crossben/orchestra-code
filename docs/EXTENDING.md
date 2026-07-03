# Extending Orchestra — adding agents

Orchestra treats every agent as an interchangeable component behind one interface. There are two
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

## 2. Implement the `Agent` interface (for a built-in / non-CLI agent)

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
