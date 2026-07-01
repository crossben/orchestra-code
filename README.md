# Orchestra

**The operating system for AI coding agents.** One interface, many coding agents. See [plan.md](plan.md) for the full roadmap.

---

## M0 — walking skeleton

The current milestone. One command proves the whole idea end to end with a single agent:

```
orchestra run "add a /health endpoint" --test "go test ./..."
```

What it does:

1. **Dispatches** the coding agent CLI (default `claude`) with your task, streaming its output live.
2. **Validates** the result by running your `--test` command (pass/fail).
3. **Reviews** — shows the git diff and asks you to **accept** or **reject**.
4. On **reject**, restores the working tree to exactly how it was.

This is *supervised, one task at a time*: nothing is kept without your explicit `y`.

### Requirements

- Go 1.26+
- `git` (the accept/reject loop is built on it)
- At least one agent CLI on your `PATH` (`claude`, `codex`, or `gemini`)
- A **clean working tree** (commit or stash first; reject reverts the tree). Override with `--force`.

### Build & run

```sh
go build -o bin/orchestra ./cmd/orchestra
./bin/orchestra run "your task here" --test "go test ./..."
```

### Flags

| Flag        | Default   | Meaning                                             |
|-------------|-----------|-----------------------------------------------------|
| `--agent`   | `claude`  | which agent CLI to dispatch (`claude\|codex\|gemini`) |
| `--dir`     | `.`       | repository/working directory                        |
| `--test`    | *(none)*  | test command to verify the result                   |
| `--timeout` | `10m`     | max time the agent may run                           |
| `--force`   | `false`   | run even if the working tree is dirty               |

### Architecture (M0)

```
cmd/orchestra        CLI entrypoint + `run` command
internal/runner      exec the agent, stream output, capture exit code   ← the engine
internal/validate    run the test command, report pass/fail
internal/gitutil     is-repo / is-clean / diff / restore
internal/review      show the diff, prompt accept/reject
```

`runner` is the engine; in later milestones the interactive shell and the AI router
become layers on top of it (see plan.md).

> **Note on Cobra:** the plan adopts Cobra for the CLI, but M0 is intentionally
> stdlib-only — a single `run` command doesn't justify a dependency, and zero deps
> means it builds offline. Cobra arrives in M1 when commands and config multiply.
# orchestra-code
