# Orchestra

**The operating system for AI coding agents.** One interface, many coding agents. See [plan.md](plan.md) for the full roadmap.

---

## Where it's at: M4 — AI router

Orchestra dispatches coding-agent CLIs (Claude, OpenCode, Mimo, …) through one **supervised** interface.
You just chat: it **reads each message, answers plain questions directly, and routes coding tasks to the
best agent** itself. It validates every result (build → lint → test), lets the agent fix its own failures,
and keeps nothing without your `y`. It can also **decompose a big request into steps** and run them one at
a time, remembering every run.

Two ways to use it — **same engine underneath**:

### Interactive shell (the primary UX)

```sh
orchestra            # drops you into a chat session
```

```
orchestra (auto) › what does this repo's config loader do?
  … routing
  <answered inline — no agent dispatched>

orchestra (auto) › add a /health endpoint
  ↳ implement → agent "opencode" (code change)
  … agent runs, tests run, diff shown …
  accept these changes? [y/N] y
  ✓ changes accepted and committed

orchestra (auto) › @claude write tests for it   # force one task to a specific agent
orchestra (auto) › /route off                   # fall back to a fixed active agent
orchestra (claude) › /exit
```

The **AI router** is on by default: plain questions are answered inline, coding tasks auto-route to the
best agent (with a printed reason). Overrides: `@<name> <task>` forces an agent; `/route off` switches to
a fixed active agent (`/agent <name>`). Shell commands: `/agents`, `/route [on|off]`, `/agent`, `/help`, `/exit`.
Each accepted turn is committed, so the tree stays clean and every turn's diff shows only its own changes.

### One-shot (scriptable, for CI/workflows)

```sh
orchestra run "add a /health endpoint" --agent claude --test "go test ./..."
```

### Plans & sequential workflows

Decompose a large request into ordered steps, then execute them one at a time — you approve the plan
first, then review each step's diff:

```sh
orchestra plan "build user authentication"   # just show the decomposition
orchestra do   "build user authentication"   # plan → approve → run each step, commit as you go
```

`do` commits each accepted step and **halts at the first rejected step** (prior steps stay committed).

### Memory & history

Every run is recorded in `~/.orchestra/orchestra.db` (outside your repo, so it never dirties the tree):

```sh
orchestra history        # recent runs for this project + the agent you accept most
orchestra history --all  # across all projects
```

## How it works (supervised loop)

1. **Dispatch** the agent CLI in headless, auto-approve mode, streaming its output live.
2. **Validate** by running the pipeline (`build → lint → test`, stop at the first failure).
3. **Self-correct** — if a check fails, feed the failure back to the agent and let it retry in place,
   up to `max_retries` times, so you review a result that already builds and passes tests when possible.
4. **Review** — show the git diff + per-stage validation report and ask you to **accept** or **reject**.
5. **Accept** keeps the changes (the shell commits them); **reject** restores the tree exactly.

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
timeout: 10m

# Validation pipeline — each result must pass before you review it. Empty stages
# are skipped. On failure the agent retries with the failure output, up to max_retries.
validate:
  build: "go build ./..."
  lint:  "go vet ./..."
  test:  "go test ./..."
max_retries: 2

# AI router — reads each message, answers questions, routes tasks to an agent.
router:
  enabled: true
  agent: claude          # who classifies / answers
  routes:                # static fallback: intent → agent
    plan: claude
    implement: opencode
    review: claude

agents:
  - name: claude
    bin: claude
    args: ["-p", "--dangerously-skip-permissions"]
    capabilities: [plan, implement, review]
  - name: opencode
    bin: opencode
    args: ["run", "--dangerously-skip-permissions"]
    capabilities: [implement, review]
  - name: mimo
    bin: mimo
    args: ["run", "--dangerously-skip-permissions"]
    capabilities: [implement, review]
```

A config file overrides defaults and adds agents; matching names replace the built-in entry.

## Commands & flags

| Command             | Purpose                                            |
|---------------------|----------------------------------------------------|
| `orchestra`         | interactive chat shell with AI routing (default)   |
| `orchestra run`     | one-shot task dispatch                             |
| `orchestra plan`    | decompose a request into ordered steps (no coding) |
| `orchestra do`      | plan + execute steps sequentially, supervised      |
| `orchestra history` | recent runs + preferred agent                      |
| `orchestra agents`  | list agents and availability                       |
| `orchestra init`    | write a starter `orchestra.yaml`                   |

`run` flags: `--agent`, `--test`, `--retries`, `--timeout`, `--force`. `do`: `--agent`, `--yes`. Global: `--dir`.

## Architecture

```
cmd/orchestra        Cobra CLI: run / plan / do / history / agents / init / shell (default)
internal/agent       Agent interface + CLIAgent + registry + Querier
internal/config      YAML config + built-in agent defaults
internal/ui          terminal styling: gradient banner, spinners, colored diffs (TTY-aware)
internal/router      AI routing: Classifier (CLI now, API later) → Decision, 3-tier fallback
internal/planner     decompose a request into ordered steps (query-mode agent)
internal/engine      the supervised pipeline (dispatch → validate → retry → review)  ← shared by run/shell/do
internal/shell       interactive chat REPL
internal/memory      SQLite run history + preferred-agent hint (~/.orchestra)
internal/runner      generic process exec (stream / capture / timeout)               ← the engine's engine
internal/validate    build → lint → test pipeline, stop-on-first-failure
internal/gitutil     is-repo / is-clean / diff / restore / commit
internal/review      show the diff, prompt accept/reject
```

`runner` runs a process; `agent` says which command each agent is; `engine` is the supervised loop every
front door (`run`, shell, `do`) drives; `planner` + `do` add multi-step workflows; `router` picks the agent;
`memory` records it all. Next (M5): parallel execution with git-worktree isolation.
