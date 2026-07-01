# Orchestra

**The operating system for AI coding agents.** One interface, many coding agents. See [plan.md](plan.md) for the full roadmap.

---

## Where it's at: M1 — multiple agents + interactive shell

Orchestra dispatches coding-agent CLIs (Claude, OpenCode, …) through one **supervised** interface:
it runs an agent, verifies the result, shows you the diff, and keeps nothing without your `y`.

Two ways to use it — **same engine underneath**:

### Interactive shell (the primary UX)

```sh
orchestra            # drops you into a chat session
```

```
orchestra (claude) › add a /health endpoint
  … agent runs, tests run, diff shown …
  accept these changes? [y/N] y
  ✓ changes accepted and committed

orchestra (claude) › @opencode write tests for it   # route one task to another agent
orchestra (claude) › /agent opencode                # switch the active agent
orchestra (opencode) › /exit
```

Shell commands: `/agents`, `/agent <name>`, `/help`, `/exit`. Per-message routing: `@<name> <task>`.
Each accepted turn is committed, so the tree stays clean and every turn's diff shows only its own changes.

> Agent selection is manual for now. **M4** adds the AI router that reads your message and picks the agent automatically.

### One-shot (scriptable, for CI/workflows)

```sh
orchestra run "add a /health endpoint" --agent claude --test "go test ./..."
```

## How it works (supervised loop)

1. **Dispatch** the agent CLI in headless, auto-approve mode, streaming its output live.
2. **Validate** by running your test command (pass/fail).
3. **Review** — show the git diff and ask you to **accept** or **reject**.
4. **Accept** keeps the changes (the shell commits them); **reject** restores the tree exactly.

Auto-approve is deliberate: *Orchestra's diff review is the human gate*, so agents must not block on their own permission prompts.

## Requirements

- Go 1.22+
- `git` (the accept/reject loop is built on it)
- At least one agent CLI on your `PATH` (`claude`, `opencode`, `codex`, or `gemini`)
- A **clean working tree** to start (reject reverts the tree). `run` accepts `--force`.

## Build & run

```sh
go build -o bin/orchestra ./cmd/orchestra
./bin/orchestra                 # interactive shell
./bin/orchestra agents          # list agents + availability
./bin/orchestra init            # write a starter orchestra.yaml
```

## Configuration

Orchestra works with no config (built-in defaults for the common agents). To customize, `orchestra init`
writes an `orchestra.yaml`:

```yaml
default_agent: claude
test_command: "go test ./..."   # verifies each result
timeout: 10m
agents:
  - name: claude
    bin: claude
    args: ["-p", "--dangerously-skip-permissions"]
    capabilities: [plan, implement, review]
  - name: opencode
    bin: opencode
    args: ["run", "--dangerously-skip-permissions"]
    capabilities: [implement, review]
```

A config file overrides defaults and adds agents; matching names replace the built-in entry.

## Commands & flags

| Command            | Purpose                                            |
|--------------------|----------------------------------------------------|
| `orchestra`        | interactive chat shell (default)                   |
| `orchestra run`    | one-shot task dispatch                             |
| `orchestra agents` | list agents and availability                        |
| `orchestra init`   | write a starter `orchestra.yaml`                    |

`run` flags: `--agent`, `--test`, `--timeout`, `--force`. Global: `--dir`.

## Architecture

```
cmd/orchestra        Cobra CLI: run / agents / init / shell (default)
internal/agent       Agent interface + CLIAgent + registry
internal/config      YAML config + built-in agent defaults
internal/engine      the supervised pipeline (dispatch → validate → review)   ← shared by run + shell
internal/shell       interactive chat REPL
internal/runner      generic process exec (stream, capture, timeout)          ← the engine's engine
internal/validate    run the test command, report pass/fail
internal/gitutil     is-repo / is-clean / diff / restore / commit
internal/review      show the diff, prompt accept/reject
```

`runner` runs a process; `agent` says which command each agent is; `engine` is the supervised loop both
front doors (`run` and the shell) drive. Next: the AI router (M4) becomes another layer over `engine`.
